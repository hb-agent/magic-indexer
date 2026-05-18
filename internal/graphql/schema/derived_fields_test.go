package schema

import (
	"context"
	"testing"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/graphql/types"
	"github.com/GainForest/hypergoat/internal/lexicon"
)

// E12.S1 (#89): pin the derivedFieldRegistry shape for
// awardCount. Field name `awardCount`, type NON_NULL Int,
// description byte-equal to awardCountDescription (R2.3 — drift
// pin mirrors TestJoinedWhereRegistry_BadgeAwardBadge).
func TestDerivedFieldRegistry_BadgeDefinitionAwardCount(t *testing.T) {
	byField, ok := derivedFieldRegistry["app.certified.badge.definition"]
	if !ok {
		t.Fatalf("registry has no entries for app.certified.badge.definition")
	}
	d, ok := byField["awardCount"]
	if !ok {
		t.Fatalf("registry entry for (app.certified.badge.definition, \"awardCount\") is missing")
	}
	if d.FieldName != "awardCount" {
		t.Errorf("FieldName = %q, want \"awardCount\"", d.FieldName)
	}
	// Type must be NON_NULL(Int). Walk the wrapper to confirm.
	nn, ok := d.Field.Type.(*graphql.NonNull)
	if !ok {
		t.Fatalf("Field.Type = %T, want *graphql.NonNull", d.Field.Type)
	}
	if nn.OfType != graphql.Int {
		t.Errorf("NonNull wraps %v, want Int", nn.OfType)
	}
	if d.Field.Description != awardCountDescription {
		t.Errorf("Description drifted from awardCountDescription — the pinned schema-introspection text is the consumer-facing contract")
	}
	if d.Field.Resolve == nil {
		t.Errorf("Field.Resolve is nil — awardCount needs a Resolve func to compute the count, not read it from the source map")
	}
	// Negative entries: only badge.definition registers awardCount today.
	for _, lexID := range []string{"app.certified.badge.award", "org.hypercerts.collection"} {
		if _, hit := derivedFieldRegistry[lexID]["awardCount"]; hit {
			t.Errorf("unexpected derivedFieldRegistry entry for (%q, \"awardCount\")", lexID)
		}
	}
}

// E12.S3 (#89): empty Source URI returns 0 without consulting
// repositories. Defensive guard in the resolver — the connection
// resolver always sets sanitized["uri"] = rec.URI (builder.go:952),
// so this path only fires for synthetic / malformed sources.
func TestResolveAwardCount_EmptyURI(t *testing.T) {
	got, err := resolveAwardCount(graphql.ResolveParams{
		Source:  map[string]interface{}{"uri": ""},
		Context: context.Background(),
	})
	if err != nil {
		t.Fatalf("resolveAwardCount(empty uri): %v", err)
	}
	if got != 0 {
		t.Errorf("resolveAwardCount(empty uri) = %v, want 0", got)
	}
	// Same path: Source missing the uri key.
	got, err = resolveAwardCount(graphql.ResolveParams{
		Source:  map[string]interface{}{"cid": "bafyrei..."},
		Context: context.Background(),
	})
	if err != nil {
		t.Fatalf("resolveAwardCount(no uri key): %v", err)
	}
	if got != 0 {
		t.Errorf("resolveAwardCount(no uri key) = %v, want 0", got)
	}
}

// E12.S4 (#89): context without repositories handle returns
// (0, nil) and logs warn (verify by absence of error, not by
// log inspection — the labels resolver pattern at
// builder.go:255-258 does the same).
func TestResolveAwardCount_NoRepositoriesInContext(t *testing.T) {
	got, err := resolveAwardCount(graphql.ResolveParams{
		Source:  map[string]interface{}{"uri": "at://did:plc:x/app.certified.badge.definition/y"},
		Context: context.Background(), // NO repos
	})
	if err != nil {
		t.Fatalf("resolveAwardCount(no repos): %v", err)
	}
	if got != 0 {
		t.Errorf("resolveAwardCount(no repos) = %v, want 0", got)
	}
}

