package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/metrics"
	"github.com/GainForest/hypergoat/internal/notifications"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// NewNotificationsXRPCHandler builds the `/notifications/graphql` handler
// for issue #57. It exposes the XRPC variants of the notification
// resolvers (reading the acting DID from the service-auth context
// instead of a GraphQL arg) as a standalone GraphQL schema — kept
// separate from the admin schema so the admin-key path remains unaware
// of the service-auth contract during the transition.
//
// The handler does NOT verify the JWT itself: that's the caller-chained
// ServiceAuthMiddleware's job. We only assert the context has a DID; an
// unset DID is a routing bug, not a caller bug, so we fail 500 rather
// than 401 to make it loud.
func NewNotificationsXRPCHandler(resolver *notifications.Resolver) (http.Handler, error) {
	didFromCtx := oauth.ActingDIDFromContext
	onReq := func(field string) {
		metrics.NotificationsRequest("xrpc", field)
	}
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name:   "Query",
		Fields: resolver.XRPCQueryFields(didFromCtx, onReq),
	})
	mutationType := graphql.NewObject(graphql.ObjectConfig{
		Name:   "Mutation",
		Fields: resolver.XRPCMutationFields(didFromCtx, onReq),
	})
	schema, err := graphql.NewSchema(graphql.SchemaConfig{
		Query:    queryType,
		Mutation: mutationType,
	})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := oauth.ActingDIDFromContext(r.Context()); !ok {
			// Routing bug: the middleware didn't run. Fail loud so ops
			// catches this in dev rather than silently serving data.
			slog.Error("[notifications-xrpc] handler called without acting DID in context")
			http.Error(w, "internal routing error", http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Query         string                 `json:"query"`
			OperationName string                 `json:"operationName"`
			Variables     map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result := graphql.Do(graphql.Params{
			Schema:         schema,
			RequestString:  body.Query,
			OperationName:  body.OperationName,
			VariableValues: body.Variables,
			Context:        r.Context(),
		})
		w.Header().Set("Content-Type", "application/json")
		if len(result.Errors) > 0 {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(result)
	}), nil
}

// NewAtprotoDIDHandler serves the `.well-known/atproto-did` endpoint
// required by the did:web spec. It emits the configured DOMAIN_DID as
// `text/plain` when the DID is of the `did:web:<host>` form and the
// host matches our own (so a did:plc deploy doesn't publish a bogus
// endpoint). Returns 404 otherwise.
func NewAtprotoDIDHandler(domainDID, expectedHost string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		const prefix = "did:web:"
		if domainDID == "" || len(domainDID) <= len(prefix) || domainDID[:len(prefix)] != prefix {
			http.NotFound(w, nil)
			return
		}
		host := domainDID[len(prefix):]
		if expectedHost != "" && host != expectedHost {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte(domainDID))
	})
}
