package repositories

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	clause, params, nextIdx, err := buildSingleFilter(f, 1, "r")
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
	clause, params, nextIdx, err := buildSingleFilter(f, 3, "r")
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
			_, _, _, err := buildSingleFilter(f, 1, "r")
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
	clause, params, _, err := buildSingleFilter(f, 1, "r")
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
	clause, _, _, err := buildSingleFilter(f, 1, "r")
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
	clause, params, nextIdx, err := buildSingleFilter(f, 1, "r")
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
	clause, params, nextIdx, err := buildSingleFilter(f, 1, "r")
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
		_, _, _, err := buildSingleFilter(f, 1, "r")
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
	clause, params, _, err := buildSingleFilter(f, 1, "r")
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
	clause, _, _, err := buildSingleFilter(f, 1, "r")
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
	clause, _, _, err := buildSingleFilter(f, 1, "r")
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if !strings.Contains(clause, "r.subject_did") {
		t.Errorf("marker should force the subject_did-targeted SQL regardless of FieldName: %s", clause)
	}
}

// -----------------------------------------------------------------------------
// Case-insensitive operators: eqi / ini
// -----------------------------------------------------------------------------

// TestBuildSingleFilter_Eqi_ShapeAndLowering pins the emitted SQL
// for OpEqi: both sides ASCII-folded via `lower(... COLLATE "C")`,
// parameter bound as a `string` pre-lowered with asciiToLower so
// it stays byte-identical to the Postgres side under non-ASCII inputs.
func TestBuildSingleFilter_Eqi_ShapeAndLowering(t *testing.T) {
	f := FieldFilter{
		FieldName: "type",
		Operator:  OpEqi,
		Value:     "Project",
		IsJSON:    true,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 1, "r")
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if nextIdx != 2 {
		t.Errorf("nextIdx = %d, want 2", nextIdx)
	}
	wantClause := `lower((r.json->>'type') COLLATE "C") = $1`
	if clause != wantClause {
		t.Errorf("clause mismatch\n got: %s\nwant: %s", clause, wantClause)
	}
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	s, ok := params[0].(string)
	if !ok {
		t.Fatalf("params[0] type = %T, want string", params[0])
	}
	if s != "project" {
		t.Errorf("params[0] = %q, want %q (ASCII-folded)", s, "project")
	}
}

// TestBuildSingleFilter_Ini_ShapeAndLowering pins the emitted SQL
// for OpIni: column side ASCII-folded, parameter bound as
// `[]string` (not `[]interface{}`) so the pq/pgx driver maps it
// straight to a Postgres text[].
func TestBuildSingleFilter_Ini_ShapeAndLowering(t *testing.T) {
	f := FieldFilter{
		FieldName: "type",
		Operator:  OpIni,
		Value:     []interface{}{"Project", "FAVORITES", "research"},
		IsJSON:    true,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 3, "r")
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	if nextIdx != 4 {
		t.Errorf("nextIdx = %d, want 4", nextIdx)
	}
	wantClause := `lower((r.json->>'type') COLLATE "C") = ANY($3::text[])`
	if clause != wantClause {
		t.Errorf("clause mismatch\n got: %s\nwant: %s", clause, wantClause)
	}
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	values, ok := params[0].([]string)
	if !ok {
		t.Fatalf("params[0] type = %T, want []string", params[0])
	}
	want := []string{"project", "favorites", "research"}
	if len(values) != len(want) {
		t.Fatalf("len(values) = %d, want %d", len(values), len(want))
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("values[%d] = %q, want %q (ASCII-folded)", i, values[i], want[i])
		}
	}
}

// TestBuildSingleFilter_Ini_NullElementMirrorsIn pins parity with
// OpIn's null-element branch. In practice unreachable through the
// `[String!]` GraphQL surface, but the emitter symmetry keeps the
// code shapes aligned and the behaviour predictable for direct
// FieldFilter consumers.
func TestBuildSingleFilter_Ini_NullElementMirrorsIn(t *testing.T) {
	f := FieldFilter{
		FieldName: "type",
		Operator:  OpIni,
		Value:     []interface{}{"Project", nil, "FAVORITES"},
		IsJSON:    true,
	}
	clause, _, _, err := buildSingleFilter(f, 1, "r")
	if err != nil {
		t.Fatalf("buildSingleFilter: %v", err)
	}
	wantClause := `(lower((r.json->>'type') COLLATE "C") = ANY($1::text[]) OR r.json->>'type' IS NULL)`
	if clause != wantClause {
		t.Errorf("clause mismatch\n got: %s\nwant: %s", clause, wantClause)
	}
}

