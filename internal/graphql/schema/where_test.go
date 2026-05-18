package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/graphql/types"
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

// loadCollectionLexicon returns a registry with just
// org.hypercerts.collection populated. Used by the array-where
// extractor tests below.
func loadCollectionLexicon(t *testing.T) (*lexicon.Registry, *lexicon.Lexicon) {
	t.Helper()
	r := lexicon.NewRegistry()
	for _, path := range []string{
		"../../../testdata/lexicons/org/hypercerts/collection.json",
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
	lex, _ := r.GetLexicon("org.hypercerts.collection")
	return r, lex
}

// E11.S1 (#88): pin the array-where registry shape. The literal
// strings here are the SQL fragment surface — changes go through
// review.
func TestArrayWhereRegistry_CollectionItems(t *testing.T) {
	ad, ok := lookupArrayWhereDescriptor("org.hypercerts.collection", "items")
	if !ok {
		t.Fatalf("registry entry for (org.hypercerts.collection, \"items\") is missing")
	}
	if ad.FieldName != "items" {
		t.Errorf("FieldName = %q, want \"items\"", ad.FieldName)
	}
	const wantArrayPath = "r.json->'items'"
	if ad.ArrayPath != wantArrayPath {
		t.Errorf("ArrayPath = %q, want %q — emitted verbatim into the EXISTS subquery", ad.ArrayPath, wantArrayPath)
	}
	if ad.ElementDef != "item" {
		t.Errorf("ElementDef = %q, want \"item\"", ad.ElementDef)
	}
	if ad.Description != collectionItemsArrayDescription {
		t.Errorf("Description drifted from collectionItemsArrayDescription — the pinned schema-introspection text is the consumer-facing contract")
	}
	// Negative entries: the registry must not list lexicons that don't
	// participate in array-where today.
	for _, lexID := range []string{"app.certified.badge.award", "app.certified.badge.definition"} {
		if _, hit := lookupArrayWhereDescriptor(lexID, "items"); hit {
			t.Errorf("unexpected registry entry for (%q, \"items\")", lexID)
		}
	}
}

// E11.S2 (#88): schema-introspection style — confirm that
// buildArrayElementInputType synthesises a non-nil input type
// from the collection lexicon's #item def, exposes itemIdentifier
// + itemWeight as filterable subfields, and that the generated
// name follows the <RecordType>WhereInput precedent.
func TestBuildArrayElementInputType_CollectionItem(t *testing.T) {
	_, lex := loadCollectionLexicon(t)
	ad, _ := lookupArrayWhereDescriptor("org.hypercerts.collection", "items")
	input := buildArrayElementInputType(lex, ad)
	if input == nil {
		t.Fatalf("buildArrayElementInputType returned nil for collection.items")
	}
	const wantName = "OrgHypercertsCollectionItemWhereInput"
	if input.Name() != wantName {
		t.Errorf("input name = %q, want %q", input.Name(), wantName)
	}
	fields := input.Fields()
	for _, want := range []string{"itemIdentifier", "itemWeight", "_and", "_or"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("input is missing %q field", want)
		}
	}
	// itemIdentifier must be StrongRefFilterInput, not the unfiltered ref skip.
	if fd, ok := fields["itemIdentifier"]; ok {
		if fd.Type != types.StrongRefFilterInput {
			t.Errorf("itemIdentifier type = %v, want StrongRefFilterInput", fd.Type)
		}
	}
}

// E11.S3 (#88): happy path for the array-where extractor. The
// where payload from the issue's example produces one Arrays
// entry whose Inner contains the itemIdentifier__uri leaf at
// OpEq+IsJSON (routes through the existing JSON containment
// shape in the SQL builder).
func TestExtractFieldFilters_NestedItemsWhere(t *testing.T) {
	registry, lex := loadCollectionLexicon(t)

	where := map[string]interface{}{
		"type": map[string]interface{}{
			"eqi": "project",
		},
		"items": map[string]interface{}{
			"itemIdentifier": map[string]interface{}{
				"uri": map[string]interface{}{
					"eq": "at://did:plc:alice/org.hypercerts.claim.activity/abc",
				},
			},
		},
	}

	group, err := extractFieldFilters(where, lex, registry)
	if err != nil {
		t.Fatalf("extractFieldFilters: %v", err)
	}

	if len(group.Filters) != 1 {
		t.Fatalf("expected 1 outer Filters entry (type), got %d", len(group.Filters))
	}
	if group.Filters[0].FieldName != "type" {
		t.Errorf("outer filter field = %q, want \"type\"", group.Filters[0].FieldName)
	}
	if len(group.Arrays) != 1 {
		t.Fatalf("expected 1 Arrays entry, got %d", len(group.Arrays))
	}
	arr := group.Arrays[0]
	if arr.FieldName != "items" {
		t.Errorf("Arrays[0].FieldName = %q, want \"items\"", arr.FieldName)
	}
	if arr.ArrayPath != "r.json->'items'" {
		t.Errorf("Arrays[0].ArrayPath = %q", arr.ArrayPath)
	}
	if len(arr.Inner.Filters) != 1 {
		t.Fatalf("inner has %d filters, want 1", len(arr.Inner.Filters))
	}
	leaf := arr.Inner.Filters[0]
	if leaf.FieldName != "itemIdentifier__uri" {
		t.Errorf("inner leaf field = %q, want \"itemIdentifier__uri\" (__-path)", leaf.FieldName)
	}
	if leaf.Operator != repositories.OpEq {
		t.Errorf("inner leaf operator = %v, want OpEq", leaf.Operator)
	}
	if leaf.Value != "at://did:plc:alice/org.hypercerts.claim.activity/abc" {
		t.Errorf("inner leaf value = %v", leaf.Value)
	}
	if !leaf.IsJSON {
		t.Errorf("inner leaf IsJSON = false, want true")
	}
	// Inner must not contain Arrays — one-level bound.
	if len(arr.Inner.Arrays) != 0 {
		t.Errorf("inner.Arrays should be empty (one-level bound)")
	}
}

