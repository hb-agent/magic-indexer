package repositories

import (
	"strings"
	"testing"
)

// These tests exercise the pure SQL-building path of buildSingleFilter
// for the IsArrayContributor branch. No database is required.

func TestBuildSingleFilter_Contributor_Eq(t *testing.T) {
	f := FieldFilter{
		FieldName:          "contributors",
		Operator:           OpEq,
		Value:              "did:plc:alice",
		IsJSON:             true,
		IsArrayContributor: true,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nextIdx != 2 {
		t.Errorf("nextIdx = %d, want 2", nextIdx)
	}
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	if params[0] != "did:plc:alice" {
		t.Errorf("params[0] = %v, want did:plc:alice", params[0])
	}
	// Guards must be present, and the whole shape must be a CASE WHEN
	// wrapper so Postgres is forced to evaluate the guards before the
	// EXISTS subquery (AND ordering in WHERE is otherwise not
	// guaranteed by the planner). The per-element candidate also uses
	// CASE to disambiguate the contributor-identity union by JSON
	// type — `->>` on a JSON object returns the object's text
	// serialisation, not NULL, so the previous COALESCE approach
	// silently matched zero rows for the object variant.
	wantSubstrings := []string{
		"CASE WHEN",
		"jsonb_typeof(r.json->'contributors') = 'array'",
		"jsonb_array_length(r.json->'contributors') <= 200",
		"THEN EXISTS",
		"jsonb_array_elements(r.json->'contributors')",
		`CASE jsonb_typeof(c->'contributorIdentity')`,
		`WHEN 'string' THEN c->>'contributorIdentity'`,
		`WHEN 'object' THEN c->'contributorIdentity'->>'identity'`,
		"= $1",
		"ELSE FALSE END",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(clause, sub) {
			t.Errorf("clause missing %q\nfull clause: %s", sub, clause)
		}
	}
}

func TestBuildSingleFilter_Contributor_In(t *testing.T) {
	f := FieldFilter{
		FieldName:          "contributors",
		Operator:           OpIn,
		Value:              []string{"did:plc:alice", "did:plc:bob"},
		IsJSON:             true,
		IsArrayContributor: true,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nextIdx != 4 {
		t.Errorf("nextIdx = %d, want 4", nextIdx)
	}
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	values, ok := params[0].([]string)
	if !ok {
		t.Fatalf("params[0] type = %T, want []string", params[0])
	}
	if len(values) != 2 || values[0] != "did:plc:alice" || values[1] != "did:plc:bob" {
		t.Errorf("values = %v, want [did:plc:alice did:plc:bob]", values)
	}
	if !strings.Contains(clause, "= ANY($3::text[])") {
		t.Errorf("clause missing parameterised text[] cast at $3:\n%s", clause)
	}
}

func TestBuildSingleFilter_Contributor_UnsupportedOperator(t *testing.T) {
	for _, op := range []FilterOperator{OpNeq, OpGt, OpLt, OpGte, OpLte, OpContains, OpStartsWith} {
		op := op
		t.Run(string(op), func(t *testing.T) {
			f := FieldFilter{
				FieldName:          "contributors",
				Operator:           op,
				Value:              "did:plc:alice",
				IsJSON:             true,
				IsArrayContributor: true,
			}
			_, _, _, err := buildSingleFilter(f, 1)
			if err == nil {
				t.Errorf("expected error for operator %s on contributor filter", op)
			}
		})
	}
}

// Confirms the SQL fragment includes no user-controllable identifier
// in the JSON path — the only user input is bound via $N.
func TestBuildSingleFilter_Contributor_NoUserInputInSQL(t *testing.T) {
	f := FieldFilter{
		FieldName:          "contributors",
		Operator:           OpEq,
		Value:              "did:plc:'; DROP TABLE record; --",
		IsJSON:             true,
		IsArrayContributor: true,
	}
	clause, params, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(clause, "DROP TABLE") {
		t.Errorf("malicious value leaked into SQL clause:\n%s", clause)
	}
	if params[0] != "did:plc:'; DROP TABLE record; --" {
		t.Errorf("malicious value not preserved as parameter: %v", params[0])
	}
}

// Sanity: the contributor branch is never reached for filters
// without the marker, even if FieldName happens to match.
func TestBuildSingleFilter_ContributorsFieldNameWithoutMarker(t *testing.T) {
	f := FieldFilter{
		FieldName: "contributors",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		// IsArrayContributor intentionally false
	}
	clause, _, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(clause, "jsonb_array_elements") {
		t.Errorf("contributor SQL path leaked without marker flag:\n%s", clause)
	}
}
