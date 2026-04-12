// Package oauth provides AT Protocol OAuth implementation.
// PKCE (Proof Key for Code Exchange) implementation per RFC 7636.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// GenerateCodeVerifier generates a cryptographically random code verifier.
// Returns a 43-character URL-safe base64 string (32 random bytes encoded).
func GenerateCodeVerifier() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// GenerateCodeChallenge generates a code challenge from a code verifier using S256 method.
// SHA-256 hash of verifier, base64url encoded without padding.
func GenerateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// VerifyCodeChallenge verifies a code verifier against a code challenge.
// Returns true if the verifier matches the challenge using the specified method.
// Supported methods: "S256" (recommended), "plain" (discouraged but spec-compliant).
func VerifyCodeChallenge(verifier, challenge, method string) bool {
	// Use constant-time comparison even though these are public-ish
	// values. Defense-in-depth: subtle.ConstantTimeCompare also
	// handles the equal-lengths requirement cleanly.
	switch method {
	case "S256":
		computed := GenerateCodeChallenge(verifier)
		return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
	case "plain":
		return subtle.ConstantTimeCompare([]byte(verifier), []byte(challenge)) == 1
	default:
		return false
	}
}
