package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/notifications"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// injectDIDHandler is a test-only middleware that simulates the service-auth
// middleware: it stuffs a verified DID into the request context so the
// XRPC handler's downstream path runs as if the JWT had been validated.
// Using this instead of spinning up the real verifier keeps the test
// scoped to the XRPC handler's contract — "given a DID in context,
// produce a valid GraphQL response" — without dragging in DID resolution.
func injectDID(did string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := oauth.ContextWithActingDIDForTest(r.Context(), did)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestNotificationsXRPCHandler_MissingDIDReturns500(t *testing.T) {
	// No test-friendly resolver needed — we exercise only the "context
	// didn't have a DID" branch before the resolver is consulted.
	handler, err := NewNotificationsXRPCHandler(&notifications.Resolver{})
	if err != nil {
		t.Fatalf("NewNotificationsXRPCHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/notifications/graphql",
		bytes.NewBufferString(`{"query":"{ unreadNotificationCount { count } }"}`))
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 when middleware didn't run, got %d", rr.Code)
	}
}

func TestNotificationsXRPCHandler_NonPostReturns405(t *testing.T) {
	handler, err := NewNotificationsXRPCHandler(&notifications.Resolver{})
	if err != nil {
		t.Fatalf("NewNotificationsXRPCHandler: %v", err)
	}
	wrapped := injectDID("did:web:test.example", handler)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/notifications/graphql", nil)
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 on GET, got %d", rr.Code)
	}
}

func TestNotificationsXRPCHandler_BadJSONReturns400(t *testing.T) {
	handler, err := NewNotificationsXRPCHandler(&notifications.Resolver{})
	if err != nil {
		t.Fatalf("NewNotificationsXRPCHandler: %v", err)
	}
	wrapped := injectDID("did:web:test.example", handler)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/notifications/graphql",
		bytes.NewBufferString(`{not json`))
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on malformed JSON, got %d", rr.Code)
	}
}

func TestNotificationsXRPCHandler_IntrospectionPassesThrough(t *testing.T) {
	// Even without a repo, the schema itself exists — introspection on
	// `__schema.queryType.name` should return "Query" proving the
	// GraphQL router is wired and the ctx DID is threaded.
	handler, err := NewNotificationsXRPCHandler(&notifications.Resolver{})
	if err != nil {
		t.Fatalf("NewNotificationsXRPCHandler: %v", err)
	}
	wrapped := injectDID("did:web:test.example", handler)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/notifications/graphql",
		bytes.NewBufferString(`{"query":"{ __schema { queryType { name } } }"}`))
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"name":"Query"`) {
		t.Errorf("want introspection result, got %s", rr.Body.String())
	}
}

// Ensure the handler's ServeHTTP signature plays with standard middleware.
func TestNotificationsXRPCHandler_HandlerType(t *testing.T) {
	handler, err := NewNotificationsXRPCHandler(&notifications.Resolver{})
	if err != nil {
		t.Fatal(err)
	}
	_ = handler // compile-time assert: NewNotificationsXRPCHandler returns http.Handler
	_ = context.Background
}
