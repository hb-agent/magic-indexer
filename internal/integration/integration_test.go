//go:build integration

// Package integration provides end-to-end integration tests for hypergoat.
//
// Run with: go test -tags=integration -v ./internal/integration/...
package integration

import (
	"context"
	"testing"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	hgraphql "github.com/GainForest/hypergoat/internal/graphql"
	"github.com/GainForest/hypergoat/internal/graphql/admin"
	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/testutil"

	"github.com/graphql-go/graphql"
)

// testDB holds test database resources.
type testDB struct {
	Executor     database.Executor
	Records      *repositories.RecordsRepository
	Actors       *repositories.ActorsRepository
	Config       *repositories.ConfigRepository
	Lexicons     *repositories.LexiconsRepository
	OAuthClients *repositories.OAuthClientsRepository
}

// setupTestDB creates a Postgres test database with migrations applied.
func setupTestDB(t *testing.T) *testDB {
	t.Helper()

	tdb := testutil.SetupTestDB(t)

	return &testDB{
		Executor:     tdb.Executor,
		Records:      tdb.Records,
		Actors:       tdb.Actors,
		Config:       tdb.Config,
		Lexicons:     tdb.Lexicons,
		OAuthClients: tdb.OAuthClients,
	}
}

// seedTestData seeds the test database with sample data.
func (db *testDB) seedTestData(t *testing.T, ctx context.Context) {
	t.Helper()

	actors := []struct {
		did    string
		handle string
	}{
		{"did:plc:user1", "user1.bsky.social"},
		{"did:plc:user2", "user2.bsky.social"},
		{"did:plc:admin1", "admin.example.com"},
	}

	for _, a := range actors {
		if err := db.Actors.Upsert(ctx, a.did, a.handle); err != nil {
			t.Fatalf("Failed to seed actor %s: %v", a.did, err)
		}
	}

	configs := map[string]string{
		"domain_authority": "example.com",
		"admin_dids":       "did:plc:admin1",
		"relay_url":        "wss://relay.example.com",
	}

	for key, value := range configs {
		if err := db.Config.Set(ctx, key, value); err != nil {
			t.Fatalf("Failed to seed config %s: %v", key, err)
		}
	}

	records := []*repositories.Record{
		{
			URI:        "at://did:plc:user1/example.post/1",
			CID:        "bafyreiabc123",
			DID:        "did:plc:user1",
			Collection: "example.post",
			JSON:       `{"text": "Hello world", "$type": "example.post"}`,
			RKey:       "1",
		},
		{
			URI:        "at://did:plc:user1/example.post/2",
			CID:        "bafyreidef456",
			DID:        "did:plc:user1",
			Collection: "example.post",
			JSON:       `{"text": "Second post", "$type": "example.post"}`,
			RKey:       "2",
		},
		{
			URI:        "at://did:plc:user2/example.post/1",
			CID:        "bafyreighi789",
			DID:        "did:plc:user2",
			Collection: "example.post",
			JSON:       `{"text": "User 2 post", "$type": "example.post"}`,
			RKey:       "1",
		},
	}

	if err := db.Records.BatchInsert(ctx, records); err != nil {
		t.Fatalf("Failed to seed records: %v", err)
	}
}

// executeQuery executes a GraphQL query.
func executeQuery(schema *graphql.Schema, query string, ctx context.Context) *graphql.Result {
	return graphql.Do(graphql.Params{
		Schema:        *schema,
		RequestString: query,
		Context:       ctx,
	})
}

// ========== Records Repository Tests ==========

func TestRecords_BatchInsert(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	records := []*repositories.Record{
		{URI: "at://did:plc:test/col/1", CID: "cid1", DID: "did:plc:test", Collection: "col", JSON: `{"test":1}`, RKey: "1"},
		{URI: "at://did:plc:test/col/2", CID: "cid2", DID: "did:plc:test", Collection: "col", JSON: `{"test":2}`, RKey: "2"},
		{URI: "at://did:plc:test/col/3", CID: "cid3", DID: "did:plc:test", Collection: "col", JSON: `{"test":3}`, RKey: "3"},
	}

	err := db.Records.BatchInsert(ctx, records)
	if err != nil {
		t.Fatalf("BatchInsert failed: %v", err)
	}

	count, err := db.Records.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount failed: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 records, got %d", count)
	}
}

