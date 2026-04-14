package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAtprotoDIDHandler_DidWebMatches(t *testing.T) {
	h := NewAtprotoDIDHandler("did:web:indexer.example", "indexer.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/atproto-did", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if body := strings.TrimSpace(rr.Body.String()); body != "did:web:indexer.example" {
		t.Errorf("body = %q", body)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestAtprotoDIDHandler_DidPLCReturns404(t *testing.T) {
	h := NewAtprotoDIDHandler("did:plc:abc123", "indexer.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/atproto-did", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("did:plc should 404, got %d", rr.Code)
	}
}

func TestAtprotoDIDHandler_HostMismatchReturns404(t *testing.T) {
	h := NewAtprotoDIDHandler("did:web:other.example", "indexer.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/atproto-did", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("host mismatch should 404, got %d", rr.Code)
	}
}

func TestAtprotoDIDHandler_Empty(t *testing.T) {
	h := NewAtprotoDIDHandler("", "")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/atproto-did", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("empty did should 404, got %d", rr.Code)
	}
}
