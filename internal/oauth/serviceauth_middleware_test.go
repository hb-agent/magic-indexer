package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceAuthMiddleware_HappyPath(t *testing.T) {
	iss := "did:web:happy.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)
	tok := signer.mint(t, baseClaims(iss, time.Now()))

	called := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		did, ok := ActingDIDFromContext(r.Context())
		if !ok || did != iss {
			t.Errorf("ctx DID = %q ok=%v, want %q true", did, ok, iss)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := ServiceAuthMiddleware(v, testLxm)(downstream)

	req := httptest.NewRequest("POST", "/graphql", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !called {
		t.Error("downstream not invoked")
	}
}

func TestServiceAuthMiddleware_MissingHeader(t *testing.T) {
	resolver := &fakeResolver{docs: map[string]*DIDDocument{}}
	v := newVerifier(resolver)
	handler := ServiceAuthMiddleware(v, testLxm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("downstream should not be called")
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("POST", "/graphql", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
	if wa := rr.Header().Get("WWW-Authenticate"); wa == "" {
		t.Errorf("WWW-Authenticate header missing")
	}
}

func TestServiceAuthMiddleware_BadToken(t *testing.T) {
	resolver := &fakeResolver{docs: map[string]*DIDDocument{}}
	v := newVerifier(resolver)
	handler := ServiceAuthMiddleware(v, testLxm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("downstream should not be called")
	}))
	req := httptest.NewRequest("POST", "/graphql", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestActingDIDFromContext_NoDID(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if did, ok := ActingDIDFromContext(req.Context()); ok || did != "" {
		t.Errorf("bare context should not have DID, got %q ok=%v", did, ok)
	}
}
