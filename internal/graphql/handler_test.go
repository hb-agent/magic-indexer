package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	graphqlgo "github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/graphql/resolver"
)

// createMinimalSchema creates a minimal GraphQL schema for testing
func createMinimalSchema() (*graphqlgo.Schema, error) {
	queryType := graphqlgo.NewObject(graphqlgo.ObjectConfig{
		Name: "Query",
		Fields: graphqlgo.Fields{
			"ping": &graphqlgo.Field{
				Type: graphqlgo.String,
				Resolve: func(p graphqlgo.ResolveParams) (interface{}, error) {
					return "pong", nil
				},
			},
		},
	})

	schema, err := graphqlgo.NewSchema(graphqlgo.SchemaConfig{
		Query: queryType,
	})
	if err != nil {
		return nil, err
	}
	return &schema, nil
}

func TestHandler_ServeHTTP_NoCORSInHandler(t *testing.T) {
	// CORS is handled by the router-level CORSMiddleware, not the handler.
	// Verify the handler does NOT set CORS headers directly.
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	t.Run("handler does not set CORS headers", func(t *testing.T) {
		body := map[string]interface{}{"query": "{ ping }"}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("handler should not set Access-Control-Allow-Origin (CORS is middleware's job)")
		}
	})
}

func TestHandler_ServeHTTP_POST(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	t.Run("valid POST request", func(t *testing.T) {
		body := map[string]interface{}{
			"query": "{ ping }",
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected data object in response")
		}

		if data["ping"] != "pong" {
			t.Errorf("expected ping to be 'pong', got %v", data["ping"])
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}
	})
}

func TestHandler_ServeHTTP_GET(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	t.Run("GET request with query parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/graphql?query={ping}", nil)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected data object in response")
		}

		if data["ping"] != "pong" {
			t.Errorf("expected ping to be 'pong', got %v", data["ping"])
		}
	})
}

func TestHandler_Schema(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	if handler.Schema() != schema {
		t.Error("Schema() did not return the expected schema")
	}
}

func TestHandler_ServeHTTP_ContentType(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	body := map[string]interface{}{
		"query": "{ ping }",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", contentType)
	}
}