// TestBuildSingleFilter_Eqi_AdversarialValue pins that user input
// never reaches the emitted SQL string — even when the value is a
// SQL-injection payload, LIKE metacharacter, NUL byte, or
// whitespace, the SQL text is the canonical shape with the
// parameter bound separately, and the parameter holds the
// byte-for-byte ASCII-folded input.
func TestBuildSingleFilter_Eqi_AdversarialValue(t *testing.T) {
	cases := []string{
		`'; DROP TABLE record; --`,
		`"`,
		`\`,
		`%`,
		`_`,
		"\x00",
		" project ",
		"\nNEWLINE\t",
		`x' OR '1'='1`,
	}
	// Canonical shape — adversarial input must NEVER affect it.
	const wantClause = `lower((r.json->>'type') COLLATE "C") = $1`
	for _, in := range cases {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			f := FieldFilter{
				FieldName: "type",
				Operator:  OpEqi,
				Value:     in,
				IsJSON:    true,
			}
			clause, params, _, err := buildSingleFilter(f, 1, "r")
			if err != nil {
				t.Fatalf("buildSingleFilter: %v", err)
			}
			if clause != wantClause {
				t.Errorf("clause shape diverged on adversarial input\n got:  %s\n want: %s", clause, wantClause)
			}
			// Per-input substring guard: a multi-character adversarial
			// input must NOT appear as a substring of the SQL string
			// (single-byte inputs like `"` overlap with our literal
			// `COLLATE "C"` and are skipped by length).
			if len(in) >= 2 && strings.Contains(clause, in) {
				t.Errorf("clause leaked user input %q:\n%s", in, clause)
			}
			s, _ := params[0].(string)
			if s != asciiToLower(in) {
				t.Errorf("param = %q, want %q (pre-lowered)", s, asciiToLower(in))
			}
		})
	}
}

// TestBuildSingleFilter_Eqi_FieldNameInjectionRejected pins that
// the field-name validator runs before SQL emission. A crafted
// field name with metacharacters must be refused, not interpolated.
func TestBuildSingleFilter_Eqi_FieldNameInjectionRejected(t *testing.T) {
	f := FieldFilter{
		FieldName: "type'); DROP TABLE record; --",
		Operator:  OpEqi,
		Value:     "project",
		IsJSON:    true,
	}
	_, _, _, err := buildSingleFilter(f, 1, "r")
	if err == nil {
		t.Fatalf("expected field-name validation error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid field name") {
		t.Errorf("error message should mention field-name validation; got: %v", err)
	}
}

// TestValidate_Eqi_RejectsNonString pins the type contract for
// OpEqi: the operator only accepts strings.
func TestValidate_Eqi_RejectsNonString(t *testing.T) {
	f := FieldFilter{FieldName: "type", Operator: OpEqi, Value: 42, IsJSON: true}
	err := f.Validate()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "eqi") || !strings.Contains(err.Error(), "string") {
		t.Errorf("error should mention eqi and string requirement; got: %v", err)
	}
}

// TestValidate_Eqi_RejectsColumnLevel pins the defense-in-depth
// guard. The GraphQL surface only routes `did` through
// DIDFilterInput (no eqi), so this case is unreachable from the
// API, but the validator still refuses it with an actionable
// message pointing the consumer to `eq`.
func TestValidate_Eqi_RejectsColumnLevel(t *testing.T) {
	f := FieldFilter{FieldName: "did", Operator: OpEqi, Value: "did:plc:abc", IsJSON: false}
	err := f.Validate()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "eqi") || !strings.Contains(err.Error(), "eq") {
		t.Errorf("error should mention eqi and the eq alternative; got: %v", err)
	}
}

// TestValidate_Ini_EmptyListRejected pins R1 item 2 — an empty
// list is a programmer error, not "match nothing". Both `ini` and
// `in` enforce this; their error messages reference MaxInListSize.
func TestValidate_Ini_EmptyListRejected(t *testing.T) {
	cases := []struct {
		op  FilterOperator
		val interface{}
	}{
		{OpIni, []interface{}{}},
		{OpIni, []string{}},
		{OpIn, []interface{}{}},
		{OpIn, []string{}},
	}
	for _, tc := range cases {
		t.Run(string(tc.op), func(t *testing.T) {
			f := FieldFilter{FieldName: "type", Operator: tc.op, Value: tc.val, IsJSON: true}
			err := f.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "1 to 50") {
				t.Errorf("error should mention unified 1 to 50 range; got: %v", err)
			}
		})
	}
}