func TestRecords_GetCIDsByURIs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	records := []*repositories.Record{
		{URI: "at://did:plc:test/col/1", CID: "cid1", DID: "did:plc:test", Collection: "col", JSON: `{}`, RKey: "1"},
		{URI: "at://did:plc:test/col/2", CID: "cid2", DID: "did:plc:test", Collection: "col", JSON: `{}`, RKey: "2"},
	}
	if err := db.Records.BatchInsert(ctx, records); err != nil {
		t.Fatalf("BatchInsert failed: %v", err)
	}

	cidMap, err := db.Records.GetCIDsByURIs(ctx, []string{
		"at://did:plc:test/col/1",
		"at://did:plc:test/col/2",
		"at://did:plc:test/col/nonexistent",
	})
	if err != nil {
		t.Fatalf("GetCIDsByURIs failed: %v", err)
	}

	if len(cidMap) != 2 {
		t.Errorf("Expected 2 CIDs, got %d", len(cidMap))
	}
	if cidMap["at://did:plc:test/col/1"] != "cid1" {
		t.Errorf("Expected cid1, got %s", cidMap["at://did:plc:test/col/1"])
	}
}

func TestRecords_GetExistingCIDs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	records := []*repositories.Record{
		{URI: "at://did:plc:test/col/1", CID: "existingcid1", DID: "did:plc:test", Collection: "col", JSON: `{}`, RKey: "1"},
		{URI: "at://did:plc:test/col/2", CID: "existingcid2", DID: "did:plc:test", Collection: "col", JSON: `{}`, RKey: "2"},
	}
	if err := db.Records.BatchInsert(ctx, records); err != nil {
		t.Fatalf("BatchInsert failed: %v", err)
	}

	existingSet, err := db.Records.GetExistingCIDs(ctx, []string{"existingcid1", "newcid", "existingcid2"})
	if err != nil {
		t.Fatalf("GetExistingCIDs failed: %v", err)
	}

	if len(existingSet) != 2 {
		t.Errorf("Expected 2 existing CIDs, got %d", len(existingSet))
	}
	if !existingSet["existingcid1"] {
		t.Error("Expected existingcid1 to exist")
	}
	if existingSet["newcid"] {
		t.Error("Expected newcid to NOT exist")
	}
}

func TestRecords_Deduplication(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert initial records
	initial := []*repositories.Record{
		{URI: "at://did:plc:test/col/1", CID: "cidA", DID: "did:plc:test", Collection: "col", JSON: `{"v":1}`, RKey: "1"},
		{URI: "at://did:plc:test/col/2", CID: "cidB", DID: "did:plc:test", Collection: "col", JSON: `{"v":2}`, RKey: "2"},
	}
	if err := db.Records.BatchInsert(ctx, initial); err != nil {
		t.Fatalf("Initial insert failed: %v", err)
	}

	// Backfill scenario
	backfillRecords := []*repositories.Record{
		// Unchanged - same URI and CID
		{URI: "at://did:plc:test/col/1", CID: "cidA", DID: "did:plc:test", Collection: "col", JSON: `{"v":1}`, RKey: "1"},
		// Updated - same URI, different CID
		{URI: "at://did:plc:test/col/2", CID: "cidBnew", DID: "did:plc:test", Collection: "col", JSON: `{"v":2.1}`, RKey: "2"},
		// New record
		{URI: "at://did:plc:test/col/3", CID: "cidC", DID: "did:plc:test", Collection: "col", JSON: `{"v":3}`, RKey: "3"},
		// New URI but duplicate CID
		{URI: "at://did:plc:other/col/1", CID: "cidA", DID: "did:plc:other", Collection: "col", JSON: `{"v":1}`, RKey: "1"},
	}

	// Get dedup info
	uris := make([]string, len(backfillRecords))
	cidSet := make(map[string]bool)
	var cids []string
	for i, r := range backfillRecords {
		uris[i] = r.URI
		if !cidSet[r.CID] {
			cids = append(cids, r.CID)
			cidSet[r.CID] = true
		}
	}

	existingByURI, _ := db.Records.GetCIDsByURIs(ctx, uris)
	existingCIDs, _ := db.Records.GetExistingCIDs(ctx, cids)

	// Filter records
	var filtered []*repositories.Record
	var skipped int
	for _, rec := range backfillRecords {
		if existingCID, ok := existingByURI[rec.URI]; ok {
			if existingCID == rec.CID {
				skipped++
				continue
			}
			if existingCIDs[rec.CID] {
				skipped++
				continue
			}
		} else {
			if existingCIDs[rec.CID] {
				skipped++
				continue
			}
		}
		filtered = append(filtered, rec)
	}

	t.Logf("Total: %d, Filtered: %d, Skipped: %d", len(backfillRecords), len(filtered), skipped)

	if skipped != 2 {
		t.Errorf("Expected 2 skipped, got %d", skipped)
	}
	if len(filtered) != 2 {
		t.Errorf("Expected 2 filtered, got %d", len(filtered))
	}

	// Insert filtered
	if len(filtered) > 0 {
		if err := db.Records.BatchInsert(ctx, filtered); err != nil {
			t.Fatalf("BatchInsert filtered failed: %v", err)
		}
	}

	count, _ := db.Records.GetCount(ctx)
	if count != 3 {
		t.Errorf("Expected 3 records after dedup, got %d", count)
	}
}

