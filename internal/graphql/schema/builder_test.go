package schema

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/lexicon"
)

// loadLexiconsFromDir loads all lexicon JSON files from a directory tree.
func loadLexiconsFromDir(dir string) ([]*lexicon.Lexicon, error) {
	var lexicons []*lexicon.Lexicon

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lex, parseErr := lexicon.ParseBytes(data)
		if parseErr != nil {
			// Skip non-lexicon JSON files
			return nil //nolint:nilerr // intentionally skip parse errors
		}

		lexicons = append(lexicons, lex)
		return nil
	})

	return lexicons, err
}

func TestBuildSchemaFromHypercertsLexicons(t *testing.T) {
	// Load all hypercerts lexicons
	lexicons, err := loadLexiconsFromDir("../../../testdata/lexicons")
	if err != nil {
		t.Fatalf("Failed to load lexicons: %v", err)
	}

	if len(lexicons) == 0 {
		t.Fatal("No lexicons loaded")
	}

	t.Logf("Loaded %d lexicons", len(lexicons))
	for _, lex := range lexicons {
		t.Logf("  - %s", lex.ID)
	}

	// Create registry and register all lexicons
	registry := lexicon.NewRegistry()
	for _, lex := range lexicons {
		registry.Register(lex)
	}

	// Build schema
	builder := NewBuilder(registry)
	schema, err := builder.Build()
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	// Verify schema has Query type
	queryType := schema.QueryType()
	if queryType == nil {
		t.Fatal("Schema has no Query type")
	}

	// Log all query fields
	t.Log("Query fields:")
	for name := range queryType.Fields() {
		t.Logf("  - %s", name)
	}

	// Verify we have the activity claim field
	activityField := queryType.Fields()["orgHypercertsClaimActivity"]
	if activityField == nil {
		t.Error("Missing orgHypercertsClaimActivity query field")
	} else {
		t.Logf("Activity field type: %s", activityField.Type.Name())
	}

	// Verify single record lookup
	activityByURI := queryType.Fields()["orgHypercertsClaimActivityByUri"]
	if activityByURI == nil {
		t.Error("Missing orgHypercertsClaimActivityByUri query field")
	}
}

func TestActivityClaimType(t *testing.T) {
	// Load activity claim lexicon specifically
	data, err := os.ReadFile("../../../testdata/lexicons/org/hypercerts/claim/activity.json")
	if err != nil {
		t.Fatalf("Failed to read activity.json: %v", err)
	}

	lex, err := lexicon.ParseBytes(data)
	if err != nil {
		t.Fatalf("Failed to parse activity.json: %v", err)
	}

	// Load supporting lexicons
	defsData, _ := os.ReadFile("../../../testdata/lexicons/org/hypercerts/defs.json")
	defsLex, _ := lexicon.ParseBytes(defsData)

	strongRefData, _ := os.ReadFile("../../../testdata/lexicons/com/atproto/repo/strongRef.json")
	strongRefLex, _ := lexicon.ParseBytes(strongRefData)

	// Create registry
	registry := lexicon.NewRegistry()
	registry.Register(lex)
	if defsLex != nil {
		registry.Register(defsLex)
	}
	if strongRefLex != nil {
		registry.Register(strongRefLex)
	}

	// Build schema
	builder := NewBuilder(registry)
	schema, err := builder.Build()
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	// Get the activity type
	activityType := builder.GetRecordType("org.hypercerts.claim.activity")
	if activityType == nil {
		t.Fatal("Activity record type not built")
	}

	t.Logf("Activity type: %s", activityType.Name())

	// Verify fields
	fields := activityType.Fields()
	expectedFields := []string{
		"uri", "cid", // Standard record fields
		"title", "shortDescription", "createdAt", // Required fields
		"description", "image", "workScope", "startDate", "endDate",
		"contributors", "rights", "locations",
	}

	for _, fieldName := range expectedFields {
		field, ok := fields[fieldName]
		if !ok {
			t.Errorf("Missing field: %s", fieldName)
		} else {
			t.Logf("  Field %s: %s", fieldName, field.Type.String())
		}
	}

	// Test query execution
	query := `{
		orgHypercertsClaimActivity(first: 10) {
			edges {
				cursor
				node {
					uri
					title
					shortDescription
				}
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
			}
		}
	}`

	result := graphql.Do(graphql.Params{
		Schema:        *schema,
		RequestString: query,
		Context:       context.Background(),
	})

	if len(result.Errors) > 0 {
		t.Errorf("GraphQL query errors: %v", result.Errors)
	} else {
		jsonResult, _ := json.MarshalIndent(result.Data, "", "  ")
		t.Logf("Query result:\n%s", jsonResult)
	}
}

