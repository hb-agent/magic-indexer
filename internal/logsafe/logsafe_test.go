package logsafe

import (
	"strings"
	"testing"
)

func TestDID_AcceptsValidDID(t *testing.T) {
	// did:plc:... is the canonical shape the rest of the
	// codebase uses; the validator in internal/atproto/did is
	// the source of truth.
	const valid = "did:plc:abcdefghijklmnopqrstuvwx"
	if got := DID(valid); got != valid {
		t.Errorf("DID(valid) = %q, want %q (pass-through)", got, valid)
	}
}

func TestDID_RejectsInvalidShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no_prefix", "plc:abc"},
		{"bare_word", "alice.bsky.social"},
		{"newline_injection", "did:plc:abc\nfake_attr=evil"},
		{"crlf_injection", "did:plc:abc\r\nevil"},
		{"ansi_escape", "did:plc:abc\x1b[31mred"},
		{"nbsp_only", " "},
		{"unicode_line_sep", "did:plc:abc "},
		{"unicode_para_sep", "did:plc:abc "},
		{"unicode_lookalike", "did:plc:аbc"}, // Cyrillic 'а' — fails IsValid's strict ASCII check.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DID(c.in)
			if got != invalidDIDSentinel {
				t.Errorf("DID(%q) = %q, want sentinel %q (hostile input must not pass through)",
					c.in, got, invalidDIDSentinel)
			}
		})
	}
}

func TestString_PassesThroughCleanInput(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"https://example.com/path?q=1",
		"did:plc:abc", // happy path
		"unicode-printable: αβγ δεζ — ✓", // multi-byte UTF-8 passes
	}
	for _, c := range cases {
		if got := String(c); got != c {
			t.Errorf("String(%q) = %q, want unchanged", c, got)
		}
	}
}

func TestString_ScrubsControlCharacters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // exact expected scrubbed value
	}{
		{"newline", "a\nb", "a�b"},
		{"crlf", "a\r\nb", "a��b"},
		{"tab", "a\tb", "a�b"},
		{"null_byte", "a\x00b", "a�b"},
		{"ansi_escape", "a\x1b[31mred", "a�[31mred"},
		{"bel", "a\x07b", "a�b"},
		{"del", "a\x7fb", "a�b"},
		{"unicode_line_sep", "a b", "a�b"},
		{"unicode_para_sep", "a b", "a�b"},
		{"vertical_tab", "a\x0bb", "a�b"},
		{"form_feed", "a\x0cb", "a�b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := String(c.in)
			if got != c.want {
				t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
			}
			// Belt-and-braces: the output must never re-introduce
			// a literal newline / CR. Downstream log shippers split
			// on \n.
			if strings.ContainsAny(got, "\n\r\t") {
				t.Errorf("String(%q) leaked control char: %q", c.in, got)
			}
		})
	}
}

func TestString_AllowsNonBreakingSpace(t *testing.T) {
	// NBSP (U+00A0) is not a line separator and not a control
	// char by our policy — it passes through. This is deliberate;
	// some legitimate operator strings use it as a thousands
	// separator. If a future audit wants to scrub it, this test
	// is the canary.
	const nbsp = "a b"
	if got := String(nbsp); got != nbsp {
		t.Errorf("String(NBSP) = %q, want pass-through %q", got, nbsp)
	}
}

func TestString_TruncatesAtMaxLogValueLen(t *testing.T) {
	// Clean input longer than the cap: result must be no longer.
	long := strings.Repeat("a", maxLogValueLen+50)
	got := String(long)
	if len(got) > maxLogValueLen {
		t.Errorf("String(len=%d) returned len=%d, want <= %d",
			len(long), len(got), maxLogValueLen)
	}
	if got != strings.Repeat("a", maxLogValueLen) {
		t.Errorf("String(long-clean) = %q, want exactly maxLogValueLen 'a's", got)
	}
}

func TestString_TruncationAfterReplacement(t *testing.T) {
	// A hostile payload of all-newlines should not pad past the
	// cap. Each newline becomes a 3-byte U+FFFD so the byte cap
	// is hit faster than the rune cap.
	in := strings.Repeat("\n", maxLogValueLen)
	got := String(in)
	if len(got) > maxLogValueLen {
		t.Errorf("String(all-newlines) returned len=%d, want <= %d", len(got), maxLogValueLen)
	}
	for _, b := range []byte(got) {
		if b == '\n' {
			t.Errorf("String(all-newlines) leaked a literal newline byte")
			break
		}
	}
}

func TestString_HandlesInvalidUTF8(t *testing.T) {
	// Standalone 0xff is invalid UTF-8 (continuation byte without
	// a lead). decodeRune returns RuneError + size 1; we replace.
	in := "before\xffafter"
	got := String(in)
	if !strings.HasPrefix(got, "before�") || !strings.HasSuffix(got, "after") {
		t.Errorf("String(invalid-utf8) = %q, want before<repl>after pattern", got)
	}
	for _, b := range []byte(got) {
		if b == 0xff {
			t.Errorf("String(invalid-utf8) leaked 0xff byte")
			break
		}
	}
}

func TestString_AuditLogInjectionShapes(t *testing.T) {
	// Real-world adversary shapes lifted from the Q-6 audit
	// recommendation. None must yield a string that contains
	// either a literal newline or a Unicode line separator.
	cases := []string{
		"https://evil\n event=admin_added target_did=did:plc:attacker",
		"https://evil\r\n",
		"https://evil ",
		"https://evil ",
		"https://evil\x1b]8;;https://attacker.com\x07phishing\x1b]8;;\x07",
	}
	for _, c := range cases {
		got := String(c)
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("String(%q) leaked CR/LF: %q", c, got)
		}
		if strings.ContainsRune(got, ' ') || strings.ContainsRune(got, ' ') {
			t.Errorf("String(%q) leaked Unicode line-sep: %q", c, got)
		}
	}
}
