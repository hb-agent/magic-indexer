// Package oauth provides AT Protocol OAuth implementation.
// OAuth scope parsing and validation.
package oauth

import "strings"

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
