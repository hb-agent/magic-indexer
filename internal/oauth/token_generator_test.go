package oauth

import (
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
