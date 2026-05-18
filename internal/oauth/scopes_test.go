package oauth

import (
	"reflect"
	"testing"
)

func TestParseScopes(t *testing.T) {
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
			name:  "single scope",
			input: "atproto",
			want:  []string{"atproto"},
		},
		{
			name:  "multiple scopes",
			input: "atproto transition:generic",
			want:  []string{"atproto", "transition:generic"},
		},
		{
			name:  "extra whitespace",
			input: "  atproto   transition:generic  ",
			want:  []string{"atproto", "transition:generic"},
		},
		{
			name:  "complex scopes",
			input: "atproto transition:chat.bsky app.bsky.feed.post",
			want:  []string{"atproto", "transition:chat.bsky", "app.bsky.feed.post"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseScopes(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseScopes(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