// TestValidate_Ini_AtAndOverMaxInListSize pins the N / N+1 boundary
// shared with OpIn — mirrors the existing MaxArrayContributorScan
// boundary-pair pattern. Exercises both `[]interface{}` and
// `[]string` branches of validateInListShape so a refactor that
// splits the cap (e.g. only updates one arm) is caught.
func TestValidate_Ini_AtAndOverMaxInListSize(t *testing.T) {
	t.Run("interface_slice", func(t *testing.T) {
		atMax := make([]interface{}, MaxInListSize)
		for i := range atMax {
			atMax[i] = "v"
		}
		overMax := append(atMax, "extra") //nolint:gocritic // explicit append for the over-by-one case
		fOK := FieldFilter{FieldName: "type", Operator: OpIni, Value: atMax, IsJSON: true}
		if err := fOK.Validate(); err != nil {
			t.Errorf("at MaxInListSize (%d) should succeed; got: %v", MaxInListSize, err)
		}
		fBad := FieldFilter{FieldName: "type", Operator: OpIni, Value: overMax, IsJSON: true}
		err := fBad.Validate()
		if err == nil {
			t.Fatalf("at MaxInListSize+1 (%d) should fail", MaxInListSize+1)
		}
		if !strings.Contains(err.Error(), "1 to 50") {
			t.Errorf("error should mention unified 1 to 50 range; got: %v", err)
		}
	})

	t.Run("string_slice", func(t *testing.T) {
		atMax := make([]string, MaxInListSize)
		for i := range atMax {
			atMax[i] = "v"
		}
		overMax := append(atMax, "extra") //nolint:gocritic // explicit append for the over-by-one case
		fOK := FieldFilter{FieldName: "type", Operator: OpIni, Value: atMax, IsJSON: true}
		if err := fOK.Validate(); err != nil {
			t.Errorf("[]string at MaxInListSize (%d) should succeed; got: %v", MaxInListSize, err)
		}
		fBad := FieldFilter{FieldName: "type", Operator: OpIni, Value: overMax, IsJSON: true}
		err := fBad.Validate()
		if err == nil {
			t.Fatalf("[]string at MaxInListSize+1 (%d) should fail", MaxInListSize+1)
		}
		if !strings.Contains(err.Error(), "1 to 50") {
			t.Errorf("error should mention unified 1 to 50 range; got: %v", err)
		}
	})
}

// TestValidate_Ini_RejectsColumnLevel — defense-in-depth guard.
func TestValidate_Ini_RejectsColumnLevel(t *testing.T) {
	f := FieldFilter{FieldName: "did", Operator: OpIni, Value: []interface{}{"did:plc:a"}, IsJSON: false}
	err := f.Validate()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ini") || !strings.Contains(err.Error(), "in") {
		t.Errorf("error should mention ini and the in alternative; got: %v", err)
	}
}

// TestValidate_Ini_RejectsNonScalarElements mirrors OpIn's contract.
func TestValidate_Ini_RejectsNonScalarElements(t *testing.T) {
	cases := []interface{}{
		[]interface{}{"project", map[string]interface{}{"a": 1}},
		[]interface{}{"project", []interface{}{"nested"}},
	}
	for i, v := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			f := FieldFilter{FieldName: "type", Operator: OpIni, Value: v, IsJSON: true}
			err := f.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "non-scalar") {
				t.Errorf("error should mention non-scalar; got: %v", err)
			}
		})
	}
}