func TestRecords_LargeBatch(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create 250 records
	records := make([]*repositories.Record, 250)
	for i := 0; i < 250; i++ {
		records[i] = &repositories.Record{
			URI:        "at://did:plc:test/col/" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			CID:        "cid" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			DID:        "did:plc:test",
			Collection: "col",
			JSON:       `{}`,
			RKey:       string(rune('A'+i%26)) + string(rune('0'+i/26)),
		}
	}

	err := db.Records.BatchInsert(ctx, records)
	if err != nil {
		t.Fatalf("BatchInsert large batch failed: %v", err)
	}

	count, _ := db.Records.GetCount(ctx)
	if count != 250 {
		t.Errorf("Expected 250 records, got %d", count)
	}
}

// ========== Admin GraphQL Tests ==========

// buildAdminSchema creates the admin GraphQL schema for testing.
func buildAdminSchema(db *testDB) (*graphql.Schema, error) {
	repos := &admin.Repositories{
		Records:      db.Records,
		Actors:       db.Actors,
		Lexicons:     db.Lexicons,
		Config:       db.Config,
		OAuthClients: db.OAuthClients,
	}
	resolver := admin.NewResolver(repos, "did:plc:test-labeler")
	builder := admin.NewSchemaBuilder(resolver)
	return builder.Build()
}

func TestAdminGraphQL_Statistics(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	db.seedTestData(t, ctx)

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	adminCtx := admin.ContextWithAuth(ctx, "did:plc:admin1", "admin.example.com", true, []string{"did:plc:admin1"})

	query := `{
		statistics {
			recordCount
			actorCount
			lexiconCount
		}
	}`

	result := executeQuery(schema, query, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors: %v", result.Errors)
	}

	data := result.Data.(map[string]interface{})
	stats := data["statistics"].(map[string]interface{})

	// GraphQL returns ints as int or int64, handle both
	recordCount := toInt(stats["recordCount"])
	actorCount := toInt(stats["actorCount"])

	if recordCount != 3 {
		t.Errorf("Expected 3 records, got %d", recordCount)
	}
	if actorCount != 3 {
		t.Errorf("Expected 3 actors, got %d", actorCount)
	}
}

// toInt converts GraphQL numeric values to int.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func TestAdminGraphQL_Settings(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	db.seedTestData(t, ctx)

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	adminCtx := admin.ContextWithAuth(ctx, "did:plc:admin1", "admin.example.com", true, []string{"did:plc:admin1"})

	query := `{ settings { domainAuthority adminDids } }`

	result := executeQuery(schema, query, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors: %v", result.Errors)
	}

	data := result.Data.(map[string]interface{})
	settings := data["settings"].(map[string]interface{})

	if settings["domainAuthority"] != "example.com" {
		t.Errorf("Expected 'example.com', got '%v'", settings["domainAuthority"])
	}
}

