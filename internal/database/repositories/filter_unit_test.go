package repositories

import (
	"strings"
	"testing"
)

// These tests exercise the pure SQL-building path of buildSingleFilter
// for the KindArrayContributor branch. No database is required.

func TestBuildSingleFilter_Contributor_Eq(t *testing.T) {
	f := FieldFilter{
		FieldName: "contributors",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindArrayContributor,
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
		FieldName: "contributors",
		Operator:  OpIn,
		Value:     []string{"did:plc:alice", "did:plc:bob"},
		IsJSON:    true,
		Kind:      KindArrayContributor,
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
				FieldName: "contributors",
				Operator:  op,
				Value:     "did:plc:alice",
				IsJSON:    true,
				Kind:      KindArrayContributor,
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
		FieldName: "contributors",
		Operator:  OpEq,
		Value:     "did:plc:'; DROP TABLE record; --",
		IsJSON:    true,
		Kind:      KindArrayContributor,
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
// without the Kind marker, even if FieldName happens to match.
func TestBuildSingleFilter_ContributorsFieldNameWithoutMarker(t *testing.T) {
	f := FieldFilter{
		FieldName: "contributors",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		// Kind intentionally KindScalar (zero value)
	}
	clause, _, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(clause, "jsonb_array_elements") {
		t.Errorf("contributor SQL path leaked without marker flag:\n%s", clause)
	}
}

// ---------------------------------------------------------------------------
// KindUnionSubject — issue #65 subject filter on badge.award.
// Pure SQL-building tests; no database required.
// ---------------------------------------------------------------------------

func TestBuildSingleFilter_BadgeAwardSubject_Eq(t *testing.T) {
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindUnionSubject,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if nextIdx != 2 {
		t.Errorf("nextIdx = %d, want 2", nextIdx)
	}
	if len(params) != 1 || params[0] != "did:plc:alice" {
		t.Errorf("params = %v, want [\"did:plc:alice\"]", params)
	}
	// defs#did object half + strong-ref-URI half, OR-composed.
	// The bare-string branch is also present (defensive) but optional.
	if !strings.Contains(clause, "r.json->'subject'->>'did'") {
		t.Errorf("clause missing defs#did object-property extract: %s", clause)
	}
	if !strings.Contains(clause, "r.json->'subject'->>'uri'") {
		t.Errorf("clause missing strong-ref-URI extract: %s", clause)
	}
	if !strings.Contains(clause, "LIKE 'at://' || $1 || '/%'") {
		t.Errorf("clause missing at://<did>/ prefix pattern: %s", clause)
	}
	if !strings.Contains(clause, " OR ") {
		t.Errorf("clause should OR the two shapes: %s", clause)
	}
}

func TestBuildSingleFilter_BadgeAwardSubject_In(t *testing.T) {
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpIn,
		Value:     []string{"did:plc:alice", "did:plc:bob"},
		IsJSON:    true,
		Kind:      KindUnionSubject,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if nextIdx != 2 {
		t.Errorf("nextIdx = %d, want 2", nextIdx)
	}
	// The IN case passes a single text[] param used for both halves.
	if len(params) != 1 {
		t.Errorf("params length = %d, want 1", len(params))
	}
	if !strings.Contains(clause, "= ANY($1::text[])") {
		t.Errorf("clause missing ANY for string-DID half: %s", clause)
	}
	if !strings.Contains(clause, "FROM unnest($1::text[])") {
		t.Errorf("clause missing unnest for strong-ref LIKE half: %s", clause)
	}
}

func TestBuildSingleFilter_BadgeAwardSubject_UnsupportedOperator(t *testing.T) {
	for _, op := range []FilterOperator{OpNeq, OpGt, OpLt, OpGte, OpLte, OpContains, OpStartsWith} {
		f := FieldFilter{
			FieldName: "subject",
			Operator:  op,
			Value:     "did:plc:alice",
			IsJSON:    true,
			Kind:      KindUnionSubject,
		}
		_, _, _, err := buildSingleFilter(f, 1)
		if err == nil {
			t.Errorf("op %s: expected unsupported-operator error, got nil", op)
		}
	}
}

// User-supplied DID never lands literally in the SQL string — only
// as a placeholder. Mirrors the contributor-filter no-injection test.
func TestBuildSingleFilter_BadgeAwardSubject_NoUserInputInSQL(t *testing.T) {
	const malicious = "did:plc:'; DROP TABLE record; --"
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpEq,
		Value:     malicious,
		IsJSON:    true,
		Kind:      KindUnionSubject,
	}
	clause, params, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if strings.Contains(clause, "DROP TABLE") {
		t.Errorf("user input leaked into SQL clause: %s", clause)
	}
	if params[0] != malicious {
		t.Errorf("user input should land in the parameter slot verbatim, got %v", params)
	}
}

// Pin the three subject shapes the SQL must cover. Production data
// (sampled 2026-05-13) shows ALL records use one of:
//
//   - defs#did:   {"did": "did:plc:..."}              — 70% of records
//   - strongRef:  {"uri": "at://did:plc:.../...", "cid": "..."}
//
// plus a defensive bare-string branch for resilience. A future change
// that drops any of these branches must update this test deliberately.
func TestBuildSingleFilter_BadgeAwardSubject_AllThreeShapesCovered(t *testing.T) {
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindUnionSubject,
	}
	clause, _, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	// defs#did object shape: r.json->'subject'->>'did'
	if !strings.Contains(clause, "r.json->'subject'->>'did'") {
		t.Errorf("missing defs#did object shape (r.json->'subject'->>'did'): %s", clause)
	}
	// strongRef shape: r.json->'subject'->>'uri'
	if !strings.Contains(clause, "r.json->'subject'->>'uri'") {
		t.Errorf("missing strongRef shape (r.json->'subject'->>'uri'): %s", clause)
	}
	// Defensive bare-string shape: r.json->>'subject'
	if !strings.Contains(clause, "r.json->>'subject'") {
		t.Errorf("missing defensive bare-string shape (r.json->>'subject'): %s", clause)
	}
}

// FieldName != "subject" with the marker set still uses the
// badge-award branch — the marker drives behaviour, not FieldName.
// This mirrors the contributor branch's contract.
func TestBuildSingleFilter_BadgeAwardSubject_MarkerDrivesBehavior(t *testing.T) {
	f := FieldFilter{
		FieldName: "irrelevant",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindUnionSubject,
	}
	clause, _, _, err := buildSingleFilter(f, 1)
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if !strings.Contains(clause, "r.json->'subject'") {
		t.Errorf("marker should force the subject-shaped SQL regardless of FieldName: %s", clause)
	}
}
