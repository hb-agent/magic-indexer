// Package oauth provides AT Protocol OAuth implementation.
// Secure token generation utilities.
package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// GenerateAccessToken generates a cryptographically secure access token.
// Returns a 43-character URL-safe base64 string (32 random bytes encoded).
func GenerateAccessToken() (string, error) {
	return generateRandomToken(32)
}

// GenerateRefreshToken generates a cryptographically secure refresh token.
// Returns a 43-character URL-safe base64 string (32 random bytes encoded).
func GenerateRefreshToken() (string, error) {
	return generateRandomToken(32)
}

// GenerateAuthorizationCode generates a cryptographically secure authorization code.
// Returns a 43-character URL-safe base64 string (32 random bytes encoded).
func GenerateAuthorizationCode() (string, error) {
	return generateRandomToken(32)
}

// GenerateSessionID generates a unique session identifier.
// Returns a 22-character URL-safe base64 string (16 random bytes encoded).
func GenerateSessionID() (string, error) {
	return generateRandomToken(16)
}

// GenerateState generates an OAuth state parameter.
// Returns a 22-character URL-safe base64 string (16 random bytes encoded).
func GenerateState() (string, error) {
	return generateRandomToken(16)
}

// generateRandomToken generates a random URL-safe base64 encoded token.
func generateRandomToken(numBytes int) (string, error) {
	bytes := make([]byte, numBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// CurrentTimestamp returns the current Unix timestamp in seconds.
func CurrentTimestamp() int64 {
	return time.Now().Unix()
}

// ExpirationTimestamp calculates an expiration timestamp from a lifetime.
func ExpirationTimestamp(lifetimeSeconds int64) int64 {
	return CurrentTimestamp() + lifetimeSeconds
}

// IsExpired checks if a timestamp has expired.
func IsExpired(expiresAt int64) bool {
	return CurrentTimestamp() >= expiresAt
}