func TestHandler_ServeHTTP_GraphQLError(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	handler := &Handler{schema: schema, repos: nil}

	// Query for a field that doesn't exist
	body := map[string]interface{}{
		"query": "{ nonexistent }",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// GraphQL errors are conveyed in the response body per spec; HTTP status is 200.
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if result["errors"] == nil {
		t.Error("expected errors in response")
	}
}

func TestHandler_ServeHTTP_WithRepositories(t *testing.T) {
	// Create a schema that accesses repositories from context
	queryType := graphqlgo.NewObject(graphqlgo.ObjectConfig{
		Name: "Query",
		Fields: graphqlgo.Fields{
			"hasRepos": &graphqlgo.Field{
				Type: graphqlgo.Boolean,
				Resolve: func(p graphqlgo.ResolveParams) (interface{}, error) {
					repos := resolver.GetRepositories(p.Context)
					return repos != nil, nil
				},
			},
		},
	})

	schema, err := graphqlgo.NewSchema(graphqlgo.SchemaConfig{
		Query: queryType,
	})
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	// Create handler with non-nil repos (even though they're empty)
	repos := &resolver.Repositories{}
	handler := &Handler{schema: &schema, repos: repos}

	body := map[string]interface{}{
		"query": "{ hasRepos }",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object in response, got %v", result)
	}

	if data["hasRepos"] != true {
		t.Errorf("expected hasRepos to be true, got %v", data["hasRepos"])
	}
}

// TestHandler_ClampOperationName verifies the slog-injection guard:
// operation names with control characters are rejected entirely;
// long names are truncated.
func TestHandler_ClampOperationName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"normal", "normal"},
		{"", ""},
		{"with newline\nattack", ""},
		{"with carriage\rreturn", ""},
		{"with\ttab", ""},
		{"with\x00null", ""},
		{strings.Repeat("a", 200), strings.Repeat("a", 128)},
	}
	for _, c := range cases {
		if got := clampOperationName(c.in); got != c.want {
			t.Errorf("clampOperationName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHandler_TimeoutResponse_ShapeIsPinned is the canonical golden
// snapshot of the QUERY_TIMEOUT error body. Consumers branch on this
// exact shape; future refactors must keep the JSON stable.
func TestHandler_TimeoutResponse_ShapeIsPinned(t *testing.T) {
	// Start from a result that has partial data — the timeout
	// handler must preserve Data and replace only Errors.
	result := &graphqlgo.Result{
		Data: map[string]interface{}{"a": "value-from-fast-field"},
	}
	out := timeoutResponse(result, 5000)
	if out.Data == nil {
		t.Fatal("partial Data was dropped; consumer cache-merging will break")
	}
	if len(out.Errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d", len(out.Errors))
	}
	e := out.Errors[0]
	if e.Message != "query exceeded server time budget" {
		t.Errorf("message = %q, want %q", e.Message, "query exceeded server time budget")
	}
	got := e.Extensions
	if got["code"] != "QUERY_TIMEOUT" {
		t.Errorf("extensions.code = %v, want QUERY_TIMEOUT", got["code"])
	}
	if got["budgetMs"] != 5000 {
		t.Errorf("extensions.budgetMs = %v, want 5000", got["budgetMs"])
	}
	if got["retryable"] != false {
		t.Errorf("extensions.retryable = %v, want false", got["retryable"])
	}
}

// TestHandler_TimeoutResponse_NilResultStillProducesShape covers the
// edge case where graphql.Do returned a nil result (shouldn't happen
// but the function shouldn't panic).
func TestHandler_TimeoutResponse_NilResultStillProducesShape(t *testing.T) {
	out := timeoutResponse(nil, 5000)
	if out == nil {
		t.Fatal("nil result must still yield a non-nil response")
	}
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "QUERY_TIMEOUT" {
		t.Errorf("nil-result path did not produce canonical error: %+v", out)
	}
}

// TestHandler_ServeHTTP_TimeoutPath end-to-end: a context with
// expired deadline triggers the full timeout shaping — header is
// set BEFORE body flush, body is the pinned shape, status is 200.
func TestHandler_ServeHTTP_TimeoutPath(t *testing.T) {
	schema, err := createMinimalSchema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	h := &Handler{schema: schema, repos: &resolver.Repositories{}, queryTimeoutMs: 5000}

	body := bytes.NewBufferString(`{"query":"{ping}","operationName":"TimeoutOp"}`)
	req := httptest.NewRequest(http.MethodPost, "/graphql", body)
	// Pre-expire the request context to simulate the middleware's
	// deadline having fired before the handler returns.
	ctx, cancel := context.WithDeadline(req.Context(), time.Now().Add(-time.Second))
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Query-Timeout"); got != "5000" {
		t.Errorf("X-Query-Timeout header = %q, want 5000", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var parsed map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errors, _ := parsed["errors"].([]interface{})
	if len(errors) != 1 {
		t.Fatalf("expected 1 error in body, got %v", parsed)
	}
	e0, _ := errors[0].(map[string]interface{})
	ext, _ := e0["extensions"].(map[string]interface{})
	if ext["code"] != "QUERY_TIMEOUT" {
		t.Errorf("extensions.code = %v, want QUERY_TIMEOUT", ext["code"])
	}
	if ext["retryable"] != false {
		t.Errorf("extensions.retryable = %v, want false", ext["retryable"])
	}
	// budgetMs survives JSON round-trip as float64.
	if v, ok := ext["budgetMs"].(float64); !ok || v != 5000 {
		t.Errorf("extensions.budgetMs = %v, want 5000", ext["budgetMs"])
	}
}
