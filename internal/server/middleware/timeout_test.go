package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryTimeoutMiddleware_InstallsDeadline(t *testing.T) {
	var observedDeadline time.Time
	var observedOK bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observedDeadline, observedOK = r.Context().Deadline()
	})
	mw := QueryTimeoutMiddleware(5 * time.Second)(next)

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	rec := httptest.NewRecorder()
	before := time.Now()
	mw.ServeHTTP(rec, req)
	if !observedOK {
		t.Fatal("downstream handler did not see a context deadline")
	}
	// The deadline should be within a small window after the budget.
	delta := observedDeadline.Sub(before)
	if delta < 4500*time.Millisecond || delta > 5500*time.Millisecond {
		t.Errorf("deadline delta = %v, want ~5s", delta)
	}
}

func TestQueryTimeoutMiddleware_DoesNotInterceptFastResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mw := QueryTimeoutMiddleware(5 * time.Second)(next)

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Errorf("body = %q, want %q", got, `{"ok":true}`)
	}
}

func TestQueryTimeoutMiddleware_PreservesParentDeadline(t *testing.T) {
	// If the parent context already has a tighter deadline, our
	// timeout should compose: the effective deadline is the earlier
	// of the two.
	var observedRemaining time.Duration
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if dl, ok := r.Context().Deadline(); ok {
			observedRemaining = time.Until(dl)
		}
	})
	mw := QueryTimeoutMiddleware(10 * time.Second)(next)

	parentCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/graphql", nil).WithContext(parentCtx)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	// Effective deadline must respect the tighter parent (2s),
	// not our looser middleware budget (10s).
	if observedRemaining > 3*time.Second {
		t.Errorf("effective remaining = %v, want <= 3s (parent's 2s should win)", observedRemaining)
	}
}