func TestAdminGraphQL_UpdateSettings(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	adminCtx := admin.ContextWithAuth(ctx, "did:plc:admin1", "admin.example.com", true, []string{"did:plc:admin1"})

	mutation := `mutation {
		updateSettings(domainAuthority: "newdomain.com") {
			domainAuthority
		}
	}`

	result := executeQuery(schema, mutation, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors: %v", result.Errors)
	}

	data := result.Data.(map[string]interface{})
	settings := data["updateSettings"].(map[string]interface{})

	if settings["domainAuthority"] != "newdomain.com" {
		t.Errorf("Expected 'newdomain.com', got '%v'", settings["domainAuthority"])
	}
}

func TestAdminGraphQL_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	userCtx := admin.ContextWithAuth(ctx, "did:plc:user1", "user.bsky.social", false, []string{"did:plc:admin1"})

	mutation := `mutation {
		updateSettings(domainAuthority: "hacked.com") {
			domainAuthority
		}
	}`

	result := executeQuery(schema, mutation, userCtx)
	if len(result.Errors) == 0 {
		t.Error("Expected error for non-admin mutation")
	}
}

func TestAdminGraphQL_CurrentSession(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	userCtx := admin.ContextWithAuth(ctx, "did:plc:user1", "user.bsky.social", false, []string{"did:plc:admin1"})

	query := `{ currentSession { did handle isAdmin } }`

	result := executeQuery(schema, query, userCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors: %v", result.Errors)
	}

	data := result.Data.(map[string]interface{})
	session := data["currentSession"].(map[string]interface{})

	if session["did"] != "did:plc:user1" {
		t.Errorf("Expected 'did:plc:user1', got '%v'", session["did"])
	}
	if session["isAdmin"] != false {
		t.Errorf("Expected isAdmin false")
	}
}

func TestAdminGraphQL_OAuthClients(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	schema, err := buildAdminSchema(db)
	if err != nil {
		t.Fatalf("Failed to build schema: %v", err)
	}

	adminCtx := admin.ContextWithAuth(ctx, "did:plc:admin1", "admin.example.com", true, []string{"did:plc:admin1"})

	// Create - using individual arguments, not input object
	createMutation := `mutation {
		createOAuthClient(
			clientName: "Test App"
			clientType: "public"
			redirectUris: ["https://testapp.example.com/callback"]
		) {
			clientId
			clientName
		}
	}`

	result := executeQuery(schema, createMutation, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors on create: %v", result.Errors)
	}

	data := result.Data.(map[string]interface{})
	createdClient := data["createOAuthClient"].(map[string]interface{})
	clientId := createdClient["clientId"].(string)

	// Query
	query := `{ oauthClients { clientId } }`
	result = executeQuery(schema, query, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors on query: %v", result.Errors)
	}

	data = result.Data.(map[string]interface{})
	clients := data["oauthClients"].([]interface{})
	if len(clients) != 1 {
		t.Errorf("Expected 1 client, got %d", len(clients))
	}

	// Delete - use the generated clientId
	deleteMutation := `mutation { deleteOAuthClient(clientId: "` + clientId + `") }`
	result = executeQuery(schema, deleteMutation, adminCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("GraphQL errors on delete: %v", result.Errors)
	}

	// Verify deletion
	result = executeQuery(schema, query, adminCtx)
	data = result.Data.(map[string]interface{})
	clients = data["oauthClients"].([]interface{})
	if len(clients) != 0 {
		t.Errorf("Expected 0 clients after delete, got %d", len(clients))
	}
}

// ========== Public GraphQL + Label Filter End-to-End ==========

