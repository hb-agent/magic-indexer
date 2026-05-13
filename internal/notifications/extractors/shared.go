// Package extractors implements Notifier extractors for each watched collection.
package extractors

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/notifications"
)

// clampSortAt parses an ISO-8601 datetime and clamps it to
// [now-SortAtMaxPast, now]. Bad inputs default to now.
func clampSortAt(createdAt string) time.Time {
	now := time.Now()
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		// Try RFC3339 without nanoseconds for cases where encoder dropped them.
		parsed, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return now
		}
	}
	if parsed.After(now) {
		return now
	}
	if parsed.Before(now.Add(-notifications.SortAtMaxPast)) {
		return now
	}
	return parsed
}

// isValidDID is a thin alias for the canonical did.IsValid predicate.
// Retained as a package-level shim for call-site brevity in the
// extractor files; new callers should import did.IsValid directly.
func isValidDID(s string) bool {
	return did.IsValid(s)
}

// extractContributorDID handles the ATProto union type on
// `org.hypercerts.claim.activity#contributor.contributorIdentity`:
// either a plain DID/identifier string, or a strongRef object.
// strongRef is not supported in v1 — we'd have to resolve the referenced record.
func extractContributorDID(raw json.RawMessage) string {
	// Try string form first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// countContributorsShallow returns a lower-bound count of commas inside the
// "contributors" array without doing a full JSON parse. Used to early-reject
// oversized records before allocating on the main parser.
//
// Not strictly accurate: nested structures inside contributors can over-count.
// Returns math.MaxInt on malformed input so the caller reliably rejects.
func countContributorsShallow(record []byte) int {
	key := []byte(`"contributors"`)
	idx := bytes.Index(record, key)
	if idx < 0 {
		return 0
	}
	// Find the opening [.
	start := bytes.IndexByte(record[idx:], '[')
	if start < 0 {
		return 0
	}
	start += idx
	depth := 0
	count := 0
	inString := false
	escaped := false
	for i := start; i < len(record); i++ {
		c := record[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			depth++
			if depth == 2 { // top-level items inside the array
				count++
			}
		case ']', '}':
			depth--
			if depth == 0 {
				return count
			}
		}
	}
	return count
}