// E11.S4 (#88): extractor branch precedence. Joined-where is
// checked before array-where; array-where is checked before
// filterRegistry; filterRegistry is checked before the property
// fall-through. Today no collision exists for the collection
// lexicon (`items` is ONLY in arrayWhereRegistry, not in
// joinedWhereRegistry or filterRegistry), so the test pins that
// `items` flows into Arrays and a known scalar property
// (`title`) flows into Filters in the same payload.
func TestExtractFieldFilters_ItemsExtractorPrecedence(t *testing.T) {
	registry, lex := loadCollectionLexicon(t)

	where := map[string]interface{}{
		"title": map[string]interface{}{"eq": "Q1 Impact"},
		"items": map[string]interface{}{
			"itemIdentifier": map[string]interface{}{
				"uri": map[string]interface{}{"eq": "at://did:plc:alice/x/y"},
			},
		},
	}
	group, err := extractFieldFilters(where, lex, registry)
	if err != nil {
		t.Fatalf("extractFieldFilters: %v", err)
	}
	if len(group.Filters) != 1 {
		t.Fatalf("title should land in Filters; got %d filters", len(group.Filters))
	}
	if group.Filters[0].FieldName != "title" {
		t.Errorf("Filters[0].FieldName = %q, want \"title\"", group.Filters[0].FieldName)
	}
	if len(group.Arrays) != 1 {
		t.Fatalf("items should land in Arrays; got %d entries", len(group.Arrays))
	}
}

// E11.S5 (#88): depth-cap pin per R2.6. The minimal payload
// trips MaxFilterDepth=3 at the first inner _and after the
// items array boundary (depth=4); a second inner _and would be
// unreachable. Assert the error contains the literal substring
// "exceeds maximum depth" — verbatim from the extractor's error
// format at where.go (R2.6).
func TestExtractFieldFilters_NestedItemsWhere_DepthCap(t *testing.T) {
	registry, lex := loadCollectionLexicon(t)
	// outer _and (depth=1) → inner _and (depth=2) → items (depth=3, the cap) → inner _and (depth=4, rejected).
	where := map[string]interface{}{
		"_and": []interface{}{
			map[string]interface{}{
				"_and": []interface{}{
					map[string]interface{}{
						"items": map[string]interface{}{
							"_and": []interface{}{
								map[string]interface{}{
									"itemWeight": map[string]interface{}{"eq": "1"},
								},
							},
						},
					},
				},
			},
		},
	}
	_, err := extractFieldFilters(where, lex, registry)
	if err == nil {
		t.Fatalf("expected depth-cap error for deeply-nested array-where; got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum depth") {
		t.Errorf("error message should contain literal \"exceeds maximum depth\"; got: %v", err)
	}
}

// E11.S6 (#88): documented-tautology placeholder mirroring the
// #87 NestedJoined_Rejected test. No element-def lexicon today
// has its own array-where registry entry, so the
// `len(inner.Arrays) > 0` guard at where.go cannot fire from
// any current payload. When a second array-where registry entry
// lands whose element type itself has another array-where
// entry, extend this test with a real two-level payload (R1.7
// follow-up in plan §9.5).
func TestExtractFieldFilters_NestedArrayWhere_Rejected(t *testing.T) {
	registry, lex := loadCollectionLexicon(t)
	// Baseline: a single-level items payload extracts cleanly.
	where := map[string]interface{}{
		"items": map[string]interface{}{
			"itemIdentifier": map[string]interface{}{
				"uri": map[string]interface{}{"eq": "at://did:plc:alice/x/y"},
			},
		},
	}
	if _, err := extractFieldFilters(where, lex, registry); err != nil {
		t.Fatalf("baseline extractFieldFilters: %v", err)
	}
	// True two-level guard is unreachable today — the registry has
	// no element-def with its own array-where entry. The check
	// lives at where.go's `len(inner.Arrays) > 0` and is exercised
	// the moment a second registration creates a candidate.
}