// TestAsciiToLower pins the helper's contract: fold ASCII A-Z and
// pass through every other byte unchanged. Mirrors Postgres
// `lower(... COLLATE "C")` so the case-insensitive operators stay
// symmetric between the bound parameter and the column expression.
func TestAsciiToLower(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"project", "project"},
		{"Project", "project"},
		{"PROJECT", "project"},
		{"PrOjEcT-123", "project-123"},
		{"abc-XYZ_!@#", "abc-xyz_!@#"},
		// Boundary bytes adjacent to A-Z must NOT fold. If the helper
		// switches from `>= 'A' && <= 'Z'` to `> '@' && < '['` (or
		// similar off-by-one prone forms), these catch it.
		{"@ABC[`abc{", "@abc[`abc{"},
		// All-digits / all-punctuation pass through.
		{"0123456789", "0123456789"},
		{"!#$%&'()*+,-./", "!#$%&'()*+,-./"},
		// Non-ASCII passes through unchanged. Cyrillic 'Р' (U+0420)
		// is NOT folded to Latin 'p' or Cyrillic 'р' — `COLLATE "C"`
		// only folds ASCII A-Z, and asciiToLower mirrors that.
		{"\xd0\xa0", "\xd0\xa0"},
		// Turkish capital I with dot (U+0130) — not folded. Go's
		// default strings.ToLower would expand this to two bytes;
		// asciiToLower deliberately doesn't, matching PG `COLLATE "C"`.
		{"\xc4\xb0", "\xc4\xb0"},
		// Embedded NUL passes through.
		{"AB\x00CD", "ab\x00cd"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := asciiToLower(tc.in)
			if got != tc.want {
				t.Errorf("asciiToLower(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Long input — exercises any future small-buffer fast path that
	// might branch on length. The pre-scan / mutation-loop forms
	// should both handle inputs well past any reasonable threshold.
	long := strings.Repeat("AbCdEfGhIjKlMnOpQrStUvWxYz", 10)
	wantLong := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 10)
	if got := asciiToLower(long); got != wantLong {
		t.Errorf("asciiToLower(long) mismatch:\n got:  %q\n want: %q", got, wantLong)
	}

	// All-256-byte sweep: every byte in 0x41..0x5A folds to itself+0x20;
	// every other byte passes through.
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	wantAll := make([]byte, 256)
	for i := range wantAll {
		if i >= 'A' && i <= 'Z' {
			wantAll[i] = byte(i) + ('a' - 'A')
		} else {
			wantAll[i] = byte(i)
		}
	}
	if got := asciiToLower(string(allBytes)); got != string(wantAll) {
		t.Errorf("asciiToLower all-256-bytes mismatch at:")
		for i := 0; i < 256; i++ {
			if got[i] != wantAll[i] {
				t.Errorf("  byte 0x%02x: got 0x%02x, want 0x%02x", i, got[i], wantAll[i])
			}
		}
	}
}

// TestContributorFilter_IndexExpressionMatchesMigration024 guards the
// byte-for-byte coupling between `buildContributorFilter`'s emitted
// SQL clause and the partial GIN index defined in migration 024.
//
// The index is keyed on `record_contributor_identities(json)`; the
// runtime clause uses `record_contributor_identities(r.json)` (the
// `r.` alias is the only permitted divergence). If migration 024 is
// edited to rename the function, change its argument, or wrap it in
// an outer expression, the planner can no longer match the runtime
// clause to the index and queries silently degrade from index-scan
// to O(collection-size) — the failure mode documented at filter.go's
// `indexedExpr` constant.
//
// Failure here means: either update the index expression in
// migration 024 to match the new clause shape, or update the clause
// in `buildContributorFilter` to match the new index. The two MUST
// agree.
func TestContributorFilter_IndexExpressionMatchesMigration024(t *testing.T) {
	// Locate the migration file relative to this test file. Walking
	// up from the test's working directory keeps the test portable
	// across `go test ./...` invocations from any subtree.
	migrationPath := findRepoFile(t, "internal/database/migrations/postgres/024_add_contributor_identities_gin.up.sql")

	migrationBytes, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration 024: %v", err)
	}
	migration := string(migrationBytes)

	// Extract the GIN index expression from `USING gin (<expr>)`.
	// We do paren-balanced extraction by hand because the expression
	// itself contains parens (function call) and a single-level
	// regex group cannot capture them correctly.
	indexedExprInMigration := extractGinExpression(t, migration)

	// The runtime clause we want to compare against. We synthesise
	// it by calling buildContributorFilter with a known operator and
	// then stripping the operator/parameter scaffolding so what
	// remains is the bare expression the planner has to match.
	clause, _, _, err := buildContributorFilter(FieldFilter{
		FieldName: "contributors",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindArrayContributor,
	}, 1)
	if err != nil {
		t.Fatalf("buildContributorFilter: %v", err)
	}
	// Clause shape is `(<indexedExpr> @> ARRAY[$1]::text[])` —
	// extract the bit before ` @> `.
	idx := strings.Index(clause, " @> ")
	if idx < 0 {
		t.Fatalf("clause missing ` @> ` separator: %s", clause)
	}
	indexedExprInClause := strings.TrimSpace(strings.TrimPrefix(clause, "("))[:idx-1]

	// The only permitted divergence: the runtime clause references
	// the aliased table (`r.json`), the migration references the
	// unaliased column (`json`). Normalise both to the unaliased
	// form for comparison.
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "(r.json)", "(json)")
		// Collapse whitespace so that minor formatting drift in the
		// migration file doesn't break the test.
		return strings.Join(strings.Fields(s), " ")
	}
	wantExpr := normalize(indexedExprInMigration)
	gotExpr := normalize(indexedExprInClause)

	if wantExpr != gotExpr {
		t.Fatalf("contributor filter expression drifted from migration 024 — planner will not match the partial GIN index\n  migration 024 indexes: %s\n  buildContributorFilter emits: %s (normalised to %s)\nIf this drift is intentional, update BOTH the migration's `USING gin (...)` expression AND the `indexedExpr` constant in filter.go.",
			wantExpr, indexedExprInClause, gotExpr)
	}

	// Defence-in-depth: spot-check the function name itself so a
	// rename is loudly named in the failure rather than buried in
	// the diff above.
	if !strings.Contains(wantExpr, "record_contributor_identities") {
		t.Fatalf("migration 024 no longer references record_contributor_identities — buildContributorFilter must be updated to match the new function name. Migration expression: %s", wantExpr)
	}
}

// extractGinExpression returns the expression text between the
// outermost parens following `USING gin` in the given migration
// SQL. Paren-balanced rather than regex because the indexed
// expression itself contains a function-call paren pair.
func extractGinExpression(t *testing.T, migration string) string {
	t.Helper()
	loc := regexp.MustCompile(`(?i)USING\s+gin\s*\(`).FindStringIndex(migration)
	if loc == nil {
		t.Fatalf("migration has no `USING gin (` clause:\n%s", migration)
	}
	// Scan from the opening paren, counting depth.
	start := loc[1] // index just after the `(`
	depth := 1
	for i := start; i < len(migration); i++ {
		switch migration[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(migration[start:i])
			}
		}
	}
	t.Fatalf("unterminated `USING gin (...)` in migration:\n%s", migration)
	return ""
}

