// Package oauth provides AT Protocol OAuth implementation.
// OAuth scope parsing and validation.
package oauth

import (
	"fmt"
	"strings"
)

// Standard AT Protocol scopes.
const (
	ScopeAtproto            = "atproto"
	ScopeTransitionGeneric  = "transition:generic"
	ScopeTransitionChatBsky = "transition:chat.bsky"
)

// ParseScopes parses a space-separated scope string into a slice.
func ParseScopes(scopeString string) []string {
	if scopeString == "" {
		return nil
	}

	parts := strings.Fields(scopeString)
	scopes := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			scopes = append(scopes, p)
		}
	}
	return scopes
}

// JoinScopes joins a slice of scopes into a space-separated string.
func JoinScopes(scopes []string) string {
	return strings.Join(scopes, " ")
}

// ValidateScopeFormat validates the format of a scope string.
// Scopes must be non-empty alphanumeric strings with optional dots, colons, and underscores.
func ValidateScopeFormat(scopeString string) error {
	if scopeString == "" {
		return nil // Empty scope is valid
	}

	scopes := ParseScopes(scopeString)
	for _, scope := range scopes {
		if err := validateSingleScope(scope); err != nil {
			return err
		}
	}
	return nil
}

// validateSingleScope validates a single scope value.
func validateSingleScope(scope string) error {
	if scope == "" {
		return fmt.Errorf("empty scope value")
	}

	// Check for valid characters: alphanumeric, dots, colons, underscores, hyphens
	for _, r := range scope {
		if !isValidScopeChar(r) {
			return fmt.Errorf("invalid character in scope: %q", string(r))
		}
	}

	// Scope must not start or end with a separator
	if scope[0] == '.' || scope[0] == ':' || scope[0] == '_' || scope[0] == '-' {
		return fmt.Errorf("scope cannot start with separator: %q", scope)
	}
	last := scope[len(scope)-1]
	if last == '.' || last == ':' || last == '_' || last == '-' {
		return fmt.Errorf("scope cannot end with separator: %q", scope)
	}

	return nil
}

// isValidScopeChar checks if a rune is valid in a scope string.
func isValidScopeChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '.' || r == ':' || r == '_' || r == '-'
}

// ContainsScope checks if a scope string contains a specific scope.
func ContainsScope(scopeString, target string) bool {
	scopes := ParseScopes(scopeString)
	for _, s := range scopes {
		if s == target {
			return true
		}
	}
	return false
}

// IsScopeSubset checks if requestedScopes is a subset of grantedScopes.
func IsScopeSubset(requestedScopes, grantedScopes string) bool {
	requested := ParseScopes(requestedScopes)
	granted := ParseScopes(grantedScopes)

	grantedSet := make(map[string]bool)
	for _, s := range granted {
		grantedSet[s] = true
	}

	for _, s := range requested {
		if !grantedSet[s] {
			return false
		}
	}
	return true
}

// FilterScopes filters a scope string to only include allowed scopes.
func FilterScopes(scopeString string, allowedScopes []string) string {
	if scopeString == "" {
		return ""
	}

	allowedSet := make(map[string]bool)
	for _, s := range allowedScopes {
		allowedSet[s] = true
	}

	scopes := ParseScopes(scopeString)
	filtered := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if allowedSet[s] {
			filtered = append(filtered, s)
		}
	}

	return JoinScopes(filtered)
}