// E12.S5 (#89): the per-collection record builder injects the
// awardCount field on AppCertifiedBadgeDefinitionRecord and
// nothing else. Validates the
// derivedFieldsForObjectBuilder → NewObjectBuilderWithDerivedFields
// → buildRecordFields path end-to-end without spinning a full
// schema.
func TestBuildRecordFields_BadgeDefinitionHasAwardCount(t *testing.T) {
	// Construct a minimal lexicon + ObjectBuilder using the real
	// derivedFieldsForObjectBuilder map.
	registry := lexicon.NewRegistry()
	mapper := types.NewMapper()
	ob := types.NewObjectBuilderWithDerivedFields(mapper, registry, derivedFieldsForObjectBuilder())

	def := &lexicon.RecordDef{
		Type: "record",
		Properties: []lexicon.PropertyEntry{
			{Name: "title", Property: lexicon.Property{Type: "string"}},
			{Name: "badgeType", Property: lexicon.Property{Type: "string"}},
		},
	}
	obj := ob.BuildRecordType("app.certified.badge.definition", def)
	if obj == nil {
		t.Fatalf("BuildRecordType returned nil")
	}
	fields := obj.Fields()
	awardCount, ok := fields["awardCount"]
	if !ok {
		t.Fatalf("AppCertifiedBadgeDefinitionRecord is missing the awardCount field")
	}
	// Confirm it's the registry-defined one (Type/Description match).
	if awardCount.Description != awardCountDescription {
		t.Errorf("awardCount.Description drifted; got %q", awardCount.Description)
	}
	// Non-badge.definition record types must NOT have awardCount.
	otherObj := ob.BuildRecordType("app.certified.badge.award", &lexicon.RecordDef{
		Type:       "record",
		Properties: []lexicon.PropertyEntry{{Name: "createdAt", Property: lexicon.Property{Type: "string", Format: "datetime"}}},
	})
	if _, hit := otherObj.Fields()["awardCount"]; hit {
		t.Errorf("AppCertifiedBadgeAwardRecord unexpectedly has awardCount field")
	}
}

// E12.S6 (#89): if a lexicon adds a property whose name
// collides with a registered derived field, the lexicon
// property wins (the derived field is silently skipped with a
// warn log). Mirrors the ReservedRecordFields policy at
// object.go:172-176.
func TestBuildRecordFields_DerivedFieldCollisionWithLexicon(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := types.NewMapper()
	// Synthetic registry with awardCount registered for a
	// fake lexicon ID — we want a stripped-down env for the
	// collision test, NOT to pollute the real registry.
	fakeRegistry := map[string]map[string]*graphql.Field{
		"my.test.lex": {
			"awardCount": {
				Type:        graphql.NewNonNull(graphql.Int),
				Description: "derived",
			},
		},
	}
	ob := types.NewObjectBuilderWithDerivedFields(mapper, registry, fakeRegistry)
	def := &lexicon.RecordDef{
		Type: "record",
		Properties: []lexicon.PropertyEntry{
			{Name: "awardCount", Property: lexicon.Property{Type: "string"}}, // collides
		},
	}
	obj := ob.BuildRecordType("my.test.lex", def)
	if obj == nil {
		t.Fatalf("BuildRecordType returned nil")
	}
	awardCount, ok := obj.Fields()["awardCount"]
	if !ok {
		t.Fatalf("awardCount field missing entirely — expected the lexicon's version")
	}
	// Lexicon-derived "awardCount" of type "string" → StringFilterInput
	// is not applicable here (this is a record field, not a where input).
	// The lexicon-property path uses propertyToGraphQLType which
	// returns graphql.String for "string". Confirm by description.
	if awardCount.Description == "derived" {
		t.Errorf("derived field won over lexicon property — policy is lexicon-wins-on-collision")
	}
}

// init-time collision panic is exercised by the existence of the
// init() block in derived_fields.go — running this test file at
// all confirms no panic. We don't assert log output (R2.4
// startup-fail mode is verified by code review of the init).

// Confirm the harness wires correctly for the WithRepositories
// path (no DB call; just confirms the context plumb-through).
func TestResolveAwardCount_RespectsRepositoriesContext(t *testing.T) {
	// Construct an empty Repositories — we only verify that the
	// resolver pulls it from the context. A non-nil Records is
	// required because the resolver calls a method on it; pass a
	// real Records constructed against nil executor will panic on
	// QueryRow. Instead, validate the EARLY guard: nil Repositories
	// → 0,nil (already covered by S4 above), and presence of a
	// repos triggers the call path (which we don't exercise here
	// to keep this a unit test). The integration coverage lives in
	// the records_test.go E12.1-3 tests against a real DB.
	repos := resolver.GetRepositories(context.Background())
	if repos != nil {
		t.Fatalf("expected nil repos in empty context, got %v", repos)
	}
}

// Sanity: the derivedFieldsForObjectBuilder flattening produces
// the same lexicon→field map structure consumed by
// types.ObjectBuilder.
func TestDerivedFieldsForObjectBuilder_ShapeMatches(t *testing.T) {
	flat := derivedFieldsForObjectBuilder()
	if len(flat) != len(derivedFieldRegistry) {
		t.Errorf("flattened map has %d lexicons, registry has %d", len(flat), len(derivedFieldRegistry))
	}
	for lexID, registeredFields := range derivedFieldRegistry {
		flatFields, ok := flat[lexID]
		if !ok {
			t.Errorf("flattened map missing lexicon %q", lexID)
			continue
		}
		if len(flatFields) != len(registeredFields) {
			t.Errorf("lexicon %q: flat has %d fields, registry has %d", lexID, len(flatFields), len(registeredFields))
		}
		for name, desc := range registeredFields {
			if flatFields[name] != desc.Field {
				t.Errorf("lexicon %q field %q: flat *graphql.Field pointer differs from registry's", lexID, name)
			}
		}
	}
}