// TestPublicGraphQL_LabelFilter stitches the whole ingest-to-query
// story together: seeds records + labels via the real repositories,
// builds the public GraphQL schema via the real lexicon registry +
// schema builder, executes a records(filter: {labels: [...]}) query
// through graphql.Do, and asserts that the filter subquery +
// batch-label-load path both return what the end-to-end review
// expected. This is the concrete regression test for issue #15.
func TestPublicGraphQL_LabelFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Seed three records in a simple test collection.
	recs := []*repositories.Record{
		{URI: "at://did:plc:alice/app.bsky.feed.post/1", CID: "cidA1", DID: "did:plc:alice", Collection: "app.bsky.feed.post", JSON: `{"text":"clean"}`, RKey: "1"},
		{URI: "at://did:plc:alice/app.bsky.feed.post/2", CID: "cidA2", DID: "did:plc:alice", Collection: "app.bsky.feed.post", JSON: `{"text":"hq"}`, RKey: "2"},
		{URI: "at://did:plc:bob/app.bsky.feed.post/1", CID: "cidB1", DID: "did:plc:bob", Collection: "app.bsky.feed.post", JSON: `{"text":"draft"}`, RKey: "1"},
	}
	if err := db.Records.BatchInsert(ctx, recs); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	// Seed label definitions + labels. The labeler is a test DID.
	labeler := "did:plc:test-labeler"
	labels := repositories.NewLabelsRepository(db.Executor)
	defs := repositories.NewLabelDefinitionsRepository(db.Executor)
	if err := defs.Insert(ctx, labeler, "high-quality", "", repositories.SeverityInform, repositories.VisibilityWarn); err != nil {
		t.Fatalf("label def insert: %v", err)
	}
	if err := defs.Insert(ctx, labeler, "draft", "", repositories.SeverityInform, repositories.VisibilityWarn); err != nil {
		t.Fatalf("label def insert: %v", err)
	}
	if _, err := labels.Insert(ctx, labeler, "at://did:plc:alice/app.bsky.feed.post/2", nil, "high-quality", nil, nil); err != nil {
		t.Fatalf("label insert: %v", err)
	}
	if _, err := labels.Insert(ctx, labeler, "at://did:plc:bob/app.bsky.feed.post/1", nil, "draft", nil, nil); err != nil {
		t.Fatalf("label insert: %v", err)
	}

	// Build the public GraphQL schema against an empty lexicon
	// registry (generic record path), then query it.
	registry := lexicon.NewRegistry()
	repos := &resolver.Repositories{
		Records:  db.Records,
		Actors:   db.Actors,
		Lexicons: db.Lexicons,
		Labels:   labels,
	}
	handler, err := hgraphql.NewHandler(registry, repos)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	schema := handler.Schema()
	queryCtx := resolver.WithRepositories(ctx, repos)

	// Include filter: only records with high-quality from the test labeler.
	query := `{
		records(collection: "app.bsky.feed.post", labels: ["high-quality"], labelerDids: ["did:plc:test-labeler"]) {
			edges { node { uri labels } }
		}
	}`
	result := executeQuery(schema, query, queryCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("graphql errors: %v", result.Errors)
	}
	data, _ := result.Data.(map[string]interface{})
	rec, _ := data["records"].(map[string]interface{})
	edges, _ := rec["edges"].([]interface{})
	if len(edges) != 1 {
		t.Fatalf("expected 1 record, got %d", len(edges))
	}
	node := edges[0].(map[string]interface{})["node"].(map[string]interface{})
	if got := node["uri"].(string); got != "at://did:plc:alice/app.bsky.feed.post/2" {
		t.Errorf("unexpected uri: %s", got)
	}
	gotLabels, _ := node["labels"].([]interface{})
	if len(gotLabels) != 1 || gotLabels[0].(string) != "high-quality" {
		t.Errorf("unexpected labels: %v", gotLabels)
	}
}

// ========== Public GraphQL + excludePds End-to-End ==========

