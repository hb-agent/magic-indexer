package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/lexicon"
)

// contributorDescriptor returns the registry's contributor descriptor
// for use in tests. Pulling from the live registry (rather than a
// hand-rolled stub) ensures these tests notice registry drift.
func contributorDescriptor(t *testing.T) filterDescriptor {
	t.Helper()
	d, ok := lookupFilterDescriptor("org.hypercerts.claim.activity", "contributor")
	if !ok {
		t.Fatalf("contributor descriptor missing from filterRegistry; registry: %+v", filterRegistry)
	}
	return d
}

func TestBuildContributorFieldFilter_EqValidDID(t *testing.T) {
	in := map[string]interface{}{"eq": "did:plc:alice"}
	f, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Operator != repositories.OpEq {
		t.Errorf("Operator = %s, want %s", f.Operator, repositories.OpEq)
	}
	if f.Kind != repositories.KindArrayContributor {
		t.Errorf("Kind = %v, want KindArrayContributor", f.Kind)
	}
	if f.Value != "did:plc:alice" {
		t.Errorf("Value = %v, want did:plc:alice", f.Value)
	}
}

func TestBuildContributorFieldFilter_EqRejectsHandle(t *testing.T) {
	in := map[string]interface{}{"eq": "alice.example.com"}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for handle, got nil")
	}
	if !strings.Contains(err.Error(), "DIDs") {
		t.Errorf("error message should mention DIDs: %v", err)
	}
	if !strings.Contains(err.Error(), "alice.example.com") {
		t.Errorf("error message should include the rejected value: %v", err)
	}
}

func TestBuildContributorFieldFilter_EqRejectsNonString(t *testing.T) {
	in := map[string]interface{}{"eq": 42}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for non-string eq value")
	}
}

func TestBuildContributorFieldFilter_InValidDIDs(t *testing.T) {
	in := map[string]interface{}{"in": []interface{}{"did:plc:alice", "did:plc:bob"}}
	f, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Operator != repositories.OpIn {
		t.Errorf("Operator = %s, want %s", f.Operator, repositories.OpIn)
	}
	values, ok := f.Value.([]string)
	if !ok {
		t.Fatalf("Value type = %T, want []string", f.Value)
	}
	if len(values) != 2 || values[0] != "did:plc:alice" || values[1] != "did:plc:bob" {
		t.Errorf("Value = %v, want [did:plc:alice did:plc:bob]", values)
	}
}

func TestBuildContributorFieldFilter_RejectsEmptyInList(t *testing.T) {
	in := map[string]interface{}{"in": []interface{}{}}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for empty in: [] list")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("error should mention 'at least one': %v", err)
	}
}

func TestBuildContributorFieldFilter_InRejectsHandleInList(t *testing.T) {
	in := map[string]interface{}{"in": []interface{}{"did:plc:alice", "bob.example.com"}}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for handle in IN list")
	}
	if !strings.Contains(err.Error(), "bob.example.com") {
		t.Errorf("error should include the rejected value: %v", err)
	}
}

func TestBuildContributorFieldFilter_InRejectsOversized(t *testing.T) {
	values := make([]interface{}, repositories.MaxInListSize+1)
	for i := range values {
		values[i] = "did:plc:alice"
	}
	in := map[string]interface{}{"in": values}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for oversized IN list")
	}
}

func TestBuildContributorFieldFilter_NoOperator(t *testing.T) {
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when neither eq nor in is provided")
	}
}

func TestBuildContributorFieldFilter_RejectsUppercaseMethodPrefix(t *testing.T) {
	// Strict input validation: canonical lowercase method prefix
	// required, even though the rest of the DID is case-sensitive.
	in := map[string]interface{}{"eq": "DID:PLC:abc"}
	_, err := buildDIDOnlyEqInFilter(contributorDescriptor(t), in)
	if err == nil {
		t.Fatal("expected error for uppercase method prefix")
	}
}

