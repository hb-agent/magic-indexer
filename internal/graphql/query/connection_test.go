package query

import (
	"fmt"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
)

func TestParseAuthorsFilter(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]interface{}
		wantNil   bool // result pointer is nil
		wantLen   int  // length of slice when non-nil
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "absent argument returns nil (no filter)",
			args:    map[string]interface{}{},
			wantNil: true,
		},
		{
			name:    "explicit null returns nil (no filter)",
			args:    map[string]interface{}{"authors": nil},
			wantNil: true,
		},
		{
			name:    "empty list returns non-nil empty slice (match nothing)",
			args:    map[string]interface{}{"authors": []interface{}{}},
			wantNil: false,
			wantLen: 0,
		},
		{
			name:    "single DID returns slice of one",
			args:    map[string]interface{}{"authors": []interface{}{"did:plc:abc"}},
			wantNil: false,
			wantLen: 1,
		},
		{
			name: "multiple DIDs returns slice of correct length",
			args: map[string]interface{}{"authors": []interface{}{
				"did:plc:a", "did:plc:b", "did:plc:c",
			}},
			wantNil: false,
			wantLen: 3,
		},
		{
			name:      "non-string element returns error",
			args:      map[string]interface{}{"authors": []interface{}{"did:plc:a", 42}},
			wantErr:   true,
			errSubstr: "must be strings",
		},
		{
			name:      "non-list type returns error",
			args:      map[string]interface{}{"authors": "did:plc:a"},
			wantErr:   true,
			errSubstr: "must be a list",
		},
		{
			name: "at cap succeeds",
			args: func() map[string]interface{} {
				list := make([]interface{}, repositories.MaxAuthorsFilterSize)
				for i := range list {
					list[i] = fmt.Sprintf("did:plc:%d", i)
				}
				return map[string]interface{}{"authors": list}
			}(),
			wantNil: false,
			wantLen: repositories.MaxAuthorsFilterSize,
		},
		{
			name: "exceeds cap returns error",
			args: func() map[string]interface{} {
				list := make([]interface{}, repositories.MaxAuthorsFilterSize+1)
				for i := range list {
					list[i] = fmt.Sprintf("did:plc:%d", i)
				}
				return map[string]interface{}{"authors": list}
			}(),
			wantErr:   true,
			errSubstr: "exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseAuthorsFilter(tt.args)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errSubstr != "" && !contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if result != nil {
					t.Fatalf("expected nil result, got %v", *result)
				}
				return
			}

			if result == nil {
				t.Fatalf("expected non-nil result, got nil")
			}
			if len(*result) != tt.wantLen {
				t.Fatalf("expected len=%d, got len=%d", tt.wantLen, len(*result))
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
