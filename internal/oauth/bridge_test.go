package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBridge_FetchProtectedResourceMetadata(t *testing.T) {
	metadata := ProtectedResourceMetadata{
		Resource:             "https://pds.example.com",
		AuthorizationServers: []string{"https://bsky.social"},
		ScopesSupported:      []string{"atproto", "transition:generic"},
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	result, err := bridge.FetchProtectedResourceMetadata(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Resource != metadata.Resource {
		t.Errorf("expected resource %q, got %q", metadata.Resource, result.Resource)
	}
	if len(result.AuthorizationServers) != 1 {
		t.Errorf("expected 1 auth server, got %d", len(result.AuthorizationServers))
	}
	if result.AuthorizationServers[0] != "https://bsky.social" {
		t.Errorf("expected auth server %q, got %q", "https://bsky.social", result.AuthorizationServers[0])
	}
}

func TestBridge_FetchProtectedResourceMetadata_Error(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	_, err := bridge.FetchProtectedResourceMetadata(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for 404 response")
	}

	var bridgeErr *BridgeError
	if !errors.As(err, &bridgeErr) {
		t.Errorf("expected BridgeError, got %T", err)
	} else if bridgeErr.Type != ErrTypeMetadataFetch {
		t.Errorf("expected error type %q, got %q", ErrTypeMetadataFetch, bridgeErr.Type)
	}
}

func TestBridge_FetchAuthorizationServerMetadata(t *testing.T) {
	par := "https://bsky.social/oauth/par"
	metadata := AuthorizationServerMetadata{
		Issuer:                             "https://bsky.social",
		AuthorizationEndpoint:              "https://bsky.social/oauth/authorize",
		TokenEndpoint:                      "https://bsky.social/oauth/token",
		PushedAuthorizationRequestEndpoint: &par,
		DPoPSigningAlgValuesSupported:      []string{"ES256"},
		ScopesSupported:                    []string{"atproto", "transition:generic"},
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	result, err := bridge.FetchAuthorizationServerMetadata(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Issuer != metadata.Issuer {
		t.Errorf("expected issuer %q, got %q", metadata.Issuer, result.Issuer)
	}
	if result.AuthorizationEndpoint != metadata.AuthorizationEndpoint {
		t.Errorf("expected auth endpoint %q, got %q", metadata.AuthorizationEndpoint, result.AuthorizationEndpoint)
	}
	if result.TokenEndpoint != metadata.TokenEndpoint {
		t.Errorf("expected token endpoint %q, got %q", metadata.TokenEndpoint, result.TokenEndpoint)
	}
	if result.PushedAuthorizationRequestEndpoint == nil || *result.PushedAuthorizationRequestEndpoint != par {
		t.Errorf("expected PAR endpoint %q", par)
	}
}

func TestBridge_ExchangeCode(t *testing.T) {
	dpopKey, _ := GenerateDPoPKeyPair()

	tokenResponse := TokenResponse{
		AccessToken:  "access-token-123",
		TokenType:    "DPoP",
		ExpiresIn:    3600,
		RefreshToken: "refresh-token-456",
		Sub:          "did:plc:test123",
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("DPoP") == "" {
			t.Error("missing DPoP header")
		}

		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("expected grant_type authorization_code, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "auth-code-789" {
			t.Errorf("expected code auth-code-789, got %s", r.Form.Get("code"))
		}
		if r.Form.Get("code_verifier") == "" {
			t.Error("missing code_verifier")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	result, err := bridge.ExchangeCode(context.Background(), ExchangeCodeRequest{
		TokenEndpoint: server.URL + "/oauth/token",
		Issuer:        server.URL,
		Code:          "auth-code-789",
		CodeVerifier:  "test-verifier",
		RedirectURI:   "https://example.com/callback",
		DPoPKey:       dpopKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.AccessToken != tokenResponse.AccessToken {
		t.Errorf("expected access token %q, got %q", tokenResponse.AccessToken, result.AccessToken)
	}
	if result.RefreshToken != tokenResponse.RefreshToken {
		t.Errorf("expected refresh token %q, got %q", tokenResponse.RefreshToken, result.RefreshToken)
	}
	if result.Sub != tokenResponse.Sub {
		t.Errorf("expected sub %q, got %q", tokenResponse.Sub, result.Sub)
	}
}

func TestBridge_ExchangeCode_DPoPNonceRetry(t *testing.T) {
	dpopKey, _ := GenerateDPoPKeyPair()
	callCount := 0

	tokenResponse := TokenResponse{
		AccessToken:  "access-token-123",
		TokenType:    "DPoP",
		ExpiresIn:    3600,
		RefreshToken: "refresh-token-456",
		Sub:          "did:plc:test123",
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		if callCount == 1 {
			// First request: require nonce
			w.Header().Set("DPoP-Nonce", "server-nonce-abc")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "use_dpop_nonce"})
			return
		}

		// Second request: should have nonce
		dpopProof := r.Header.Get("DPoP")
		if dpopProof == "" {
			t.Error("missing DPoP header on retry")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	result, err := bridge.ExchangeCode(context.Background(), ExchangeCodeRequest{
		TokenEndpoint: server.URL + "/oauth/token",
		Issuer:        server.URL,
		Code:          "auth-code-789",
		CodeVerifier:  "test-verifier",
		RedirectURI:   "https://example.com/callback",
		DPoPKey:       dpopKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls (initial + retry), got %d", callCount)
	}

	if result.AccessToken != tokenResponse.AccessToken {
		t.Errorf("expected access token %q, got %q", tokenResponse.AccessToken, result.AccessToken)
	}
}

func TestBridge_RefreshTokens(t *testing.T) {
	dpopKey, _ := GenerateDPoPKeyPair()

	tokenResponse := TokenResponse{
		AccessToken:  "new-access-token",
		TokenType:    "DPoP",
		ExpiresIn:    3600,
		RefreshToken: "new-refresh-token",
		Sub:          "did:plc:test123",
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type refresh_token, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "old-refresh-token" {
			t.Errorf("expected refresh_token old-refresh-token, got %s", r.Form.Get("refresh_token"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	result, err := bridge.RefreshTokens(context.Background(), RefreshTokensRequest{
		TokenEndpoint: server.URL + "/oauth/token",
		Issuer:        server.URL,
		RefreshToken:  "old-refresh-token",
		DPoPKey:       dpopKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.AccessToken != tokenResponse.AccessToken {
		t.Errorf("expected access token %q, got %q", tokenResponse.AccessToken, result.AccessToken)
	}
	if result.RefreshToken != tokenResponse.RefreshToken {
		t.Errorf("expected refresh token %q, got %q", tokenResponse.RefreshToken, result.RefreshToken)
	}
}

func TestBridge_PushAuthorizationRequest(t *testing.T) {
	dpopKey, _ := GenerateDPoPKeyPair()

	parResponse := PARResponse{
		RequestURI: "urn:ietf:params:oauth:request_uri:test123",
		ExpiresIn:  90,
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("DPoP") == "" {
			t.Error("missing DPoP header")
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(parResponse)
	}))
	defer server.Close()

	bridge := NewBridge(BridgeConfig{
		HTTPClient: server.Client(),
		ClientID:   "https://example.com/client-metadata.json",
	})

	params := url.Values{
		"client_id":             {"https://example.com/client-metadata.json"},
		"response_type":         {"code"},
		"redirect_uri":          {"https://example.com/callback"},
		"scope":                 {"atproto transition:generic"},
		"code_challenge":        {"test-challenge"},
		"code_challenge_method": {"S256"},
	}

	result, err := bridge.PushAuthorizationRequest(context.Background(), server.URL+"/oauth/par", params, dpopKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.RequestURI != parResponse.RequestURI {
		t.Errorf("expected request_uri %q, got %q", parResponse.RequestURI, result.RequestURI)
	}
	if result.ExpiresIn != parResponse.ExpiresIn {
		t.Errorf("expected expires_in %d, got %d", parResponse.ExpiresIn, result.ExpiresIn)
	}
}

func TestBuildAuthorizationURL(t *testing.T) {
	tests := []struct {
		name         string
		authEndpoint string
		params       url.Values
		want         string
	}{
		{
			name:         "simple endpoint",
			authEndpoint: "https://bsky.social/oauth/authorize",
			params: url.Values{
				"client_id":     {"test-client"},
				"response_type": {"code"},
			},
			want: "https://bsky.social/oauth/authorize?client_id=test-client&response_type=code",
		},
		{
			name:         "endpoint with existing params",
			authEndpoint: "https://bsky.social/oauth/authorize?foo=bar",
			params: url.Values{
				"client_id": {"test-client"},
			},
			want: "https://bsky.social/oauth/authorize?foo=bar&client_id=test-client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAuthorizationURL(tt.authEndpoint, tt.params)
			// URL encoding order may vary, so we need to parse and compare
			gotURL, _ := url.Parse(got)
			wantURL, _ := url.Parse(tt.want)

			if gotURL.Scheme != wantURL.Scheme || gotURL.Host != wantURL.Host || gotURL.Path != wantURL.Path {
				t.Errorf("BuildAuthorizationURL() base = %q, want base = %q", got, tt.want)
			}
		})
	}
}

func TestBridgeError(t *testing.T) {
	t.Run("with cause", func(t *testing.T) {
		cause := &BridgeError{Type: ErrTypeHTTP, Message: "connection failed"}
		err := &BridgeError{Type: ErrTypeDIDResolution, Message: "failed to resolve", Cause: cause}

		//nolint:errorlint // testing Unwrap returns exact same object
		if err.Unwrap() != cause {
			t.Error("Unwrap should return cause")
		}

		errStr := err.Error()
		if errStr == "" {
			t.Error("Error should return non-empty string")
		}
	})

	t.Run("without cause", func(t *testing.T) {
		err := &BridgeError{Type: ErrTypeInvalidResponse, Message: "bad json"}

		if err.Unwrap() != nil {
			t.Error("Unwrap should return nil for no cause")
		}

		errStr := err.Error()
		if errStr != "invalid_response: bad json" {
			t.Errorf("unexpected error string: %s", errStr)
		}
	})
}

func TestCreateClientAssertion(t *testing.T) {
	keyPair, err := GenerateDPoPKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}

	bridge := NewBridge(BridgeConfig{
		ClientID:  "test-client",
		SigningKey: keyPair,
	})

	token, err := bridge.createClientAssertion("https://example.com")
	if err != nil {
		t.Fatalf("failed to create client assertion: %v", err)
	}

	// Should be a valid 3-part JWT
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("JWT should have 3 parts, got %d", len(parts))
	}
	for i, part := range parts {
		if part == "" {
			t.Errorf("JWT part %d should not be empty", i)
		}
	}
}

func TestCreateClientAssertion_NoKey(t *testing.T) {
	bridge := NewBridge(BridgeConfig{
		ClientID: "test-client",
	})

	_, err := bridge.createClientAssertion("https://example.com")
	if err == nil {
		t.Error("expected error when no signing key is configured")
	}
}