// extractBtreeExpression returns the expression text inside the
// outermost parens after `ON <table>` for a btree expression
// index (one without an explicit `USING ...` clause — btree is
// the default). Postgres requires non-column expressions in
// btree indexes to be wrapped in their own parens, so the syntax
// is `ON record ((expr))` — paren-balanced extraction returns
// the outer parens' content, which is `(expr)`. The test caller
// is responsible for stripping the inner parens if needed.
//
// Sibling of extractGinExpression but anchored on `ON <table> (`
// instead of `USING gin (` so it works for index expressions
// that don't name a method.
func extractBtreeExpression(t *testing.T, migration string) string {
	t.Helper()
	loc := regexp.MustCompile(`(?i)ON\s+\w+\s*\(`).FindStringIndex(migration)
	if loc == nil {
		t.Fatalf("migration has no `ON <table> (` clause:\n%s", migration)
	}
	start := loc[1]
	depth := 1
	for i := start; i < len(migration); i++ {
		switch migration[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(migration[start:i])
			}
		}
	}
	t.Fatalf("unterminated `ON <table> (...)` in migration:\n%s", migration)
	return ""
}

// findRepoFile walks up from the current working directory until it
// finds a directory containing `go.mod`, then returns
// `<repoRoot>/<relPath>`. Fails the test if the file does not exist
// at that location.
func findRepoFile(t *testing.T, relPath string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			full := filepath.Join(dir, relPath)
			if _, err := os.Stat(full); err != nil {
				t.Fatalf("expected file at %s but stat failed: %v", full, err)
			}
			return full
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked to filesystem root without finding go.mod; cannot locate %s", relPath)
		}
		dir = parent
	}
}

// TestBuildSingleFilter_StringSubject_Eq verifies the SQL shape
// emitted for an `eq` filter on a bare-DID `subject` field (first
// user: app.certified.graph.follow). The clause must be the bare
// `r.json->>'subject' = $N` form — no outer CASE wrapper, no
// `at://`-prefix LIKE, no jsonb type guard. Anything else hides
// the indexable expression from the partial expression index in
// migration 029 and silently regresses the filter to O(collection).
func TestBuildSingleFilter_StringSubject_Eq(t *testing.T) {
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindStringSubject,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 1, "r")
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
	const wantClause = "r.json->>'subject' = $1"
	if clause != wantClause {
		t.Errorf("clause = %q, want %q", clause, wantClause)
	}
	// Guard against accidental drift to a CASE wrapper or LIKE
	// pattern — both hide the indexable expression.
	if strings.Contains(clause, "CASE") {
		t.Errorf("clause contains CASE wrapper, hiding the indexable expression: %s", clause)
	}
	if strings.Contains(clause, "LIKE") {
		t.Errorf("clause contains LIKE, defeats the equality-index path: %s", clause)
	}
}

// TestBuildSingleFilter_StringSubject_In verifies the SQL shape
// for an `in` filter on bare-DID `subject`. The clause must use
// `= ANY($N::text[])` against the same bare expression so the
// partial expression index in migration 029 can serve both
// operators.
func TestBuildSingleFilter_StringSubject_In(t *testing.T) {
	f := FieldFilter{
		FieldName: "subject",
		Operator:  OpIn,
		Value:     []string{"did:plc:alice", "did:plc:bob"},
		IsJSON:    true,
		Kind:      KindStringSubject,
	}
	clause, params, nextIdx, err := buildSingleFilter(f, 3, "r")
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
	const wantClause = "r.json->>'subject' = ANY($3::text[])"
	if clause != wantClause {
		t.Errorf("clause = %q, want %q", clause, wantClause)
	}
}

// TestBuildSingleFilter_StringSubject_UnsupportedOperator
// enforces the contract that string-subject only supports
// `eq`/`in`. Other operators must error out — there is no
// useful semantic for `gt`/`contains`/etc. on a DID column, and
// silently accepting them risks producing a clause the index
// can't serve.
func TestBuildSingleFilter_StringSubject_UnsupportedOperator(t *testing.T) {
	for _, op := range []FilterOperator{OpNeq, OpGt, OpLt, OpGte, OpLte, OpContains, OpStartsWith} {
		op := op
		t.Run(string(op), func(t *testing.T) {
			f := FieldFilter{
				FieldName: "subject",
				Operator:  op,
				Value:     "did:plc:alice",
				IsJSON:    true,
				Kind:      KindStringSubject,
			}
			_, _, _, err := buildSingleFilter(f, 1, "r")
			if err == nil {
				t.Errorf("expected error for operator %s on string-subject filter", op)
			}
		})
	}
}

