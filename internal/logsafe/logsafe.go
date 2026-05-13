// Package logsafe provides scrubbing helpers for slog attributes
// whose values come from user-controllable input. The slog text
// handler already quotes control characters in its output, but
// downstream log aggregators that split on newlines or interpret
// ANSI escapes can still be confused by tainted values. These
// helpers are belt-and-braces — apply at every audit-log site.
//
// Two helpers cover the surface we audit:
//
//   - DID:    fast happy path for values that callers *expect* are
//             valid DIDs. Returns the sentinel "<invalid-did>" if
//             validation fails so the audit log still records the
//             presence of an attribute, just without a forgeable
//             value.
//   - String: byte-for-byte scrub of arbitrary operator-controlled
//             input (URLs, free-form fields). Truncates at
//             maxLogValueLen and replaces every control char and
//             both Unicode line-separator codepoints with U+FFFD.
//
// These helpers exist because slog audit lines are the security
// boundary, not the format. Applying them at every emission site
// means a future bug bypassing upstream validation still cannot
// inject a newline into an `event=…` line and forge a second
// audit record.
package logsafe

import (
	"strings"
	"unicode/utf8"

	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
)

// maxLogValueLen bounds the length of a single scrubbed attribute
// value. 256 bytes is enough for any DID, every host:port, and any
// URL we expose through settings. An attacker shipping a megabyte
// of payload through `relayUrl` is otherwise free to flood the log
// pipeline.
const maxLogValueLen = 256

// invalidDIDSentinel is logged in place of a DID that does not
// pass did.IsValid. The value is chosen to be unmistakeable in a
// log scan (`<invalid-did>` won't ever be a real DID) while
// preserving the presence of the attribute. Downstream alerting
// can match on this string when the upstream validation regresses.
const invalidDIDSentinel = "<invalid-did>"

// replacement is the single replacement rune (U+FFFD) used for
// every scrubbed character. Pre-encoded to a string so the inner
// loop can use a single WriteString call.
const replacement = "�"

// lineSep and paraSep are the two Unicode line-terminator
// codepoints downstream log aggregators (notably Logstash with
// the multiline filter) treat as line boundaries. slog's text
// handler does NOT escape these by default, so we have to.
const (
	lineSep = ' ' // LINE SEPARATOR
	paraSep = ' ' // PARAGRAPH SEPARATOR
)

// DID returns s if it passes did.IsValid, otherwise the sentinel
// "<invalid-did>" marker. Apply at audit-log sites where the
// callsite already validated the DID upstream — the helper is
// defense-in-depth: if a future bug bypasses did.IsValid, the log
// line is still well-formed.
//
// did.IsValid bounds the value (validated DIDs are bounded by
// the DID spec), so no length cap is applied on the happy path.
func DID(s string) string {
	if didpkg.IsValid(s) {
		return s
	}
	return invalidDIDSentinel
}

// String returns s with control characters replaced by U+FFFD and
// truncated at maxLogValueLen bytes. Characters scrubbed:
//
//   - all ASCII < 0x20 (newline, tab, escape, …)
//   - DEL (0x7f)
//   - U+2028 LINE SEPARATOR and U+2029 PARAGRAPH SEPARATOR
//
// Other non-ASCII printables pass through. slog's text handler
// will quote-encode the remaining bytes; the goal here is to make
// sure none of the scrubbed sentinels can land in the line at all.
//
// Truncation runs after replacement so a hostile payload cannot
// pad its way past the cap.
func String(s string) string {
	if s == "" {
		return s
	}

	// Fast path: nothing to scrub and short enough — skip the
	// allocation. The byte scan is cheap; a builder allocation
	// for every clean URL would dominate the audit-log cost.
	// Require valid UTF-8 here so a lone 0xff (which utf8.ValidString
	// catches but needsScrub does not) falls into the scrubbing
	// branch rather than passing through unchanged.
	if len(s) <= maxLogValueLen && !needsScrub(s) && utf8.ValidString(s) {
		return s
	}

	var b strings.Builder
	// Worst case is replacement of every byte with a 3-byte
	// U+FFFD encoding; cap by maxLogValueLen since we truncate
	// at that boundary anyway.
	if cap := maxLogValueLen; cap < len(s) {
		b.Grow(cap)
	} else {
		b.Grow(len(s))
	}

	written := 0
	i := 0
	for i < len(s) {
		// Iterate one rune at a time so U+2028 / U+2029 (3-byte
		// UTF-8 encodings) can be matched by codepoint rather
		// than by byte. Invalid UTF-8 turns into U+FFFD via the
		// rune-decode contract.
		r, size := utf8.DecodeRuneInString(s[i:])

		var enc string
		needsReplace := false
		switch {
		case r < 0x20, r == 0x7f, r == lineSep, r == paraSep:
			needsReplace = true
		case r == utf8.RuneError && size == 1:
			// Invalid UTF-8: replace.
			needsReplace = true
		}

		if needsReplace {
			enc = replacement
		} else if size == 1 {
			// Cheap path: write a single byte without allocating
			// a transient string for the rune.
			if written+1 > maxLogValueLen {
				return b.String()
			}
			b.WriteByte(s[i])
			written++
			i += size
			continue
		} else {
			enc = string(r)
		}

		if written+len(enc) > maxLogValueLen {
			return b.String()
		}
		b.WriteString(enc)
		written += len(enc)
		i += size
	}
	return b.String()
}

// needsScrub returns true if s contains any byte/rune that String
// would replace. The fast path uses this to skip allocation for
// well-formed inputs.
func needsScrub(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	// Scan for U+2028 / U+2029 only if no ASCII control char
	// fired (avoids a second pass on most inputs).
	return strings.ContainsRune(s, lineSep) || strings.ContainsRune(s, paraSep)
}
