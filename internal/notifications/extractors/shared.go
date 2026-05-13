// Package extractors implements Notifier extractors for each watched collection.
package extractors

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/metrics"
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

// extractContributorDID resolves an ATProto union on
// `org.hypercerts.claim.activity#contributor.contributorIdentity`
// to a DID, or to "" if no DID can be read.
//
// The lexicon-compliant shape is a bare string DID. Production
// data from `certified.app` wraps it in an object with a `$type`
// discriminator and an `identity` field; both shapes are accepted
// as long as the resolved string passes did.IsValid. The
// strong-ref variant of the union (com.atproto.repo.strongRef) is
// not supported — those entries return "" and the caller drops
// the contributor.
//
// Every call increments hypergoat_contributor_identity_total with
// one of three outcomes: did (value resolved to a DID), non_did
// (a string was read but failed did.IsValid — typically a
// handle), unrecognized_shape (the value was neither a string nor
// an object with a string .identity, or .identity was empty —
// this is the operator's signal for strong-refs or unexpected
// drift).
func extractContributorDID(raw json.RawMessage) string {
	// Bare-string variant: the lexicon-compliant shape.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			metrics.ContributorIdentityUnrecognizedShape()
			return ""
		case did.IsValid(s):
			metrics.ContributorIdentityDID()
			return s
		default:
			metrics.ContributorIdentityNonDID()
			return ""
		}
	}
	// Object variant: production drift from certified.app.
	var obj struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		ident := strings.TrimSpace(obj.Identity)
		switch {
		case ident == "":
			// Object without .identity (strong-refs land here too).
			metrics.ContributorIdentityUnrecognizedShape()
			return ""
		case did.IsValid(ident):
			metrics.ContributorIdentityDID()
			return ident
		default:
			metrics.ContributorIdentityNonDID()
			return ""
		}
	}
	// Neither shape parsed (array, number, malformed JSON, etc).
	metrics.ContributorIdentityUnrecognizedShape()
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
