package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginMatcher(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		origin  string
		want    bool
	}{
		{
			name:    "exact match",
			origins: []string{"https://certs.social"},
			origin:  "https://certs.social",
			want:    true,
		},
		{
			name:    "exact mismatch",
			origins: []string{"https://certs.social"},
			origin:  "https://evil.example.com",
			want:    false,
		},
		{
			name:    "wildcard single label match",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://foo.vercel.app",
			want:    true,
		},
		{
			name:    "wildcard with dashes in label matches",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://certs-social-git-staging-hypercerts-foundation.vercel.app",
			want:    true,
		},
		{
			name:    "wildcard rejects multi-label",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://foo.bar.vercel.app",
			want:    false,
		},
		{
			name:    "wildcard rejects suffix-extension attack",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://vercel.app.attacker.com",
			want:    false,
		},
		{
			name:    "wildcard rejects bare suffix",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://vercel.app",
			want:    false,
		},
		{
			name:    "wildcard rejects empty prefix",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://.vercel.app",
			want:    false,
		},
		{
			name:    "wildcard requires scheme match",
			origins: []string{"https://*.vercel.app"},
			origin:  "http://foo.vercel.app",
			want:    false,
		},
		{
			name:    "wildcard with port matches same port",
			origins: []string{"http://*.local:3000"},
			origin:  "http://foo.local:3000",
			want:    true,
		},
		{
			name:    "wildcard with port rejects different port",
			origins: []string{"http://*.local:3000"},
			origin:  "http://foo.local:4000",
			want:    false,
		},
		{
			name:    "wildcard without port rejects origin with port",
			origins: []string{"https://*.vercel.app"},
			origin:  "https://foo.vercel.app:8443",
			want:    false,
		},
		{
			name:    "empty origin header is rejected",
			origins: []string{"https://certs.social"},
			origin:  "",
			want:    false,
		},
		{
			name:    "mixed list: exact + wildcard, exact hit",
			origins: []string{"https://certs.social", "https://*.vercel.app"},
			origin:  "https://certs.social",
			want:    true,
		},
		{
			name:    "mixed list: exact + wildcard, wildcard hit",
			origins: []string{"https://certs.social", "https://*.vercel.app"},
			origin:  "https://preview-abc.vercel.app",
			want:    true,
		},
		{
			name:    "mixed list: exact + wildcard, neither hit",
			origins: []string{"https://certs.social", "https://*.vercel.app"},
			origin:  "https://evil.com",
			want:    false,
		},
		{
			name:    "malformed wildcard pattern is ignored",
			origins: []string{"https://*.*.vercel.app", "https://certs.social"},
			origin:  "https://foo.bar.vercel.app",
			want:    false,
		},
		{
			name:    "prefix wildcard matches prefixed label",
			origins: []string{"https://certs-social-*.vercel.app"},
			origin:  "https://certs-social-abc.vercel.app",
			want:    true,
		},
		{
			name:    "prefix wildcard matches long real preview URL",
			origins: []string{"https://certs-social-*.vercel.app"},
			origin:  "https://certs-social-git-staging-hypercerts-foundation.vercel.app",
			want:    true,
		},
		{
			name:    "prefix wildcard rejects unrelated project",
			origins: []string{"https://certs-social-*.vercel.app"},
			origin:  "https://other-app.vercel.app",
			want:    false,
		},
		{
			name:    "prefix wildcard requires non-empty suffix",
			origins: []string{"https://certs-social-*.vercel.app"},
			origin:  "https://certs-social-.vercel.app",
			want:    false,
		},
		{
			name:    "prefix wildcard rejects multi-label host",
			origins: []string{"https://certs-social-*.vercel.app"},
			origin:  "https://certs-social-foo.bar.vercel.app",
			want:    false,
		},
		{
			name:    "suffix wildcard matches",
			origins: []string{"https://*-preview.example.com"},
			origin:  "https://feature-preview.example.com",
			want:    true,
		},
		{
			name:    "middle wildcard matches with prefix and suffix",
			origins: []string{"https://foo-*-bar.example.com"},
			origin:  "https://foo-abc-bar.example.com",
			want:    true,
		},
		{
			name:    "middle wildcard rejects when asterisk matches zero chars",
			origins: []string{"https://foo-*-bar.example.com"},
			origin:  "https://foo--bar.example.com",
			want:    false,
		},
		{
			name:    "multiple asterisks in leftmost label is rejected",
			origins: []string{"https://*-*.vercel.app"},
			origin:  "https://foo-bar.vercel.app",
			want:    false,
		},
		{
			name:    "asterisk in non-leftmost label is rejected",
			origins: []string{"https://foo.*.vercel.app"},
			origin:  "https://foo.abc.vercel.app",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildOriginMatcher(tt.origins)
			if got := m.allow(tt.origin); got != tt.want {
				t.Errorf("allow(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}

func TestCORSMiddleware_WildcardEchoesAllowedOrigin(t *testing.T) {
	mw := CORSMiddleware(CORSConfig{
		AllowedOrigins: []string{"https://certs.social", "https://*.vercel.app"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		origin     string
		wantAllow  string
		wantVary   bool
		wantStatus int
	}{
		{"https://certs.social", "https://certs.social", true, http.StatusOK},
		{"https://preview-xyz.vercel.app", "https://preview-xyz.vercel.app", true, http.StatusOK},
		{"https://evil.com", "", false, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.origin, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Errorf("Allow-Origin = %q, want %q", got, tc.wantAllow)
			}
			if got := rec.Header().Get("Vary"); (got == "Origin") != tc.wantVary {
				t.Errorf("Vary = %q, wantVary=%v", got, tc.wantVary)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestCORSMiddleware_PreflightShortCircuits(t *testing.T) {
	var called bool
	mw := CORSMiddleware(CORSConfig{
		AllowedOrigins: []string{"https://*.vercel.app"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "https://foo.vercel.app")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Error("next handler should not be called on OPTIONS preflight")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://foo.vercel.app" {
		t.Errorf("Allow-Origin = %q, want echo of request origin", got)
	}
}

// TestCORSMiddleware_ExposesXQueryTimeout — browser-based clients
// only see CORS-safelisted response headers unless explicitly
// exposed. The X-Query-Timeout header from issue #71's timeout
// shaping must be exposed so fetch / Apollo / urql can read it.
func TestCORSMiddleware_ExposesXQueryTimeout(t *testing.T) {
	mw := CORSMiddleware(CORSConfig{AllowedOrigins: []string{"https://certs.social"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://certs.social")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := rec.Header().Get("Access-Control-Expose-Headers")
	if got != "X-Query-Timeout" {
		t.Errorf("Access-Control-Expose-Headers = %q, want X-Query-Timeout", got)
	}
}