// TestStringSubjectFilter_IndexExpressionMatchesMigration029
// guards the byte-for-byte coupling between
// buildStringSubjectFilter's emitted SQL and the partial
// expression index defined in migration 029.
//
// The index is keyed on `(json->>'subject')`; the runtime clause
// uses `r.json->>'subject'` (the `r.` alias is the only permitted
// divergence). If migration 029 is edited to wrap the expression
// or change the JSON path, the planner can no longer match the
// runtime clause to the index and queries silently degrade from
// index-scan to O(collection-size) — the same failure mode the
// contributor index regression test (#024) guards against.
//
// Failure here means: either update the migration's expression
// to match the new clause shape, or update the clause in
// buildStringSubjectFilter to match the new index. The two MUST
// agree.
func TestStringSubjectFilter_IndexExpressionMatchesMigration029(t *testing.T) {
	migrationPath := findRepoFile(t,
		"internal/database/migrations/postgres/029_add_follow_subject_index.up.sql")

	migrationBytes, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration 029: %v", err)
	}
	migration := string(migrationBytes)

	indexedExprInMigration := extractBtreeExpression(t, migration)
	// Postgres requires non-column btree index expressions to be
	// wrapped in their own parens — the syntax is
	// `ON record ((expr))`. extractBtreeExpression returns the
	// content of the outer parens, which is `(expr)`; strip the
	// inner pair.
	indexedExprInMigration = strings.TrimSpace(indexedExprInMigration)
	indexedExprInMigration = strings.TrimPrefix(indexedExprInMigration, "(")
	indexedExprInMigration = strings.TrimSuffix(indexedExprInMigration, ")")
	indexedExprInMigration = strings.TrimSpace(indexedExprInMigration)

	clause, _, _, err := buildStringSubjectFilter(FieldFilter{
		FieldName: "subject",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    true,
		Kind:      KindStringSubject,
	}, 1)
	if err != nil {
		t.Fatalf("buildStringSubjectFilter: %v", err)
	}
	// Clause shape is `r.json->>'subject' = $1`. Extract the bit
	// before ` = ` so we compare expressions, not operators.
	idx := strings.Index(clause, " = ")
	if idx < 0 {
		t.Fatalf("clause missing ` = ` separator: %s", clause)
	}
	indexedExprInClause := strings.TrimSpace(clause[:idx])

	// Permitted divergence: runtime references `r.json`,
	// migration references `json` (the column name without the
	// table alias).
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "r.json", "json")
		return strings.Join(strings.Fields(s), " ")
	}
	wantExpr := normalize(indexedExprInMigration)
	gotExpr := normalize(indexedExprInClause)

	if wantExpr != gotExpr {
		t.Fatalf("string-subject filter expression drifted from migration 029 — planner will not match the partial expression index\n  migration 029 indexes: %s\n  buildStringSubjectFilter emits: %s (normalised to %s)\nIf this drift is intentional, update BOTH the migration's `ON record (...)` expression AND the `indexedExpr` constant in filter.go's buildStringSubjectFilter.",
			wantExpr, indexedExprInClause, gotExpr)
	}

	// Defence-in-depth: spot-check the operator + JSON path so a
	// silent change of `->>` to `->` (returns jsonb, not text) is
	// caught by name in the failure rather than buried in the
	// diff above.
	if !strings.Contains(wantExpr, "->>") {
		t.Fatalf("migration 029 no longer uses `->>` (text extraction) — buildStringSubjectFilter must be updated to match. Migration expression: %s", wantExpr)
	}
	if !strings.Contains(wantExpr, "'subject'") {
		t.Fatalf("migration 029 no longer extracts `subject` — buildStringSubjectFilter must be updated to match. Migration expression: %s", wantExpr)
	}
}

// ---------------------------------------------------------------------------
// Issue #87 — alias plumbing + JoinedFilter EXISTS emission +
// locked-kind sentinel.
// ---------------------------------------------------------------------------

// TestBuildSingleFilter_AliasParameter_QualifiesColumnsAndJSON
// pins the contract that buildSingleFilter qualifies BOTH
// column-level references (via qualifyColumn) and JSON-path
// references (via jsonExtract) with the alias parameter. The
// "d" alias is what the joined-where EXISTS subquery passes; if
// either side regresses to a bare reference, the inner clause
// would be ambiguous against the outer `r` table when both are
// in scope.
func TestBuildSingleFilter_AliasParameter_QualifiesColumnsAndJSON(t *testing.T) {
	jsonFilter := FieldFilter{
		FieldName: "badgeType",
		Operator:  OpEq,
		Value:     "endorsement",
		IsJSON:    true,
	}
	clause, _, _, err := buildSingleFilter(jsonFilter, 1, "d")
	if err != nil {
		t.Fatalf("buildSingleFilter(json, alias=d): %v", err)
	}
	if !strings.Contains(clause, "d.json @>") {
		t.Errorf("JSON containment did not qualify with alias `d`: %s", clause)
	}
	if strings.Contains(clause, " json @>") {
		t.Errorf("JSON containment still references bare `json`: %s", clause)
	}

	columnFilter := FieldFilter{
		FieldName: "did",
		Operator:  OpEq,
		Value:     "did:plc:alice",
		IsJSON:    false,
	}
	clause, _, _, err = buildSingleFilter(columnFilter, 1, "d")
	if err != nil {
		t.Fatalf("buildSingleFilter(column, alias=d): %v", err)
	}
	if !strings.Contains(clause, "d.did =") {
		t.Errorf("column reference did not qualify with alias `d`: %s", clause)
	}
}

