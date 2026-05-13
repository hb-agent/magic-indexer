// Package graphql provides GraphQL schema building and HTTP handling.
package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/gqlerrors"

	"github.com/GainForest/hypergoat/internal/graphql/depth"
	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/graphql/schema"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/metrics"
)

// maxGraphQLBodyBytes caps the size of a POSTed GraphQL request body.
// 1 MiB is more than enough for any hand-written query; machine-generated
// persisted queries are shorter. Anything larger is almost certainly an
// attempt to exhaust memory via the JSON decoder.
const maxGraphQLBodyBytes = 1 << 20

// maxGraphQLQueryDepth bounds the nested selection depth a client can
// request. 15 is deeper than anything the lexicons produce in practice;
// it exists to reject pathological nesting attempts that fit inside
// the body cap.
const maxGraphQLQueryDepth = 15

// maxLoggedOperationName clamps the operationName field before
// logging. Operation names are attacker-controllable; without a
// clamp, a 1 MiB payload would land in slog and any downstream log
// aggregator. 128 chars is plenty for any real operation name and
// keeps log lines bounded.
const maxLoggedOperationName = 128

// publicRouteLabel is the metric route label for the public
// `/graphql` endpoint. Hardcoded to keep the metric label set
// bounded and out of any user-controllable code path.
const publicRouteLabel = "public"

// Handler handles GraphQL requests.
type Handler struct {
	schema         *graphql.Schema
	repos          *resolver.Repositories
	queryTimeoutMs int
}

// NewHandler creates a new GraphQL handler from a lexicon registry and repositories.
//
// queryTimeoutMs is the public-endpoint per-request budget (issue
// #71's Layer 2) — it travels with the handler so the timeout
// response shape includes the exact value the deadline was set to.
// Pass 0 in tests that don't exercise the timeout path; the
// timeout-detection code still runs but the response shape
// reports `budgetMs: 0`.
func NewHandler(registry *lexicon.Registry, repos *resolver.Repositories, queryTimeoutMs int) (*Handler, error) {
	builder := schema.NewBuilder(registry)
	s, err := builder.Build()
	if err != nil {
		return nil, err
	}

	return &Handler{schema: s, repos: repos, queryTimeoutMs: queryTimeoutMs}, nil
}

// clampOperationName trims op-name to maxLoggedOperationName chars
// and rejects any control characters (newline, carriage return,
// tab, etc). Returns "" for an entirely-unsafe value. Defensive
// shaping for slog and downstream log aggregators that split on
// newlines.
func clampOperationName(op string) string {
	if len(op) > maxLoggedOperationName {
		op = op[:maxLoggedOperationName]
	}
	for _, r := range op {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return op
}

// timeoutResponse builds the pinned QUERY_TIMEOUT GraphQL response
// shape per `docs/issue-71/plan.md`. Preserves any partial
// `result.Data` the resolver produced before cancellation; replaces
// `result.Errors` with the canonical single error.
//
// extensions.code is SCREAMING_SNAKE_CASE per the project
// convention recorded in AGENTS.md. extensions.retryable=false
// signals to Apollo / urql retry middleware that retrying will not
// help, preventing a timed-out query from piling on the pool.
func timeoutResponse(result *graphql.Result, budgetMs int) *graphql.Result {
	timeoutErr := gqlerrors.FormattedError{
		Message: "query exceeded server time budget",
		Path:    nil,
		Extensions: map[string]interface{}{
			"code":      "QUERY_TIMEOUT",
			"budgetMs":  budgetMs,
			"retryable": false,
		},
	}
	if result == nil {
		return &graphql.Result{Data: nil, Errors: []gqlerrors.FormattedError{timeoutErr}}
	}
	// Preserve partial result.Data; replace errors with the canonical
	// timeout entry only.
	result.Errors = []gqlerrors.FormattedError{timeoutErr}
	return result
}

// ServeHTTP handles GraphQL HTTP requests.
// CORS is handled by the router-level middleware; not duplicated here.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse the request
	var params struct {
		Query         string                 `json:"query"`
		OperationName string                 `json:"operationName"`
		Variables     map[string]interface{} `json:"variables"`
	}

	if r.Method == "GET" {
		params.Query = r.URL.Query().Get("query")
		params.OperationName = r.URL.Query().Get("operationName")
		// Variables from query string would need to be parsed from JSON
	} else {
		// Cap request body so an attacker can't stream unlimited JSON
		// and exhaust memory before the decoder returns.
		r.Body = http.MaxBytesReader(w, r.Body, maxGraphQLBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
	}

	// Pre-execution depth guard: reject queries nested beyond
	// maxGraphQLQueryDepth so an attacker cannot burn CPU on
	// query planning within the body cap.
	if err := depth.Check(params.Query, maxGraphQLQueryDepth); err != nil {
		if errors.Is(err, depth.ErrTooDeep) {
			http.Error(w, "query rejected: nested too deeply", http.StatusBadRequest)
			return
		}
		http.Error(w, "query rejected", http.StatusBadRequest)
		return
	}

	// Inject repositories into context
	ctx := resolver.WithRepositories(r.Context(), h.repos)

	// Execute the query
	result := graphql.Do(graphql.Params{
		Schema:         *h.schema,
		RequestString:  params.Query,
		OperationName:  params.OperationName,
		VariableValues: params.Variables,
		Context:        ctx,
	})

	// Per-request timeout detection (issue #71's Layer 2). The
	// middleware installs the deadline; the handler owns response
	// shaping because:
	//   - response headers must be written before the body is
	//     flushed (a middleware-level Set after next.ServeHTTP is a
	//     no-op);
	//   - chi's outer middleware.Timeout(60s) writes 504 in its
	//     defer if it observes DeadlineExceeded — by writing 200
	//     here first, that later 504 is silently discarded as a
	//     superfluous WriteHeader call.
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		op := clampOperationName(params.OperationName)
		slog.Warn("public GraphQL query exceeded budget",
			"route", publicRouteLabel,
			"budget_ms", h.queryTimeoutMs,
			"operation", op,
		)
		metrics.GraphQLQueryTimeout(publicRouteLabel)
		result = timeoutResponse(result, h.queryTimeoutMs)
		w.Header().Set("X-Query-Timeout", strconv.Itoa(h.queryTimeoutMs))
		// Defence against any caching proxy that decides to cache a
		// 200-with-errors body for a different request.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(result)
		return
	}

	// Write response — always return 200 per GraphQL-over-HTTP spec;
	// errors are conveyed in the response body's "errors" array.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// Schema returns the underlying GraphQL schema.
func (h *Handler) Schema() *graphql.Schema {
	return h.schema
}
