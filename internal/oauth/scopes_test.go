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

func TestJoinScopes(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "nil slice",
			input: nil,
			want:  "",
		},
		{
			name:  "empty slice",
			input: []string{},
			want:  "",
		},
		{
			name:  "single scope",
			input: []string{"atproto"},
			want:  "atproto",
		},
		{
			name:  "multiple scopes",
			input: []string{"atproto", "transition:generic"},
			want:  "atproto transition:generic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JoinScopes(tt.input)
			if got != tt.want {
				t.Errorf("JoinScopes(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateScopeFormat(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantErr: false,
		},
		{
			name:    "valid single scope",
			input:   "atproto",
			wantErr: false,
		},
		{
			name:    "valid scope with colon",
			input:   "transition:generic",
			wantErr: false,
		},
		{
			name:    "valid scope with dots",
			input:   "app.bsky.feed.post",
			wantErr: false,
		},
		{
			name:    "valid multiple scopes",
			input:   "atproto transition:generic",
			wantErr: false,
		},
		{
			name:    "invalid - starts with dot",
			input:   ".invalid",
			wantErr: true,
		},
		{
			name:    "invalid - ends with colon",
			input:   "invalid:",
			wantErr: true,
		},
		{
			name:    "invalid - contains space in scope",
			input:   "inva lid",
			wantErr: false, // This becomes two scopes
		},
		{
			name:    "invalid - special characters",
			input:   "invalid@scope",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateScopeFormat(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateScopeFormat(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestContainsScope(t *testing.T) {
	tests := []struct {
		name        string
		scopeString string
		target      string
		want        bool
	}{
		{
			name:        "empty string",
			scopeString: "",
			target:      "atproto",
			want:        false,
		},
		{
			name:        "contains scope",
			scopeString: "atproto transition:generic",
			target:      "atproto",
			want:        true,
		},
		{
			name:        "does not contain scope",
			scopeString: "atproto transition:generic",
			target:      "admin",
			want:        false,
		},
		{
			name:        "partial match should not count",
			scopeString: "atproto transition:generic",
			target:      "transition",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsScope(tt.scopeString, tt.target)
			if got != tt.want {
				t.Errorf("ContainsScope(%q, %q) = %v, want %v", tt.scopeString, tt.target, got, tt.want)
			}
		})
	}
}

func TestIsScopeSubset(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		granted   string
		want      bool
	}{
		{
			name:      "empty requested",
			requested: "",
			granted:   "atproto",
			want:      true,
		},
		{
			name:      "exact match",
			requested: "atproto",
			granted:   "atproto",
			want:      true,
		},
		{
			name:      "subset",
			requested: "atproto",
			granted:   "atproto transition:generic",
			want:      true,
		},
		{
			name:      "not a subset",
			requested: "atproto admin",
			granted:   "atproto transition:generic",
			want:      false,
		},
		{
			name:      "superset requested",
			requested: "atproto transition:generic admin",
			granted:   "atproto transition:generic",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsScopeSubset(tt.requested, tt.granted)
			if got != tt.want {
				t.Errorf("IsScopeSubset(%q, %q) = %v, want %v", tt.requested, tt.granted, got, tt.want)
			}
		})
	}
}

func TestFilterScopes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		allowed []string
		want    string
	}{
		{
			name:    "empty input",
			input:   "",
			allowed: []string{"atproto"},
			want:    "",
		},
		{
			name:    "all allowed",
			input:   "atproto transition:generic",
			allowed: []string{"atproto", "transition:generic"},
			want:    "atproto transition:generic",
		},
		{
			name:    "some filtered",
			input:   "atproto admin transition:generic",
			allowed: []string{"atproto", "transition:generic"},
			want:    "atproto transition:generic",
		},
		{
			name:    "none allowed",
			input:   "admin secret",
			allowed: []string{"atproto"},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterScopes(tt.input, tt.allowed)
			if got != tt.want {
				t.Errorf("FilterScopes(%q, %v) = %q, want %q", tt.input, tt.allowed, got, tt.want)
			}
		})
	}
}
