package atproto

import (
	"testing"
)

func TestParseCollections(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "single collection",
			input: "org.hypercerts.claim.activity",
			want:  []string{"org.hypercerts.claim.activity"},
		},
		{
			name:  "multiple collections",
			input: "org.hypercerts.claim.activity,org.hypercerts.claim.collection",
			want:  []string{"org.hypercerts.claim.activity", "org.hypercerts.claim.collection"},
		},
		{
			name:  "with spaces",
			input: "org.hypercerts.claim.activity, org.hypercerts.claim.collection, org.hypercerts.claim.record",
			want:  []string{"org.hypercerts.claim.activity", "org.hypercerts.claim.collection", "org.hypercerts.claim.record"},
		},
		{
			name:  "trailing comma",
			input: "org.hypercerts.claim.activity,",
			want:  []string{"org.hypercerts.claim.activity"},
		},
		{
			name:  "empty entries",
			input: "org.hypercerts.claim.activity,,org.hypercerts.claim.collection",
			want:  []string{"org.hypercerts.claim.activity", "org.hypercerts.claim.collection"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCollections(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("ParseCollections(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseCollections(%q)[%d] = %q, want %q",
						tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
