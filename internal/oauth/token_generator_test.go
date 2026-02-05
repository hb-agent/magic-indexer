package oauth

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateAccessToken(t *testing.T) {
	token, err := GenerateAccessToken()
	if err != nil {
		t.Fatalf("GenerateAccessToken() error = %v", err)
	}

	// 32 bytes base64url encoded = 43 characters
	if len(token) != 43 {
		t.Errorf("GenerateAccessToken() length = %d, want 43", len(token))
	}

	// Generate another and verify they're different
	token2, _ := GenerateAccessToken()
	if token == token2 {
		t.Error("GenerateAccessToken() generated identical tokens")
	}
}

func TestGenerateRefreshToken(t *testing.T) {
	token, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken() error = %v", err)
	}

	if len(token) != 43 {
		t.Errorf("GenerateRefreshToken() length = %d, want 43", len(token))
	}
}

func TestGenerateAuthorizationCode(t *testing.T) {
	code, err := GenerateAuthorizationCode()
	if err != nil {
		t.Fatalf("GenerateAuthorizationCode() error = %v", err)
	}

	if len(code) != 43 {
		t.Errorf("GenerateAuthorizationCode() length = %d, want 43", len(code))
	}
}

func TestGenerateClientID(t *testing.T) {
	clientID, err := GenerateClientID()
	if err != nil {
		t.Fatalf("GenerateClientID() error = %v", err)
	}

	if !strings.HasPrefix(clientID, "client_") {
		t.Errorf("GenerateClientID() = %q, want prefix 'client_'", clientID)
	}

	// "client_" (7) + 22 (16 bytes base64url) = 29
	if len(clientID) != 29 {
		t.Errorf("GenerateClientID() length = %d, want 29", len(clientID))
	}
}

func TestGenerateClientSecret(t *testing.T) {
	secret, err := GenerateClientSecret()
	if err != nil {
		t.Fatalf("GenerateClientSecret() error = %v", err)
	}

	if len(secret) != 43 {
		t.Errorf("GenerateClientSecret() length = %d, want 43", len(secret))
	}
}

func TestGeneratePARRequestURI(t *testing.T) {
	uri, err := GeneratePARRequestURI()
	if err != nil {
		t.Fatalf("GeneratePARRequestURI() error = %v", err)
	}

	prefix := "urn:ietf:params:oauth:request_uri:"
	if !strings.HasPrefix(uri, prefix) {
		t.Errorf("GeneratePARRequestURI() = %q, want prefix %q", uri, prefix)
	}
}

func TestGenerateDPoPNonce(t *testing.T) {
	nonce, err := GenerateDPoPNonce()
	if err != nil {
		t.Fatalf("GenerateDPoPNonce() error = %v", err)
	}

	// 16 bytes base64url encoded = 22 characters
	if len(nonce) != 22 {
		t.Errorf("GenerateDPoPNonce() length = %d, want 22", len(nonce))
	}
}

func TestGenerateSessionID(t *testing.T) {
	sessionID, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("GenerateSessionID() error = %v", err)
	}

	if len(sessionID) != 22 {
		t.Errorf("GenerateSessionID() length = %d, want 22", len(sessionID))
	}
}

func TestGenerateState(t *testing.T) {
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() error = %v", err)
	}

	if len(state) != 22 {
		t.Errorf("GenerateState() length = %d, want 22", len(state))
	}
}

func TestCurrentTimestamp(t *testing.T) {
	now := time.Now().Unix()
	ts := CurrentTimestamp()

	// Should be within 1 second
	if ts < now-1 || ts > now+1 {
		t.Errorf("CurrentTimestamp() = %d, expected near %d", ts, now)
	}
}

func TestExpirationTimestamp(t *testing.T) {
	lifetime := int64(3600) // 1 hour
	now := CurrentTimestamp()
	exp := ExpirationTimestamp(lifetime)

	// Should be approximately now + lifetime
	expected := now + lifetime
	if exp < expected-1 || exp > expected+1 {
		t.Errorf("ExpirationTimestamp() = %d, expected near %d", exp, expected)
	}
}

func TestIsExpired(t *testing.T) {
	now := CurrentTimestamp()

	// Past timestamp should be expired
	if !IsExpired(now - 100) {
		t.Error("IsExpired(past) = false, want true")
	}

	// Future timestamp should not be expired
	if IsExpired(now + 100) {
		t.Error("IsExpired(future) = true, want false")
	}

	// Current timestamp should be expired (>= check)
	if !IsExpired(now) {
		t.Error("IsExpired(now) = false, want true")
	}
}

func TestIsExpiredWithSkew(t *testing.T) {
	now := CurrentTimestamp()

	// Past timestamp with skew should be expired
	if !IsExpiredWithSkew(now-100, 10) {
		t.Error("IsExpiredWithSkew(past, 10) = false, want true")
	}

	// Future timestamp with skew should still allow some grace
	// now >= expiresAt + skew
	if !IsExpiredWithSkew(now-10, 10) {
		t.Error("IsExpiredWithSkew(now-10, 10) = false, want true")
	}

	// Future timestamp beyond skew should not be expired
	if IsExpiredWithSkew(now+100, 10) {
		t.Error("IsExpiredWithSkew(now+100, 10) = true, want false")
	}
}