func TestRecordType_HasPDSField(t *testing.T) {
	// pds is a "reserved" record metadata field — like uri, cid, did,
	// rkey, labels — synthesised on every record type regardless of
	// the lexicon definition. This test pins that contract: removing
	// pds from buildRecordFields would silently break every consumer
	// querying the field.
	data, err := os.ReadFile("../../../testdata/lexicons/org/hypercerts/claim/activity.json")
	if err != nil {
		t.Fatalf("read activity.json: %v", err)
	}
	lex, err := lexicon.ParseBytes(data)
	if err != nil {
		t.Fatalf("parse activity.json: %v", err)
	}
	registry := lexicon.NewRegistry()
	registry.Register(lex)
	for _, p := range []string{
		"../../../testdata/lexicons/org/hypercerts/defs.json",
		"../../../testdata/lexicons/com/atproto/repo/strongRef.json",
	} {
		if d, err := os.ReadFile(p); err == nil {
			if l, err := lexicon.ParseBytes(d); err == nil {
				registry.Register(l)
			}
		}
	}

	builder := NewBuilder(registry)
	if _, err := builder.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	recordType := builder.GetRecordType("org.hypercerts.claim.activity")
	if recordType == nil {
		t.Fatal("activity record type not built")
	}
	field, ok := recordType.Fields()["pds"]
	if !ok {
		t.Fatal("expected 'pds' field on record type, not found")
	}
	// Nullable String — clients rely on null meaning "not yet resolved".
	if field.Type.String() != "String" {
		t.Errorf("expected pds type to be nullable String, got %s", field.Type.String())
	}
}

func TestConnection_HasExcludePdsArg(t *testing.T) {
	// excludePds is wired globally via PDSFilterArgs() in
	// ConnectionArgs(), so every record connection should carry the
	// arg. Pin that contract on the activity connection field.
	data, err := os.ReadFile("../../../testdata/lexicons/org/hypercerts/claim/activity.json")
	if err != nil {
		t.Fatalf("read activity.json: %v", err)
	}
	lex, err := lexicon.ParseBytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	registry := lexicon.NewRegistry()
	registry.Register(lex)
	for _, p := range []string{
		"../../../testdata/lexicons/org/hypercerts/defs.json",
		"../../../testdata/lexicons/com/atproto/repo/strongRef.json",
	} {
		if d, err := os.ReadFile(p); err == nil {
			if l, err := lexicon.ParseBytes(d); err == nil {
				registry.Register(l)
			}
		}
	}

	builder := NewBuilder(registry)
	schema, err := builder.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	queryFields := schema.QueryType().Fields()
	field, ok := queryFields["orgHypercertsClaimActivity"]
	if !ok {
		t.Fatal("expected orgHypercertsClaimActivity query field; not found")
	}
	var foundExcludePds bool
	for _, arg := range field.Args {
		if arg.Name() == "excludePds" {
			foundExcludePds = true
			// Type should be [String!] (nullable list of non-null strings).
			if arg.Type.String() != "[String!]" {
				t.Errorf("excludePds type = %s, want [String!]", arg.Type.String())
			}
			break
		}
	}
	if !foundExcludePds {
		args := make([]string, 0, len(field.Args))
		for _, a := range field.Args {
			args = append(args, a.Name())
		}
		t.Errorf("expected excludePds arg on connection, got args: %v", args)
	}

	// Generic records query should also have the arg.
	genericField, ok := queryFields["records"]
	if !ok {
		t.Fatal("expected generic 'records' query field; not found")
	}
	foundExcludePds = false
	for _, arg := range genericField.Args {
		if arg.Name() == "excludePds" {
			foundExcludePds = true
			break
		}
	}
	if !foundExcludePds {
		t.Error("expected excludePds arg on generic records query")
	}
}

