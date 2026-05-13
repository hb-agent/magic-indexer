package did

import "testing"

func TestIsValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Happy paths.
		{"plc", "did:plc:cwqqmzquxrw6wqlpvk2h2s5x", true},
		{"web", "did:web:example.com", true},
		{"web with port-like dot path", "did:web:example.com:8443", true},
		{"web with dot-separated host", "did:web:sub.example.com", true},
		{"underscore in id", "did:plc:abc_def", true},
		{"hyphen in id", "did:plc:abc-def", true},
		{"mixed-case id", "did:plc:AbCdEf123", true},
		{"min-length boundary", "did:a:bcde", true}, // length 9

		// Length bounds.
		{"too short", "did:p:a", false},                       // length 7
		{"empty", "", false},
		{"way too long", "did:plc:" + string(make([]byte, 250)), false},

		// Whitespace.
		{"leading space", " did:plc:abc", false},
		{"trailing space", "did:plc:abc ", false},
		{"leading tab", "\tdid:plc:abc", false},
		{"trailing tab", "did:plc:abc\t", false},
		{"inner space", "did:plc:ab c", false},

		// Prefix discipline.
		{"missing did prefix", "plc:abcdefgh", false},
		{"uppercase DID prefix", "DID:plc:abc", false},
		{"uppercase method", "did:PLC:abc", false},
		{"mixed-case method", "did:Plc:abc", false},
		{"missing colon after did", "did/plc:abc", false},

		// Method portion.
		{"empty method", "did::abcdef", false},
		{"digit in method", "did:pl1:abc", false},
		{"method with hyphen", "did:p-c:abc", false},

		// Identifier portion.
		{"missing colon after method", "did:plcabcdef", false}, // no second colon -> falls through to id charset reading
		{"empty identifier", "did:plc:", false},
		{"identifier with space", "did:plc:abc def", false},
		{"identifier with percent", "did:plc:abc%20", false},
		{"identifier with slash", "did:plc:abc/def", false},
		{"identifier with at-sign", "did:plc:abc@def", false},

		// Control / dangerous chars.
		{"NUL byte", "did:plc:abc\x00def", false},
		{"newline", "did:plc:abc\ndef", false},
		{"backslash", "did:plc:abc\\def", false},
		{"single quote", "did:plc:abc'def", false},
		{"backtick", "did:plc:abc`def", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsValid(tc.in); got != tc.want {
				t.Errorf("IsValid(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Boundary check at exactly maxLen — built dynamically so the
// constant change is caught.
func TestIsValid_MaxLenBoundary(t *testing.T) {
	t.Parallel()
	prefix := "did:plc:"
	body := make([]byte, maxLen-len(prefix))
	for i := range body {
		body[i] = 'a'
	}
	s := prefix + string(body)
	if len(s) != maxLen {
		t.Fatalf("setup: built string length %d, want %d", len(s), maxLen)
	}
	if !IsValid(s) {
		t.Errorf("IsValid(maxLen string) = false, want true")
	}
	if IsValid(s + "a") {
		t.Errorf("IsValid(maxLen+1 string) = true, want false")
	}
}
