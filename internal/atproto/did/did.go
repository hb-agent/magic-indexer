// Package did provides the canonical input-validation predicate for
// ATProto DID strings. Other packages may have looser checks tuned to
// their own scopes (e.g. oauth.HasDIDMethodPrefix is a prefix-only
// gate for token-bound DIDs); when validating attacker-influenced
// input that flows into SQL parameters or log messages, use this
// package.
//
// The predicate is intentionally stricter than the W3C DID syntax: it
// accepts only `[a-z]+` for the method name and `[A-Za-z0-9:._-]` for
// the method-specific identifier. This is sufficient for every DID
// method the indexer actually ingests (`did:plc:`, `did:web:`) and
// keeps the input surface narrow enough that no character in a valid
// DID can interact with shell, SQL, JSONB, or LIKE/ILIKE escape
// semantics.
package did

// Length bounds match the prior conservative check used by the
// notifications extractor: 8 is enough to fit `did:plc:` plus a
// minimal identifier; 256 is well above any realistic DID length and
// well below any pathological one that would distort a query plan.
const (
	minLen = 8
	maxLen = 256
)

// IsValid reports whether s is a syntactically-valid DID by the
// indexer's strict definition. It enforces:
//
//   - length in [8, 256] bytes
//   - prefix `did:` followed by a lowercase ASCII method name and a
//     colon (e.g. `did:plc:`, `did:web:`)
//   - method-specific identifier characters drawn from
//     `[A-Za-z0-9:._-]`
//   - no leading or trailing whitespace
//
// Uppercase characters in the method prefix are rejected so that
// canonical-form comparisons against stored DIDs cannot be defeated
// by case-folded input.
func IsValid(s string) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	// Leading/trailing whitespace is rejected. Inner whitespace is
	// already excluded by the charset below.
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return false
	}
	// `did:` prefix.
	if s[0] != 'd' || s[1] != 'i' || s[2] != 'd' || s[3] != ':' {
		return false
	}
	// Method name: one or more lowercase ASCII letters, terminated by `:`.
	i := 4
	for i < len(s) && s[i] != ':' {
		c := s[i]
		if c < 'a' || c > 'z' {
			return false
		}
		i++
	}
	if i == 4 || i >= len(s) {
		// empty method or missing terminating colon
		return false
	}
	// Skip the terminating colon, then validate the method-specific id.
	i++
	if i >= len(s) {
		return false
	}
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == ':' || c == '-' || c == '.' || c == '_':
		default:
			return false
		}
	}
	return true
}
