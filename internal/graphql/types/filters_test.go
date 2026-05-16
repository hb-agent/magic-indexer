package types //nolint:revive // package name is descriptive within graphql context

import (
	"strings"
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
			wantKeys: []string{"eq", "eqi", "neq", "in", "ini", "contains", "startsWith", "isNull"},
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

// TestStringFilterInput_CaseInsensitiveVariants pins the eqi/ini
// operator shape: present, typed as String / [String!], descriptions
// include the case-insensitive contract and the ASCII-fold caveat.
func TestStringFilterInput_CaseInsensitiveVariants(t *testing.T) {
	fields := StringFilterInput.Fields()

	eqi, ok := fields["eqi"]
	if !ok {
		t.Fatalf("eqi field missing from StringFilterInput")
	}
	if eqi.Type.Name() != "String" {
		t.Errorf("eqi type = %s, want String", eqi.Type.Name())
	}
	for _, want := range []string{"case-insensitive", "ASCII fold", "COLLATE \"C\""} {
		if !strings.Contains(eqi.Description(), want) {
			t.Errorf("eqi description missing %q; got: %s", want, eqi.Description())
		}
	}

	ini, ok := fields["ini"]
	if !ok {
		t.Fatalf("ini field missing from StringFilterInput")
	}
	// `[String!]` — a List of NonNull String.
	if _, isList := ini.Type.(*graphql.List); !isList {
		t.Errorf("ini type = %T, want graphql.List", ini.Type)
	}
	for _, want := range []string{"case-insensitive", "1-50", "empty list is rejected"} {
		if !strings.Contains(ini.Description(), want) {
			t.Errorf("ini description missing %q; got: %s", want, ini.Description())
		}
	}

	// `eq` and `in` are still the case-sensitive operators with
	// updated descriptions noting that explicitly.
	if !strings.Contains(fields["eq"].Description(), "case-sensitive") {
		t.Errorf("eq description should call out case-sensitivity; got: %s", fields["eq"].Description())
	}
	if !strings.Contains(fields["in"].Description(), "case-sensitive") {
		t.Errorf("in description should call out case-sensitivity; got: %s", fields["in"].Description())
	}

	// `-i` suffix convention is pinned in the type-level description
	// so introspection makes the contract self-documenting.
	if !strings.Contains(StringFilterInput.Description(), "-i") {
		t.Errorf("StringFilterInput description should document the -i suffix convention; got: %s", StringFilterInput.Description())
	}
}

// TestDIDFilterInput_NoCaseInsensitiveOperators pins the contract
// that DIDs are spec-case-sensitive and DIDFilterInput exposes neither
// eqi nor ini — DID case folding would be a spec violation.
func TestDIDFilterInput_NoCaseInsensitiveOperators(t *testing.T) {
	fields := DIDFilterInput.Fields()
	for _, op := range []string{"eqi", "ini"} {
		if _, leaked := fields[op]; leaked {
			t.Errorf("DIDFilterInput leaked case-insensitive operator %q; DIDs must remain spec-case-sensitive", op)
		}
	}
	if !strings.Contains(DIDFilterInput.Description(), "case-sensitive") {
		t.Errorf("DIDFilterInput description should pin spec-case-sensitivity; got: %s", DIDFilterInput.Description())
	}
}