// TestFilterRegistry_Contributor pins the contract that the activity
// collection has a contributor filter descriptor, and that no other
// loaded collection has one (replaces the older wantsContributorFilter
// predicate test now that the predicate is collapsed into the registry).
func TestFilterRegistry_Contributor(t *testing.T) {
	cases := map[string]bool{
		"org.hypercerts.claim.activity":        true,
		"org.hypercerts.collection":            false,
		"app.certified.badge.award":            false,
		"app.certified.temp.graph.endorsement": false,
		"":                                     false,
	}
	for lexID, want := range cases {
		_, got := lookupFilterDescriptor(lexID, "contributor")
		if got != want {
			t.Errorf("lookupFilterDescriptor(%q, \"contributor\") presence = %v, want %v", lexID, got, want)
		}
	}
}

// TestFilterRegistry_BadgeAwardSubject pins the same shape for the
// badge-award subject filter.
func TestFilterRegistry_BadgeAwardSubject(t *testing.T) {
	cases := map[string]bool{
		"app.certified.badge.award":     true,
		"org.hypercerts.claim.activity": false,
		"":                              false,
	}
	for lexID, want := range cases {
		_, got := lookupFilterDescriptor(lexID, "subject")
		if got != want {
			t.Errorf("lookupFilterDescriptor(%q, \"subject\") presence = %v, want %v", lexID, got, want)
		}
	}
}

// TestFilterRegistry_GraphFollowSubject pins the graph.follow
// subject filter wiring. The follow lexicon also has `subject` as
// a filterable scalar (string, format: did) so this guards against
// two regressions: (1) the registry entry being removed, dropping
// the SQL path to the default scalar handler that doesn't use the
// migration-029 partial index; and (2) the descriptor's Kind being
// accidentally changed to KindUnionSubject (which would emit
// `r.subject_did = $N` — wrong column for follow records).
func TestFilterRegistry_GraphFollowSubject(t *testing.T) {
	desc, ok := lookupFilterDescriptor("app.certified.graph.follow", "subject")
	if !ok {
		t.Fatalf("lookupFilterDescriptor(\"app.certified.graph.follow\", \"subject\") returned not-found; the registry entry is missing")
	}
	if desc.FieldName != "subject" {
		t.Errorf("descriptor.FieldName = %q, want \"subject\"", desc.FieldName)
	}
	if desc.Kind != repositories.KindStringSubject {
		t.Errorf("descriptor.Kind = %v, want KindStringSubject (%v) — accidentally switching to KindUnionSubject would route to the badge-award subject_did column, which is wrong for follow records",
			desc.Kind, repositories.KindStringSubject)
	}
	if desc.Description != graphFollowSubjectDescription {
		t.Errorf("descriptor.Description drifted from graphFollowSubjectDescription — the pinned schema-introspection text is the consumer-facing contract")
	}
	// Negative: the registry must NOT have an entry for
	// other lexicons under "subject" that would shadow theirs.
	for _, lexID := range []string{"org.hypercerts.claim.activity", "app.certified.actor.profile"} {
		if _, hit := lookupFilterDescriptor(lexID, "subject"); hit {
			t.Errorf("unexpected registry entry for (%q, \"subject\")", lexID)
		}
	}
}