// TestActivityWhereInput_HasContributorFilter pins the contract that
// only the activity collection's WhereInput carries the `contributor`
// filter field (and that it is typed DIDFilterInput).
func TestActivityWhereInput_HasContributorFilter(t *testing.T) {
	lexicons, err := loadLexiconsFromDir("../../../testdata/lexicons")
	if err != nil {
		t.Fatalf("load lexicons: %v", err)
	}
	registry := lexicon.NewRegistry()
	for _, lex := range lexicons {
		registry.Register(lex)
	}
	builder := NewBuilder(registry)
	schema, err := builder.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	queryFields := schema.QueryType().Fields()

	// Helper: locate the `where` arg on a per-collection field and return
	// the input-object fields exposed by its type.
	whereFieldsFor := func(t *testing.T, fieldName string) graphql.InputObjectFieldMap {
		t.Helper()
		field, ok := queryFields[fieldName]
		if !ok {
			t.Fatalf("query field %q not found", fieldName)
		}
		for _, arg := range field.Args {
			if arg.Name() != "where" {
				continue
			}
			io, ok := arg.Type.(*graphql.InputObject)
			if !ok {
				t.Fatalf("where arg on %q is not an InputObject (got %T)", fieldName, arg.Type)
			}
			return io.Fields()
		}
		return nil
	}

	// Activity WhereInput must expose `contributor` typed DIDFilterInput.
	activityWhere := whereFieldsFor(t, "orgHypercertsClaimActivity")
	contrib, ok := activityWhere["contributor"]
	if !ok {
		names := make([]string, 0, len(activityWhere))
		for k := range activityWhere {
			names = append(names, k)
		}
		t.Fatalf("contributor field missing on activity WhereInput; got: %v", names)
	}
	if contrib.Type.Name() != "DIDFilterInput" {
		t.Errorf("contributor field type = %s, want DIDFilterInput", contrib.Type.Name())
	}
	desc := contrib.Description()
	if !strings.Contains(desc, "DIDs only") {
		t.Errorf("contributor description missing DID-only policy callout: %q", desc)
	}

	// Absence assertion: pick a loaded collection that is NOT activity
	// and confirm `contributor` is not on its WhereInput.
	awardWhere := whereFieldsFor(t, "appCertifiedBadgeAward")
	if _, leaked := awardWhere["contributor"]; leaked {
		names := make([]string, 0, len(awardWhere))
		for k := range awardWhere {
			names = append(names, k)
		}
		t.Errorf("contributor field leaked to badge award WhereInput; fields: %v", names)
	}
}

func TestUnionTypes(t *testing.T) {
	// Load lexicons
	activityData, _ := os.ReadFile("../../../testdata/lexicons/org/hypercerts/claim/activity.json")
	activityLex, _ := lexicon.ParseBytes(activityData)

	defsData, _ := os.ReadFile("../../../testdata/lexicons/org/hypercerts/defs.json")
	defsLex, _ := lexicon.ParseBytes(defsData)

	strongRefData, _ := os.ReadFile("../../../testdata/lexicons/com/atproto/repo/strongRef.json")
	strongRefLex, _ := lexicon.ParseBytes(strongRefData)

	registry := lexicon.NewRegistry()
	if activityLex != nil {
		registry.Register(activityLex)
	}
	if defsLex != nil {
		registry.Register(defsLex)
	}
	if strongRefLex != nil {
		registry.Register(strongRefLex)
	}

	builder := NewBuilder(registry)
	_, err := builder.Build()
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	// Get activity type and check union fields
	activityType := builder.GetRecordType("org.hypercerts.claim.activity")
	if activityType == nil {
		t.Fatal("Activity type not found")
	}

	fields := activityType.Fields()

	// image is a union of org.hypercerts.defs#uri | org.hypercerts.defs#smallImage
	imageField := fields["image"]
	if imageField == nil {
		t.Error("Missing image field")
	} else {
		t.Logf("image field type: %s", imageField.Type.String())
	}

	// workScope is a union of com.atproto.repo.strongRef | #workScopeString
	workScopeField := fields["workScope"]
	if workScopeField == nil {
		t.Error("Missing workScope field")
	} else {
		t.Logf("workScope field type: %s", workScopeField.Type.String())
	}
}

func TestSchemaIntrospection(t *testing.T) {
	// Load all lexicons
	lexicons, err := loadLexiconsFromDir("../../../testdata/lexicons")
	if err != nil {
		t.Fatalf("Failed to load lexicons: %v", err)
	}

	registry := lexicon.NewRegistry()
	for _, lex := range lexicons {
		registry.Register(lex)
	}

	builder := NewBuilder(registry)
	schema, err := builder.Build()
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	// Test introspection query
	query := `{
		__schema {
			queryType {
				name
				fields {
					name
					type {
						name
						kind
					}
				}
			}
			types {
				name
				kind
			}
		}
	}`

	result := graphql.Do(graphql.Params{
		Schema:        *schema,
		RequestString: query,
	})

	if len(result.Errors) > 0 {
		t.Errorf("Introspection errors: %v", result.Errors)
	}

	jsonResult, _ := json.MarshalIndent(result.Data, "", "  ")
	t.Logf("Introspection result:\n%s", jsonResult)
}