// TestBuildSingleFilter_LockedKindsRejectNonRSeqAlias enforces
// the R2.8 sentinel: lexicon-specific filter kinds emit
// hardcoded "r."-prefixed SQL and must not be used inside a
// joined-where subquery (alias != "r"). The error is loud so a
// future accidental registry edit gets caught at request time
// rather than silently producing SQL against the wrong table.
func TestBuildSingleFilter_LockedKindsRejectNonRSeqAlias(t *testing.T) {
	for _, kind := range []FilterKind{KindArrayContributor, KindUnionSubject, KindStringSubject} {
		kind := kind
		t.Run(fmt.Sprintf("kind=%v", kind), func(t *testing.T) {
			f := FieldFilter{
				FieldName: "x",
				Operator:  OpEq,
				Value:     "did:plc:alice",
				IsJSON:    true,
				Kind:      kind,
			}
			// Outer alias "r": accepted.
			if _, _, _, err := buildSingleFilter(f, 1, "r"); err != nil {
				t.Errorf("kind %v rejected with alias r (should be accepted): %v", kind, err)
			}
			// Inner alias "d": rejected.
			_, _, _, err := buildSingleFilter(f, 1, "d")
			if err == nil {
				t.Errorf("kind %v accepted with alias d (sentinel failed); future-proofing of locked-kind contract is broken", kind)
			}
		})
	}
}

// TestBuildFilterGroupClause_JoinedFilter_Eq pins the EXISTS
// shape for a single joined filter with one inner leaf
// (badgeType = "endorsement" on the joined badge.definition).
// This is the exact SQL the certified-app's Endorsements tab
// generates after #87 lands.
func TestBuildFilterGroupClause_JoinedFilter_Eq(t *testing.T) {
	group := FilterGroup{
		Operator: GroupAND,
		Joined: []JoinedFilter{
			{
				TargetCollection: "app.certified.badge.definition",
				JoinExpr:         "r.json->'badge'->>'uri'",
				Inner: FilterGroup{
					Operator: GroupAND,
					Filters: []FieldFilter{
						{
							FieldName: "badgeType",
							Operator:  OpEq,
							Value:     "endorsement",
							IsJSON:    true,
						},
					},
				},
			},
		},
	}
	clause, params, err := BuildFilterGroupClause(group, 1)
	if err != nil {
		t.Fatalf("BuildFilterGroupClause: %v", err)
	}
	// Inner clause is JSON containment so the parameter is a
	// jsonb literal {"badgeType":"endorsement"}, NOT the raw
	// string "endorsement" — that's the equality-as-containment
	// shape buildSingleFilter chose at the OpEq+IsJSON arm.
	// The outer EXISTS gets the collection name as its last
	// parameter.
	if len(params) != 2 {
		t.Fatalf("expected 2 params (inner + collection), got %d: %v", len(params), params)
	}
	if !strings.Contains(clause, "EXISTS (SELECT 1 FROM record d") {
		t.Errorf("clause missing the EXISTS prefix: %s", clause)
	}
	if !strings.Contains(clause, "d.collection = $2") {
		t.Errorf("clause should bind d.collection to $2 (after the inner's $1): %s", clause)
	}
	if !strings.Contains(clause, "d.uri = r.json->'badge'->>'uri'") {
		t.Errorf("clause missing the join correlation: %s", clause)
	}
	if !strings.Contains(clause, "d.json @>") {
		t.Errorf("inner clause does not qualify with alias `d`: %s", clause)
	}
	if params[1] != "app.certified.badge.definition" {
		t.Errorf("collection param mismatch: got %v want app.certified.badge.definition", params[1])
	}
}

