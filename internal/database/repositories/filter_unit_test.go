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
	// The clause must:
	//   - Call the migration-023 IMMUTABLE wrapper function
	//     `record_contributor_identities(r.json)`, which migration
	//     024's partial GIN expression index is keyed on. A diff
	//     here silently degrades the filter back to
	//     O(collection-size) per query.
	//   - Use `@>` against an `ARRAY[$N]::text[]` literal so the
	//     GIN-on-text[] operator class picks it up.
	//
	// No outer CASE-WHEN or AND-guards: the function returns NULL
	// on non-array contributors and on over-long arrays, so the
	// indexable expression stands alone at the top level for the
	// planner to match against the partial index.
	wantSubstrings := []string{
		"record_contributor_identities(r.json) @> ARRAY[$1]::text[]",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(clause, sub) {
			t.Errorf("clause missing %q\nfull clause: %s", sub, clause)
		}
	}
	// Guard against accidental regression to the old EXISTS shape.
	if strings.Contains(clause, "THEN EXISTS") {
		t.Errorf("contributor SQL fell back to the legacy EXISTS shape:\n%s", clause)
	}
	// Guard against the inline-ARRAY shape that Postgres rejects in
	// index expressions — we tried it first and learned the hard way.
	if strings.Contains(clause, "ARRAY(SELECT") {
		t.Errorf("contributor SQL inlined the ARRAY-subquery (Postgres rejects this in indexes; use the migration-023 wrapper function):\n%s", clause)
	}
	// Outer guard must NOT wrap the indexable expression — that
	// would hide it from the planner and degrade the filter back
	// to O(collection-size).
	if strings.Contains(clause, "jsonb_typeof(r.json->'contributors')") {
		t.Errorf("filter SQL still includes an outer jsonb_typeof guard — the wrapper function handles that internally:\n%s", clause)
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
	// IN uses array-overlap (`&&`) against the same indexable
	// wrapper-function call — `&&` is GIN-supported just like `@>`
	// so the planner can still pick the migration-024 index.
	if !strings.Contains(clause, "record_contributor_identities(r.json) && $3::text[]") {
		t.Errorf("clause missing array-overlap match against the wrapper function at $3:\n%s", clause)
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
	if strings.Contains(clause, "record_contributor_identities") {
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
	// Direct equality against the migration-024 STORED generated
	// column `record.subject_did`. The previous LIKE-pattern shape
	// (`LIKE 'at://' || $1 || '/%'`) couldn't be index-served because
	// the LIKE pattern was parameter-driven; the column comparison
	// is index-served by migration-025's partial btree.
	if !strings.Contains(clause, "r.subject_did = $1") {
		t.Errorf("clause missing subject_did equality: %s", clause)
	}
	// Negative assertions: legacy JSON-extraction shapes must be gone
	// or the planner can't use the partial btree.
	if strings.Contains(clause, "r.json->'subject'") {
		t.Errorf("clause still references the per-row JSON subject extraction: %s", clause)
	}
	if strings.Contains(clause, "LIKE 'at://'") {
		t.Errorf("clause still uses the un-indexable LIKE pattern: %s", clause)
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
	// The IN case passes a single text[] param to ANY against
	// `r.subject_did`. The partial btree on subject_did serves
	// both `=` and `= ANY(...)` paths.
	if len(params) != 1 {
		t.Errorf("params length = %d, want 1", len(params))
	}
	if !strings.Contains(clause, "r.subject_did = ANY($1::text[])") {
		t.Errorf("clause missing subject_did ANY-match: %s", clause)
	}
	if strings.Contains(clause, "unnest") {
		t.Errorf("clause still uses the legacy LIKE-per-DID unnest path: %s", clause)
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

// Pin coverage of the three subject shapes the filter must match.
// The shapes themselves no longer live in the filter SQL — the
// migration-024 STORED generated column `subject_did` is what
// expands the three shapes (bare-string `at://...`, strongRef
// object with `uri`, defs#did object with `did`) into a single
// scalar DID column. The filter SQL is now a one-line equality
// against that column. So this test asserts the contract has
// moved correctly: filter SQL targets the column, not the per-row
// JSON extractions.
//
// If a producer ever writes a fourth subject shape, both the
// generated-column expression (migration 024) AND a regenerated
// /backfilled column will need updating; the filter SQL stays.
func TestBuildSingleFilter_BadgeAwardSubject_ColumnMaterializedShapes(t *testing.T) {
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
	if !strings.Contains(clause, "r.subject_did") {
		t.Errorf("filter SQL must target the migration-024 generated column: %s", clause)
	}
	// Neither the strongRef-URI extraction nor the defs#did-object
	// extraction nor the bare-string extraction is in the filter SQL
	// anymore — they all live in the generated-column expression.
	for _, banned := range []string{
		"r.json->'subject'->>'did'",
		"r.json->'subject'->>'uri'",
		"r.json->>'subject'",
	} {
		if strings.Contains(clause, banned) {
			t.Errorf("filter SQL leaked a per-row JSON extraction (%q): the generated column should own this responsibility now:\n%s", banned, clause)
		}
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
	if !strings.Contains(clause, "r.subject_did") {
		t.Errorf("marker should force the subject_did-targeted SQL regardless of FieldName: %s", clause)
	}
}
