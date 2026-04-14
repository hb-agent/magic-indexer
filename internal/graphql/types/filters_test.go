package types

import (
	"testing"

	"github.com/graphql-go/graphql"
)

// TestFilterInput_FieldsPresent verifies each typed filter input has the
// expected operator fields. Guards against accidental removal of operators
// when refactoring.
func TestFilterInput_FieldsPresent(t *testing.T) {
	tests := []struct {
		name     string
		input    *graphql.InputObject
		wantKeys []string
	}{
		{
			name:     "StringFilterInput",
			input:    StringFilterInput,
			wantKeys: []string{"eq", "neq", "in", "contains", "startsWith", "isNull"},
		},
		{
			name:     "IntFilterInput",
			input:    IntFilterInput,
			wantKeys: []string{"eq", "neq", "gt", "lt", "gte", "lte", "in", "isNull"},
		},
		{
			name:     "FloatFilterInput",
			input:    FloatFilterInput,
			wantKeys: []string{"eq", "neq", "gt", "lt", "gte", "lte", "isNull"},
		},
		{
			name:     "BooleanFilterInput",
			input:    BooleanFilterInput,
			wantKeys: []string{"eq", "isNull"},
		},
		{
			name:     "DateTimeFilterInput",
			input:    DateTimeFilterInput,
			wantKeys: []string{"eq", "neq", "gt", "lt", "gte", "lte", "isNull"},
		},
		{
			name:     "DIDFilterInput",
			input:    DIDFilterInput,
			wantKeys: []string{"eq", "in"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := tt.input.Fields()
			for _, key := range tt.wantKeys {
				if _, ok := fields[key]; !ok {
					t.Errorf("%s missing expected field %q", tt.name, key)
				}
			}
			for key := range fields {
				found := false
				for _, want := range tt.wantKeys {
					if want == key {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s has unexpected field %q", tt.name, key)
				}
			}
		})
	}
}

// TestDateTimeFilterInput_UsesDateTimeScalar verifies that datetime operators
// use DateTimeScalar (which enforces ISO-8601 parsing) instead of graphql.String.
// Regression guard for the fix under issue #53.
func TestDateTimeFilterInput_UsesDateTimeScalar(t *testing.T) {
	fields := DateTimeFilterInput.Fields()
	datetimeOps := []string{"eq", "neq", "gt", "lt", "gte", "lte"}
	for _, op := range datetimeOps {
		field, ok := fields[op]
		if !ok {
			t.Errorf("field %q missing", op)
			continue
		}
		if field.Type.Name() != "DateTime" {
			t.Errorf("field %q has type %q, expected DateTime", op, field.Type.Name())
		}
	}
}

// TestFilterInputForLexiconType_Dispatch covers every lexicon type the mapper
// handles plus unknown/unfilterable cases.
func TestFilterInputForLexiconType_Dispatch(t *testing.T) {
	tests := []struct {
		lexiconType string
		want        *graphql.InputObject
	}{
		{"string", StringFilterInput},
		{"uri", StringFilterInput},
		{"handle", StringFilterInput},
		{"did", DIDFilterInput},
		{"integer", IntFilterInput},
		{"number", FloatFilterInput},
		{"boolean", BooleanFilterInput},
		{"datetime", DateTimeFilterInput},
		{"at-uri", StringFilterInput},
		{"tid", StringFilterInput},
		{"cid", StringFilterInput},
		{"cid-link", StringFilterInput},
		// Unfilterable types.
		{"array", nil},
		{"union", nil},
		{"object", nil},
		{"blob", nil},
		{"bytes", nil},
		{"", nil},
		{"totally-unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.lexiconType, func(t *testing.T) {
			got := FilterInputForLexiconType(tt.lexiconType)
			if got != tt.want {
				gotName, wantName := "nil", "nil"
				if got != nil {
					gotName = got.Name()
				}
				if tt.want != nil {
					wantName = tt.want.Name()
				}
				t.Errorf("type %q: got %s, want %s", tt.lexiconType, gotName, wantName)
			}
		})
	}
}
