package depth

import (
	"errors"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		maxDepth int
		wantErr  bool
	}{
		{
			name:     "trivial query under limit",
			query:    `{ a b c }`,
			maxDepth: 5,
			wantErr:  false,
		},
		{
			name:     "nested under limit",
			query:    `{ a { b { c { d { e } } } } }`,
			maxDepth: 5,
			wantErr:  false,
		},
		{
			name:     "nested exactly at limit",
			query:    `{ a { b { c { d { e } } } } }`,
			maxDepth: 5,
			wantErr:  false,
		},
		{
			name:     "nested over limit",
			query:    `{ a { b { c { d { e { f } } } } } }`,
			maxDepth: 5,
			wantErr:  true,
		},
		{
			name:     "fragment inlines into parent depth",
			query:    `query { a { ...F } } fragment F on T { b { c { d { e } } } }`,
			maxDepth: 4,
			wantErr:  true,
		},
		{
			name:     "fragment cycle does not panic",
			query:    `query { a { ...F } } fragment F on T { b { ...F } }`,
			maxDepth: 10,
			wantErr:  false,
		},
		{
			name:     "empty query",
			query:    "",
			maxDepth: 5,
			wantErr:  false,
		},
		{
			name:     "unlimited depth",
			query:    `{ a { b { c { d { e { f { g } } } } } } }`,
			maxDepth: 0,
			wantErr:  false,
		},
		{
			name:     "malformed query ignored (graphql.Do reports)",
			query:    `{ a { b`,
			maxDepth: 5,
			wantErr:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.query, tc.maxDepth)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Check(%q, %d) = nil; want error", tc.query, tc.maxDepth)
				}
				if !errors.Is(err, ErrTooDeep) {
					t.Errorf("Check error not ErrTooDeep: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Check(%q, %d) = %v; want nil", tc.query, tc.maxDepth, err)
			}
		})
	}
}