// TestPublicGraphQL_ExcludePdsFilter verifies the full chain that the
// excludePds GraphQL arg depends on: actor.pds is read via the
// record→actor JOIN, the filter clause translates correctly to SQL,
// and the resulting node carries the joined pds string for clients.
// Also exercises the "actor row absent" pass-through case, which is
// the documented best-effort semantic.
func TestPublicGraphQL_ExcludePdsFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Two actors on different PDSes; carol intentionally has no actor
	// row so her records JOIN with NULL pds and pass through the
	// filter — the documented best-effort behaviour.
	if err := db.Actors.UpsertWithPDS(ctx, "did:plc:alice", "alice", "https://test.pds.example.com"); err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	if err := db.Actors.UpsertWithPDS(ctx, "did:plc:bob", "bob", "https://prod.pds.example.com"); err != nil {
		t.Fatalf("upsert bob: %v", err)
	}
	recs := []*repositories.Record{
		{URI: "at://did:plc:alice/app.bsky.feed.post/1", CID: "cidA1", DID: "did:plc:alice", Collection: "app.bsky.feed.post", JSON: `{"text":"a"}`, RKey: "1"},
		{URI: "at://did:plc:bob/app.bsky.feed.post/1", CID: "cidB1", DID: "did:plc:bob", Collection: "app.bsky.feed.post", JSON: `{"text":"b"}`, RKey: "1"},
		{URI: "at://did:plc:carol/app.bsky.feed.post/1", CID: "cidC1", DID: "did:plc:carol", Collection: "app.bsky.feed.post", JSON: `{"text":"c"}`, RKey: "1"},
	}
	if err := db.Records.BatchInsert(ctx, recs); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	registry := lexicon.NewRegistry()
	repos := &resolver.Repositories{
		Records:  db.Records,
		Actors:   db.Actors,
		Lexicons: db.Lexicons,
		Labels:   repositories.NewLabelsRepository(db.Executor),
	}
	handler, err := hgraphql.NewHandler(registry, repos)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	schema := handler.Schema()
	queryCtx := resolver.WithRepositories(ctx, repos)

	// 1. Without excludePds: all 3 records returned, alice's includes pds.
	query := `{
		records(collection: "app.bsky.feed.post") {
			edges { node { uri did pds } }
		}
	}`
	result := executeQuery(schema, query, queryCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("graphql errors: %v", result.Errors)
	}
	data := result.Data.(map[string]interface{})
	rec := data["records"].(map[string]interface{})
	edges := rec["edges"].([]interface{})
	if len(edges) != 3 {
		t.Fatalf("expected 3 records, got %d", len(edges))
	}
	pdsByDID := map[string]interface{}{}
	for _, e := range edges {
		n := e.(map[string]interface{})["node"].(map[string]interface{})
		pdsByDID[n["did"].(string)] = n["pds"]
	}
	if got, want := pdsByDID["did:plc:alice"], "https://test.pds.example.com"; got != want {
		t.Errorf("alice pds = %v, want %q", got, want)
	}
	if got, want := pdsByDID["did:plc:bob"], "https://prod.pds.example.com"; got != want {
		t.Errorf("bob pds = %v, want %q", got, want)
	}
	if got := pdsByDID["did:plc:carol"]; got != nil {
		t.Errorf("carol pds = %v, want nil (no actor row)", got)
	}

	// 2. excludePds drops alice; bob (prod) and carol (NULL) remain.
	query2 := `{
		records(collection: "app.bsky.feed.post", excludePds: ["https://test.pds.example.com"]) {
			edges { node { did pds } }
		}
	}`
	result = executeQuery(schema, query2, queryCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("graphql errors: %v", result.Errors)
	}
	data = result.Data.(map[string]interface{})
	edges = data["records"].(map[string]interface{})["edges"].([]interface{})
	gotDIDs := map[string]bool{}
	for _, e := range edges {
		n := e.(map[string]interface{})["node"].(map[string]interface{})
		gotDIDs[n["did"].(string)] = true
	}
	if len(gotDIDs) != 2 || gotDIDs["did:plc:alice"] {
		t.Errorf("excludePds[test]: expected {bob, carol}, got %v", gotDIDs)
	}
	if !gotDIDs["did:plc:bob"] || !gotDIDs["did:plc:carol"] {
		t.Errorf("excludePds[test]: missing expected DIDs, got %v", gotDIDs)
	}

	// 3. excludePds with both known PDSes: only carol (NULL pds) survives.
	query3 := `{
		records(collection: "app.bsky.feed.post", excludePds: [
			"https://test.pds.example.com",
			"https://prod.pds.example.com"
		]) {
			edges { node { did } }
		}
	}`
	result = executeQuery(schema, query3, queryCtx)
	if len(result.Errors) > 0 {
		t.Fatalf("graphql errors: %v", result.Errors)
	}
	data = result.Data.(map[string]interface{})
	edges = data["records"].(map[string]interface{})["edges"].([]interface{})
	if len(edges) != 1 {
		t.Fatalf("expected only carol, got %d records", len(edges))
	}
	carolNode := edges[0].(map[string]interface{})["node"].(map[string]interface{})
	if carolNode["did"].(string) != "did:plc:carol" {
		t.Errorf("expected carol, got %v", carolNode["did"])
	}
}
