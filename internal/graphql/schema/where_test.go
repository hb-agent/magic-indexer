package schema

import (
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
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