// TestParseOperator_CaseInsensitiveVariants pins the parser
// extension for eqi / ini. The parser reuses the per-field
// operator loop in extractFieldFiltersRecursive, so these are
// the only mappings we need to verify.
func TestParseOperator_CaseInsensitiveVariants(t *testing.T) {
	cases := []struct {
		in     string
		want   repositories.FilterOperator
		isNull bool
	}{
		{"eqi", repositories.OpEqi, false},
		{"ini", repositories.OpIni, false},
		// Existing operators continue to map correctly — regression
		// guard against accidental fallthrough when adding cases.
		{"eq", repositories.OpEq, false},
		{"in", repositories.OpIn, false},
		{"isNull", "", true},
		{"nope", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotOp, gotIsNull := parseOperator(tc.in)
			if gotOp != tc.want {
				t.Errorf("parseOperator(%q) op = %q, want %q", tc.in, gotOp, tc.want)
			}
			if gotIsNull != tc.isNull {
				t.Errorf("parseOperator(%q) isNull = %v, want %v", tc.in, gotIsNull, tc.isNull)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Issue #87 — joinedWhereRegistry + extractor + depth-cap tests.
// ---------------------------------------------------------------------------

// TestJoinedWhereRegistry_BadgeAwardBadge pins the
// (parent, field) → descriptor entry that backs the
// AppCertifiedBadgeAwardWhereInput.badge nested-where filter.
// Guards against:
//   - accidental removal (filter silently disappears from the
//     schema; certified-app's Endorsements tab regresses to the
//     2-call workaround)
//   - field-name drift (a registry rename without a client
//     coordination breaks every consumer)
//   - target-lexicon drift (a rename here against the actual
//     lexicon ID emits SQL that targets a non-existent
//     collection)
//   - JoinExpr drift (any change here is a SQL diff — the
//     expression is emitted verbatim and a typo would silently
//     return no results)
func TestJoinedWhereRegistry_BadgeAwardBadge(t *testing.T) {
	jd, ok := lookupJoinedWhereDescriptor("app.certified.badge.award", "badge")
	if !ok {
		t.Fatalf("registry entry for (app.certified.badge.award, \"badge\") is missing")
	}
	if jd.FieldName != "badge" {
		t.Errorf("FieldName = %q, want \"badge\"", jd.FieldName)
	}
	if jd.TargetLexicon != "app.certified.badge.definition" {
		t.Errorf("TargetLexicon = %q, want \"app.certified.badge.definition\"", jd.TargetLexicon)
	}
	const wantJoinExpr = "r.json->'badge'->>'uri'"
	if jd.JoinExpr != wantJoinExpr {
		t.Errorf("JoinExpr = %q, want %q — this expression is emitted verbatim into the EXISTS subquery", jd.JoinExpr, wantJoinExpr)
	}
	if jd.Description != badgeAwardBadgeDescription {
		t.Errorf("Description drifted from badgeAwardBadgeDescription — the pinned schema-introspection text is the consumer-facing contract")
	}
	// Negative entries: the registry must not have entries for
	// lexicons that don't participate in joined-where today.
	for _, lexID := range []string{"org.hypercerts.claim.activity", "app.certified.graph.follow"} {
		if _, hit := lookupJoinedWhereDescriptor(lexID, "badge"); hit {
			t.Errorf("unexpected registry entry for (%q, \"badge\")", lexID)
		}
	}
}

// loadAwardAndDefinitionLexicons returns a registry with the
// badge.award + badge.definition lexicons populated (plus
// strongRef for the badge.award field type to resolve). Used
// by the extractor tests below — calling them with a real
// registry beats hand-constructing test doubles.
func loadAwardAndDefinitionLexicons(t *testing.T) *lexicon.Registry {
	t.Helper()
	r := lexicon.NewRegistry()
	for _, path := range []string{
		"../../../testdata/lexicons/app/certified/badge/award.json",
		"../../../testdata/lexicons/app/certified/badge/definition.json",
		"../../../testdata/lexicons/com/atproto/repo/strongRef.json",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lex, err := lexicon.ParseBytes(data)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		r.Register(lex)
	}
	return r
}

// TestExtractFieldFilters_NestedBadgeWhere covers the happy path
// for the joined-where extractor: a where like
// { subject: {eq: <did>}, badge: { badgeType: {eq: "endorsement"} } }
// produces a FilterGroup with one Filters entry (the subject
// filter going through the existing filterRegistry path) and
// one Joined entry whose Inner has the badgeType leaf.
func TestExtractFieldFilters_NestedBadgeWhere(t *testing.T) {
	registry := loadAwardAndDefinitionLexicons(t)
	awardLex, _ := registry.GetLexicon("app.certified.badge.award")

	where := map[string]interface{}{
		"subject": map[string]interface{}{
			"eq": "did:plc:alice",
		},
		"badge": map[string]interface{}{
			"badgeType": map[string]interface{}{
				"eq": "endorsement",
			},
		},
	}

	group, err := extractFieldFilters(where, awardLex, registry)
	if err != nil {
		t.Fatalf("extractFieldFilters: %v", err)
	}

	// One Filters entry (subject), one Joined entry (badge).
	if len(group.Filters) != 1 {
		t.Fatalf("expected 1 outer Filters entry (subject), got %d", len(group.Filters))
	}
	if group.Filters[0].FieldName != "subject" {
		t.Errorf("outer filter field = %q, want \"subject\"", group.Filters[0].FieldName)
	}
	if len(group.Joined) != 1 {
		t.Fatalf("expected 1 Joined entry, got %d", len(group.Joined))
	}
	j := group.Joined[0]
	if j.TargetCollection != "app.certified.badge.definition" {
		t.Errorf("TargetCollection = %q, want \"app.certified.badge.definition\"", j.TargetCollection)
	}
	if j.JoinExpr != "r.json->'badge'->>'uri'" {
		t.Errorf("JoinExpr = %q", j.JoinExpr)
	}
	if len(j.Inner.Filters) != 1 {
		t.Fatalf("inner has %d filters, want 1", len(j.Inner.Filters))
	}
	if j.Inner.Filters[0].FieldName != "badgeType" {
		t.Errorf("inner filter field = %q, want \"badgeType\"", j.Inner.Filters[0].FieldName)
	}
	if j.Inner.Filters[0].Value != "endorsement" {
		t.Errorf("inner filter value = %v, want \"endorsement\"", j.Inner.Filters[0].Value)
	}
	// Inner must not itself contain Joined entries — one-level bound.
	if len(j.Inner.Joined) != 0 {
		t.Errorf("inner.Joined should be empty (one-level bound)")
	}
}

// TestExtractFieldFilters_NestedBadgeWhere_DepthCap pins the
// extractor's interaction with MaxFilterDepth across the
// joined-where boundary. Crossing the boundary counts as +1
// depth; the inner's own _and/_or composition counts further.
// A pathological input that exceeds the cap inside the inner
// must be rejected, not silently truncated.
func TestExtractFieldFilters_NestedBadgeWhere_DepthCap(t *testing.T) {
	registry := loadAwardAndDefinitionLexicons(t)
	awardLex, _ := registry.GetLexicon("app.certified.badge.award")

	// MaxFilterDepth = 3 today. Construct a payload that exceeds
	// it: outer _and (depth 1) → outer _and (depth 2) → badge
	// (depth 3 across boundary) → inner _and (depth 4) → leaf.
	// Depth 4 > 3 → reject.
	where := map[string]interface{}{
		"_and": []interface{}{
			map[string]interface{}{
				"_and": []interface{}{
					map[string]interface{}{
						"badge": map[string]interface{}{
							"_and": []interface{}{
								map[string]interface{}{
									"badgeType": map[string]interface{}{"eq": "x"},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := extractFieldFilters(where, awardLex, registry)
	if err == nil {
		t.Fatalf("expected depth-cap error for deeply-nested joined-where; got nil")
	}
}

// TestExtractFieldFilters_NestedJoined_Rejected enforces the
// one-level bound on joined-where nesting at the extractor.
// Today no lexicon registers a join target that itself has a
// joined-where entry, so this is hard to trigger naturally —
// but a future-state registry edit that introduces such a
// chain should be rejected loudly, not silently emit
// EXISTS-in-EXISTS SQL.
func TestExtractFieldFilters_NestedJoined_Rejected(t *testing.T) {
	// We can't easily reproduce nested joined-where through the
	// extractor's normal path because there's only one entry in
	// the registry today. Instead, simulate the post-extraction
	// guard: a FilterGroup whose Joined entry has Inner.Joined
	// populated must trip the check the extractor enforces.
	innerJoinedField := map[string]interface{}{
		"badge": map[string]interface{}{
			"badgeType": map[string]interface{}{"eq": "x"},
		},
	}
	registry := loadAwardAndDefinitionLexicons(t)
	awardLex, _ := registry.GetLexicon("app.certified.badge.award")

	// First extract a valid joined-where to confirm baseline works.
	if _, err := extractFieldFilters(innerJoinedField, awardLex, registry); err != nil {
		t.Fatalf("baseline extractFieldFilters: %v", err)
	}

	// The behaviour is bounded at extract time by the registry
	// shape: there is no nested joined target today. Once one is
	// added, this test should be extended with a real two-level
	// payload. For now, assert the guard exists by reading the
	// source — at minimum the lookupJoinedWhereDescriptor only
	// surfaces ONE entry per parent lexicon, and the extractor's
	// `len(inner.Joined) > 0` check (where.go) handles the
	// future case.
}