// TestBuildFilterGroupClause_JoinedFilter_EmptyInner: existence
// check only — useful for filtering out awards whose badge
// strongRef points at a missing/deleted definition. SQL must
// not emit a dangling `AND ()` when the inner is empty.
func TestBuildFilterGroupClause_JoinedFilter_EmptyInner(t *testing.T) {
	group := FilterGroup{
		Operator: GroupAND,
		Joined: []JoinedFilter{
			{
				TargetCollection: "app.certified.badge.definition",
				JoinExpr:         "r.json->'badge'->>'uri'",
				Inner:            FilterGroup{Operator: GroupAND},
			},
		},
	}
	clause, params, err := BuildFilterGroupClause(group, 1)
	if err != nil {
		t.Fatalf("BuildFilterGroupClause: %v", err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param (collection only), got %d: %v", len(params), params)
	}
	if strings.Contains(clause, "AND ()") {
		t.Errorf("clause contains dangling `AND ()` from empty inner: %s", clause)
	}
	if !strings.HasSuffix(clause, "r.json->'badge'->>'uri')") {
		t.Errorf("clause should close with the join correlation, no tail AND: %s", clause)
	}
}

// TestBuildFilterGroupClause_JoinedFilter_InsideOr verifies the
// joined filter composes correctly inside a _or group with a
// normal scalar leaf — the issue's "intersection on subject +
// disjunction in badge" shape from the pinned description.
// Parameter numbering must be consistent across the OR
// children.
func TestBuildFilterGroupClause_JoinedFilter_InsideOr(t *testing.T) {
	group := FilterGroup{
		Operator: GroupAND,
		Children: []FilterGroup{
			{
				Operator: GroupOR,
				Filters: []FieldFilter{
					{
						FieldName: "did",
						Operator:  OpEq,
						Value:     "did:plc:me",
						IsJSON:    false,
					},
				},
				Joined: []JoinedFilter{
					{
						TargetCollection: "app.certified.badge.definition",
						JoinExpr:         "r.json->'badge'->>'uri'",
						Inner: FilterGroup{
							Operator: GroupAND,
							Filters: []FieldFilter{
								{
									FieldName: "badgeType",
									Operator:  OpEq,
									Value:     "endorsement",
									IsJSON:    true,
								},
							},
						},
					},
				},
			},
		},
	}
	clause, params, err := BuildFilterGroupClause(group, 1)
	if err != nil {
		t.Fatalf("BuildFilterGroupClause: %v", err)
	}
	// Outer leaf: $1 (did value). Inner leaf: $2 (badgeType
	// containment). Collection: $3. Three params total.
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d: %v", len(params), params)
	}
	if !strings.Contains(clause, "r.did = $1") {
		t.Errorf("clause missing the outer leaf: %s", clause)
	}
	if !strings.Contains(clause, " OR ") {
		t.Errorf("clause should join the OR children with ` OR `: %s", clause)
	}
	if !strings.Contains(clause, "d.collection = $3") {
		t.Errorf("clause should bind collection to $3 after inner $2: %s", clause)
	}
	if params[0] != "did:plc:me" {
		t.Errorf("param[0] should be the did value, got %v", params[0])
	}
	if params[2] != "app.certified.badge.definition" {
		t.Errorf("param[2] should be the joined collection, got %v", params[2])
	}
}

// TestJoinedFilter_CountConditions verifies the inner's leaf
// count rolls up to the outer's CountConditions(). Without
// this, the global MaxFilterConditions cap would be silently
// bypassed by joined filters (R1.1 in plan-review round 1).
func TestJoinedFilter_CountConditions(t *testing.T) {
	group := FilterGroup{
		Filters: []FieldFilter{
			{FieldName: "did", Operator: OpEq, Value: "x"},
		},
		Joined: []JoinedFilter{
			{
				Inner: FilterGroup{
					Filters: []FieldFilter{
						{FieldName: "badgeType", Operator: OpEq, Value: "a", IsJSON: true},
						{FieldName: "badgeType", Operator: OpEq, Value: "b", IsJSON: true},
					},
				},
			},
		},
	}
	got := group.CountConditions()
	const want = 3 // 1 outer + 2 inner
	if got != want {
		t.Errorf("CountConditions = %d, want %d (cap bypass via joined would report 1)", got, want)
	}
}

// TestBuildFilterGroupClause_JoinedFilter_CapEnforced verifies
// the global MaxFilterConditions cap actually trips when the
// total (outer + joined inner) exceeds it. Together with
// TestJoinedFilter_CountConditions this pins the R1.1 fix end-
// to-end.
func TestBuildFilterGroupClause_JoinedFilter_CapEnforced(t *testing.T) {
	// Build (MaxFilterConditions - 1) outer leaves + a joined
	// filter with 2 inner leaves → total exceeds the cap by 1.
	outerLeaves := make([]FieldFilter, MaxFilterConditions-1)
	for i := range outerLeaves {
		outerLeaves[i] = FieldFilter{
			FieldName: "did",
			Operator:  OpEq,
			Value:     "did:plc:x",
			IsJSON:    false,
		}
	}
	group := FilterGroup{
		Operator: GroupAND,
		Filters:  outerLeaves,
		Joined: []JoinedFilter{
			{
				TargetCollection: "app.certified.badge.definition",
				JoinExpr:         "r.json->'badge'->>'uri'",
				Inner: FilterGroup{
					Operator: GroupAND,
					Filters: []FieldFilter{
						{FieldName: "badgeType", Operator: OpEq, Value: "a", IsJSON: true},
						{FieldName: "badgeType", Operator: OpEq, Value: "b", IsJSON: true},
					},
				},
			},
		},
	}
	_, _, err := BuildFilterGroupClause(group, 1)
	if err == nil {
		t.Fatalf("expected error when total conditions exceed MaxFilterConditions (%d), got nil", MaxFilterConditions)
	}
	if !strings.Contains(err.Error(), "too many filter conditions") {
		t.Errorf("error message should mention the cap; got: %v", err)
	}
}
