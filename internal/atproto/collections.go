package atproto

import "strings"

// ParseCollections parses a comma-separated list of AT Protocol collection
// NSIDs. Empty entries and whitespace are trimmed. Returns nil for empty input.
func ParseCollections(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
