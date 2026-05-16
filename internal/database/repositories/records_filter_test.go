package repositories_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

// seedFilterRecords creates a test DB with records from multiple authors
// across two collections for authors-filter testing.
func seedFilterRecords(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	type rec struct {
		uri, cid, did, collection string
	}
	records := []rec{
		{"at://did:plc:alice/col.a/r1", "cid1", "did:plc:alice", "col.a"},
		{"at://did:plc:alice/col.a/r2", "cid2", "did:plc:alice", "col.a"},
		{"at://did:plc:bob/col.a/r3", "cid3", "did:plc:bob", "col.a"},
		{"at://did:plc:carol/col.a/r4", "cid4", "did:plc:carol", "col.a"},
		{"at://did:plc:alice/col.b/r5", "cid5", "did:plc:alice", "col.b"},
		{"at://did:plc:bob/col.b/r6", "cid6", "did:plc:bob", "col.b"},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, r.cid, r.did, r.collection, `{"v":1}`); err != nil {
			t.Fatalf("insert %s: %v", r.uri, err)
		}
	}
	return db
}

func TestGetByCollectionFiltered_AuthorsNil_MatchesAll(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: nil}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("expected 4 records (all in col.a), got %d", len(got))
	}
}

func TestGetByCollectionFiltered_AuthorsEmpty_ReturnsEmpty(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 records for empty authors, got %d", len(got))
	}
}

func TestGetByCollectionFiltered_AuthorsSingleDID(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:bob"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].URI != "at://did:plc:bob/col.a/r3" {
		t.Errorf("expected bob's record, got %s", got[0].URI)
	}
}

func TestGetByCollectionFiltered_AuthorsMultipleDIDs(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:alice", "did:plc:carol"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 records (alice:2 + carol:1), got %d", len(got))
	}
}

func TestGetByCollectionFiltered_AuthorsAtCap(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	// Build a list of exactly MaxAuthorsFilterSize DIDs. Only alice is real.
	dids := make([]string, repositories.MaxAuthorsFilterSize)
	dids[0] = "did:plc:alice"
	for i := 1; i < len(dids); i++ {
		dids[i] = fmt.Sprintf("did:plc:filler%d", i)
	}
	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: dids}, nil, nil)
	if err != nil {
		t.Fatalf("expected no error at cap, got: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records (alice in col.a), got %d", len(got))
	}
}

func TestGetByCollectionFiltered_AuthorsExceedsCap(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	dids := make([]string, repositories.MaxAuthorsFilterSize+1)
	for i := range dids {
		dids[i] = fmt.Sprintf("did:plc:x%d", i)
	}
	_, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: dids}, nil, nil)
	if err == nil {
		t.Fatal("expected ErrAuthorsFilterTooLarge, got nil")
	}
	if !errors.Is(err, repositories.ErrAuthorsFilterTooLarge) {
		t.Errorf("expected ErrAuthorsFilterTooLarge, got: %v", err)
	}
}

func TestGetByCollectionFiltered_AuthorsDuplicates(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:bob", "did:plc:bob", "did:plc:bob"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 record (bob deduped), got %d", len(got))
	}
}

func TestGetByCollectionFiltered_AuthorsCollectionScoping(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	// Alice has records in both col.a and col.b. Querying col.b with
	// authors=alice should only return col.b records.
	got, err := db.Records.GetByCollectionFiltered(ctx, "col.b", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:alice"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record in col.b, got %d", len(got))
	}
	if got[0].URI != "at://did:plc:alice/col.b/r5" {
		t.Errorf("expected r5, got %s", got[0].URI)
	}
}

func TestGetByCollectionFiltered_KeysetPaginationStability(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	// Insert several records; they'll all get roughly the same indexed_at.
	authors := []string{"did:plc:pag1", "did:plc:pag2"}
	for i := 0; i < 10; i++ {
		did := authors[i%2]
		uri := fmt.Sprintf("at://%s/col.pag/r%02d", did, i)
		cid := fmt.Sprintf("cidpag%d", i)
		if _, err := db.Records.Insert(ctx, uri, cid, did, "col.pag", `{"v":1}`); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	filter := repositories.RecordFilter{Authors: authors}
	seen := map[string]bool{}

	// The real resolver encodes/decodes via base64 cursors using RFC3339.
	// For this direct-repository test, match the DB's native Postgres format.
	tsFormat := "2006-01-02T15:04:05.999999Z07:00"

	var afterTS, afterURI string
	pages := 0
	for {
		page, err := db.Records.GetByCollectionFiltered(ctx, "col.pag", 3, afterTS, afterURI, filter, nil, nil)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			if seen[r.URI] {
				t.Errorf("duplicate record across pages: %s", r.URI)
			}
			seen[r.URI] = true
		}
		last := page[len(page)-1]
		afterTS = last.IndexedAt.Format(tsFormat)
		afterURI = last.URI
		pages++
		if pages > 10 {
			t.Fatal("too many pages, likely infinite loop")
		}
	}
	if len(seen) != 10 {
		t.Errorf("expected 10 unique records across pages, got %d", len(seen))
	}
}

func TestGetByCollectionFiltered_AuthorsWithLabelInclude(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	// Add a label to alice's r1 only.
	if err := db.LabelDefinitions.Insert(
		ctx, "did:plc:lab1", "approved", "", repositories.SeverityInform, repositories.VisibilityShow,
	); err != nil {
		t.Fatalf("insert label def: %v", err)
	}
	if _, err := db.Labels.Insert(ctx, "did:plc:lab1",
		"at://did:plc:alice/col.a/r1", nil, "approved", nil, nil); err != nil {
		t.Fatalf("insert label: %v", err)
	}

	// Query with authors=[alice, bob] AND labels include=[approved].
	// Only alice's r1 has the label, so only r1 should be returned.
	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{
			Authors: []string{"did:plc:alice", "did:plc:bob"},
			Labels: repositories.LabelFilter{
				LabelerSrcs: []string{"did:plc:lab1"},
				Include:     []string{"approved"},
			},
		}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record (intersection), got %d", len(got))
	}
	if got[0].URI != "at://did:plc:alice/col.a/r1" {
		t.Errorf("expected r1, got %s", got[0].URI)
	}
}

func TestGetByCollectionFiltered_AuthorsWithLabelExclude(t *testing.T) {
	db := seedFilterRecords(t)
	ctx := context.Background()

	// Add a "spam" label to bob's r3.
	if err := db.LabelDefinitions.Insert(
		ctx, "did:plc:lab2", "spam", "", repositories.SeverityInform, repositories.VisibilityWarn,
	); err != nil {
		t.Fatalf("insert label def: %v", err)
	}
	if _, err := db.Labels.Insert(ctx, "did:plc:lab2",
		"at://did:plc:bob/col.a/r3", nil, "spam", nil, nil); err != nil {
		t.Fatalf("insert label: %v", err)
	}

	// Query with authors=[alice, bob] AND excludeLabels=[spam].
	// Bob's r3 should be excluded. Alice's r1, r2 remain.
	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{
			Authors: []string{"did:plc:alice", "did:plc:bob"},
			Labels: repositories.LabelFilter{
				LabelerSrcs: []string{"did:plc:lab2"},
				Exclude:     []string{"spam"},
			},
		}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records (alice's two), got %d", len(got))
	}
	for _, r := range got {
		if r.DID != "did:plc:alice" {
			t.Errorf("expected only alice's records, got DID=%s URI=%s", r.DID, r.URI)
		}
	}
}

// seedRecordsWithPDS sets up alice on a "test" PDS and bob on a "prod"
// PDS, both publishing into col.a, and returns the test DB. Used by the
// excludePds tests below to verify the JOIN-based filter.
func seedRecordsWithPDS(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	if err := db.Actors.UpsertWithPDS(ctx, "did:plc:alice", "alice", "https://test.pds.example.com"); err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	if err := db.Actors.UpsertWithPDS(ctx, "did:plc:bob", "bob", "https://prod.pds.example.com"); err != nil {
		t.Fatalf("upsert bob: %v", err)
	}
	// carol has no actor row at all — she still publishes records, and
	// those records should pass through the excludePds filter (NULL pds
	// is the "unknown / not yet resolved" sentinel that the filter
	// deliberately allows through).
	for _, r := range []struct{ uri, cid, did string }{
		{"at://did:plc:alice/col.a/r1", "cida1", "did:plc:alice"},
		{"at://did:plc:alice/col.a/r2", "cida2", "did:plc:alice"},
		{"at://did:plc:bob/col.a/r3", "cidb1", "did:plc:bob"},
		{"at://did:plc:carol/col.a/r4", "cidc1", "did:plc:carol"},
	} {
		if _, err := db.Records.Insert(ctx, r.uri, r.cid, r.did, "col.a", `{"v":1}`); err != nil {
			t.Fatalf("insert %s: %v", r.uri, err)
		}
	}
	return db
}

func TestGetByCollectionFiltered_PDSExclude_NilFilter(t *testing.T) {
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("expected 4 records (no filter), got %d", len(got))
	}
}

func TestGetByCollectionFiltered_PDSExclude_DropsTestPDS(t *testing.T) {
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{
			PDSExclude: []string{"https://test.pds.example.com"},
		}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// alice (test pds) → 2 records dropped. bob (prod) + carol (no
	// actor row, NULL pds) remain.
	gotDIDs := map[string]int{}
	for _, r := range got {
		gotDIDs[r.DID]++
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records (bob + carol), got %d: %+v", len(got), gotDIDs)
	}
	if gotDIDs["did:plc:alice"] != 0 {
		t.Errorf("alice should have been excluded; got %d records", gotDIDs["did:plc:alice"])
	}
	if gotDIDs["did:plc:bob"] != 1 || gotDIDs["did:plc:carol"] != 1 {
		t.Errorf("expected 1 bob + 1 carol, got %+v", gotDIDs)
	}
}

func TestGetByCollectionFiltered_PDSExclude_NullPDSPassesThrough(t *testing.T) {
	// Carol has no actor row — a.pds JOINs as NULL — which the filter
	// SQL `(a.pds IS NULL OR a.pds NOT IN (...))` deliberately lets
	// through. This is the documented best-effort semantic.
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{
			PDSExclude: []string{"https://test.pds.example.com", "https://prod.pds.example.com"},
		}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record (carol's NULL-pds record), got %d", len(got))
	}
	if got[0].DID != "did:plc:carol" {
		t.Errorf("expected carol's record, got %s", got[0].URI)
	}
}

func TestGetByCollectionFiltered_PDSExclude_MultipleEndpoints(t *testing.T) {
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{
			PDSExclude: []string{
				"https://test.pds.example.com",
				"https://other.pds.example.com",
			},
		}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// alice on test pds dropped; bob (prod) + carol (NULL) remain.
	if len(got) != 2 {
		t.Errorf("expected 2 records, got %d", len(got))
	}
	for _, r := range got {
		if r.DID == "did:plc:alice" {
			t.Errorf("alice should be excluded but appeared: %s", r.URI)
		}
	}
}

func TestGetByCollectionFiltered_PopulatesPDSOnRecord(t *testing.T) {
	// Records returned from any filtered query carry their author's PDS
	// via the JOIN. This is what populates the GraphQL `pds` field.
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:alice"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 alice records, got %d", len(got))
	}
	for _, r := range got {
		if r.PDS != "https://test.pds.example.com" {
			t.Errorf("expected PDS populated from JOIN, got %q for %s", r.PDS, r.URI)
		}
	}
}

func TestGetByCollectionFiltered_PDSEmptyForActorWithoutRow(t *testing.T) {
	// carol has records but no actor row → r.PDS should be "".
	db := seedRecordsWithPDS(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.a", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:carol"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 carol record, got %d", len(got))
	}
	if got[0].PDS != "" {
		t.Errorf("expected empty PDS for actor without row, got %q", got[0].PDS)
	}
}

func TestGetByCollectionFiltered_AuthorsCaseSensitive(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	if _, err := db.Records.Insert(ctx,
		"at://did:plc:abc/col.cs/r1", "cidcs1", "did:plc:abc", "col.cs", `{"v":1}`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:ABC/col.cs/r2", "cidcs2", "did:plc:ABC", "col.cs", `{"v":1}`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := db.Records.GetByCollectionFiltered(ctx, "col.cs", 100, "", "",
		repositories.RecordFilter{Authors: []string{"did:plc:abc"}}, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record (case-sensitive), got %d", len(got))
	}
	if got[0].DID != "did:plc:abc" {
		t.Errorf("expected did:plc:abc, got %s", got[0].DID)
	}
}

// seedContributorRecords seeds the activity collection with a mix of
// contributor identity shapes so the contributor-filter integration
// suite can assert each match/no-match case.
func seedContributorRecords(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	const col = "org.hypercerts.claim.activity"
	type rec struct {
		uri, did, body string
	}
	records := []rec{
		// Bare-string DID contributor (lexicon-compliant).
		{
			"at://did:plc:author1/" + col + "/r1", "did:plc:author1",
			`{"title":"r1","contributors":[{"contributorIdentity":"did:plc:alice"}]}`,
		},
		// Object-form DID contributor (production drift shape).
		{
			"at://did:plc:author2/" + col + "/r2", "did:plc:author2",
			`{"title":"r2","contributors":[{"contributorIdentity":{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"did:plc:bob"}}]}`,
		},
		// Mixed: one bare-string DID and one object DID in the same record.
		{
			"at://did:plc:author3/" + col + "/r3", "did:plc:author3",
			`{"title":"r3","contributors":[{"contributorIdentity":"did:plc:alice"},{"contributorIdentity":{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"did:plc:carol"}}]}`,
		},
		// Contributor is a handle — must NOT match a DID filter.
		{
			"at://did:plc:author4/" + col + "/r4", "did:plc:author4",
			`{"title":"r4","contributors":[{"contributorIdentity":"alice.example.com"}]}`,
		},
		// Empty contributors array.
		{
			"at://did:plc:author5/" + col + "/r5", "did:plc:author5",
			`{"title":"r5","contributors":[]}`,
		},
		// Missing contributors field entirely.
		{
			"at://did:plc:author6/" + col + "/r6", "did:plc:author6",
			`{"title":"r6"}`,
		},
		// Object without .identity field (e.g. a strong-ref) — must NOT match.
		{
			"at://did:plc:author7/" + col + "/r7", "did:plc:author7",
			`{"title":"r7","contributors":[{"contributorIdentity":{"$type":"com.atproto.repo.strongRef","uri":"at://example","cid":"bafy"}}]}`,
		},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, "cid"+r.uri, r.did, col, r.body); err != nil {
			t.Fatalf("insert %s: %v", r.uri, err)
		}
	}
	return db
}

func contributorFilterGroup(op repositories.FilterOperator, value interface{}) *repositories.FilterGroup {
	return &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Filters: []repositories.FieldFilter{{
			FieldName: "contributors",
			Operator:  op,
			Value:     value,
			IsJSON:    true,
			Kind:      repositories.KindArrayContributor,
		}},
	}
}

func TestGetByCollectionFiltered_Contributor_Eq_BareString(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// did:plc:alice appears in r1 (bare) and r3 (mixed) — both must match.
	wantURIs := map[string]bool{
		"at://did:plc:author1/org.hypercerts.claim.activity/r1": false,
		"at://did:plc:author3/org.hypercerts.claim.activity/r3": false,
	}
	for _, rec := range got {
		if _, ok := wantURIs[rec.URI]; ok {
			wantURIs[rec.URI] = true
		} else {
			t.Errorf("unexpected URI in results: %s", rec.URI)
		}
	}
	for uri, seen := range wantURIs {
		if !seen {
			t.Errorf("expected URI not found: %s", uri)
		}
	}
}

func TestGetByCollectionFiltered_Contributor_Eq_ObjectShape(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:bob")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].URI != "at://did:plc:author2/org.hypercerts.claim.activity/r2" {
		t.Errorf("got %d records, want 1 (r2); got: %+v", len(got), got)
	}
}

func TestGetByCollectionFiltered_Contributor_In(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	fg := contributorFilterGroup(repositories.OpIn, []string{"did:plc:bob", "did:plc:carol"})
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// did:plc:bob → r2 (object); did:plc:carol → r3 (mixed).
	got2 := map[string]bool{}
	for _, rec := range got {
		got2[rec.URI] = true
	}
	for _, want := range []string{
		"at://did:plc:author2/org.hypercerts.claim.activity/r2",
		"at://did:plc:author3/org.hypercerts.claim.activity/r3",
	} {
		if !got2[want] {
			t.Errorf("missing expected URI: %s", want)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d records, want 2", len(got))
	}
}

func TestGetByCollectionFiltered_Contributor_HandleEntryDoesNotMatch(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	// A consumer trying to filter by the handle string would not get here
	// (the GraphQL layer rejects non-DIDs), but if a DID is queried, the
	// handle-shaped record (r4) must not match.
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:nonexistent")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 records, got %d (%+v)", len(got), got)
	}
}

func TestGetByCollectionFiltered_Contributor_AbsentAndEmpty(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	// Records r5 (empty array) and r6 (missing field) must be filtered out
	// for any DID query — but the query itself must NOT error.
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range got {
		if rec.URI == "at://did:plc:author5/org.hypercerts.claim.activity/r5" ||
			rec.URI == "at://did:plc:author6/org.hypercerts.claim.activity/r6" {
			t.Errorf("absent/empty record should not match: %s", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_Contributor_ObjectWithoutIdentity(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	// r7 has a strong-ref-like object without .identity — must not match.
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range got {
		if rec.URI == "at://did:plc:author7/org.hypercerts.claim.activity/r7" {
			t.Errorf("strong-ref-like record should not match: %s", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_Contributor_NonArrayContributorsDoesNotError(t *testing.T) {
	// Defensive: a record whose `contributors` field is a string (or any
	// non-array) would otherwise make jsonb_array_elements raise and
	// brick every query touching this filter. The jsonb_typeof guard in
	// the SQL must short-circuit before that happens.
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:weird/"+col+"/badShape", "cidbad", "did:plc:weird", col,
		`{"title":"weird","contributors":"not-an-array"}`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Add one legitimate matching record too, so we can confirm the
	// query returns the good one rather than erroring on the bad one.
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:good/"+col+"/r1", "cidgood", "did:plc:good", col,
		`{"contributors":[{"contributorIdentity":"did:plc:alice"}]}`); err != nil {
		t.Fatalf("insert good: %v", err)
	}
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query errored on non-array contributors: %v", err)
	}
	if len(got) != 1 || got[0].URI != "at://did:plc:good/"+col+"/r1" {
		t.Errorf("expected the well-shaped record only, got %d records", len(got))
	}
}

func TestGetByCollectionFiltered_Contributor_ComposeWithDID_OR(t *testing.T) {
	db := seedContributorRecords(t)
	ctx := context.Background()
	// "Authored OR contributed" as a single query using _or.
	fg := &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Children: []repositories.FilterGroup{{
			Operator: repositories.GroupOR,
			Filters: []repositories.FieldFilter{
				{FieldName: "did", Operator: repositories.OpEq, Value: "did:plc:author2", IsJSON: false},
				{FieldName: "contributors", Operator: repositories.OpEq, Value: "did:plc:alice", IsJSON: true, Kind: repositories.KindArrayContributor},
			},
		}},
	}
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	want := map[string]bool{
		"at://did:plc:author2/org.hypercerts.claim.activity/r2": false,
		"at://did:plc:author1/org.hypercerts.claim.activity/r1": false,
		"at://did:plc:author3/org.hypercerts.claim.activity/r3": false,
	}
	for _, rec := range got {
		if _, ok := want[rec.URI]; ok {
			want[rec.URI] = true
		}
	}
	for uri, seen := range want {
		if !seen {
			t.Errorf("missing expected URI: %s", uri)
		}
	}
}

func TestGetByCollectionFiltered_Contributor_LargeArrayInvisible(t *testing.T) {
	// A record with more than MaxArrayContributorScan contributors becomes
	// invisible to the filter (fail-safe). Build a record at the boundary
	// (201 contributors) and confirm it does not match.
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	var contribs []string
	contribs = append(contribs, `{"contributorIdentity":"did:plc:target"}`)
	for i := 0; i < repositories.MaxArrayContributorScan; i++ {
		contribs = append(contribs, fmt.Sprintf(`{"contributorIdentity":"did:plc:filler%d"}`, i))
	}
	body := `{"contributors":[` + strings.Join(contribs, ",") + `]}`
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:huge/"+col+"/r1", "cidhuge", "did:plc:huge", col, body); err != nil {
		t.Fatalf("insert: %v", err)
	}
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:target")
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("oversized-contributors record should be invisible to filter, got %d records", len(got))
	}
}

func TestGetByCollectionFiltered_Contributor_ExclusivelyEnforcesGuards_OK(t *testing.T) {
	// Boundary: a record at exactly MaxArrayContributorScan contributors
	// IS visible (the bound is inclusive).
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	var contribs []string
	contribs = append(contribs, `{"contributorIdentity":"did:plc:target"}`)
	for i := 0; i < repositories.MaxArrayContributorScan-1; i++ {
		contribs = append(contribs, fmt.Sprintf(`{"contributorIdentity":"did:plc:filler%d"}`, i))
	}
	body := `{"contributors":[` + strings.Join(contribs, ",") + `]}`
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:edge/"+col+"/r1", "cidedge", "did:plc:edge", col, body); err != nil {
		t.Fatalf("insert: %v", err)
	}
	fg := contributorFilterGroup(repositories.OpEq, "did:plc:target")
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 record at array-size boundary, got %d", len(got))
	}
}

func TestGetByCollectionFiltered_Contributor_PaginationKeyset(t *testing.T) {
	// Acceptance criterion E: keyset cursor advances correctly under
	// the contributor filter. Seed 5 records all matching the same
	// contributor DID; page with limit=2 and verify each page returns
	// the expected slice in expected order, with the final page
	// returning the tail.
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	// Five matching records authored by distinct DIDs so URIs sort
	// stably. indexed_at is set by the DB at insert time; the
	// loop's insert order determines the DESC ordering.
	uris := make([]string, 5)
	for i := 0; i < 5; i++ {
		uri := fmt.Sprintf("at://did:plc:author%d/%s/r%d", i, col, i)
		uris[i] = uri
		body := `{"contributors":[{"contributorIdentity":"did:plc:target"}]}`
		if _, err := db.Records.Insert(ctx, uri, fmt.Sprintf("cid%d", i),
			fmt.Sprintf("did:plc:author%d", i), col, body); err != nil {
			t.Fatalf("insert %s: %v", uri, err)
		}
	}

	fg := contributorFilterGroup(repositories.OpEq, "did:plc:target")
	// Default sort is indexed_at DESC; format matches the keyset
	// timestamp Postgres returns.
	tsFormat := "2006-01-02T15:04:05.999999Z07:00"

	// Page 1: newest two.
	page1, err := db.Records.GetByCollectionFiltered(ctx, col, 2, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 expected 2 records, got %d", len(page1))
	}
	advance := func(page []*repositories.Record) (string, string) {
		last := page[len(page)-1]
		return last.IndexedAt.Format(tsFormat), last.URI
	}
	// Page 2: next two.
	afterTS, afterURI := advance(page1)
	page2, err := db.Records.GetByCollectionFiltered(ctx, col, 2, afterTS, afterURI,
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2 expected 2 records, got %d", len(page2))
	}
	// Page 3: the tail.
	afterTS, afterURI = advance(page2)
	page3, err := db.Records.GetByCollectionFiltered(ctx, col, 2, afterTS, afterURI,
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("page 3 expected 1 record (tail), got %d", len(page3))
	}
	// Page 4: empty.
	afterTS, afterURI = advance(page3)
	page4, err := db.Records.GetByCollectionFiltered(ctx, col, 2, afterTS, afterURI,
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("page 4: %v", err)
	}
	if len(page4) != 0 {
		t.Errorf("page 4 expected 0 records (past tail), got %d", len(page4))
	}
	// No URI appears twice across pages.
	seen := map[string]bool{}
	for _, page := range [][]*repositories.Record{page1, page2, page3} {
		for _, rec := range page {
			if seen[rec.URI] {
				t.Errorf("URI %s appeared on more than one page", rec.URI)
			}
			seen[rec.URI] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("expected 5 unique URIs across pages, got %d", len(seen))
	}
}

func TestGetByCollectionFiltered_Contributor_ComposeWithExcludePds(t *testing.T) {
	// Both filters AND together: a record matches only if its
	// contributor is `did:plc:target` AND its author's PDS is not in
	// the exclude list.
	db := seedContributorRecords(t)
	ctx := context.Background()

	// UpsertWithPDS creates the actor row in the same call that sets
	// the PDS. Records.Insert does NOT create an actor row, so a bare
	// SetPDS (pure UPDATE) would silently no-op against the missing
	// row and the filter would have nothing to exclude.
	if err := db.Actors.UpsertWithPDS(ctx, "did:plc:author1", "", "https://blocked.example"); err != nil {
		t.Fatalf("upsert pds: %v", err)
	}

	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.claim.activity",
		100, "", "",
		repositories.RecordFilter{PDSExclude: []string{"https://blocked.example"}},
		nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// did:plc:alice was a contributor on r1 (author1, now excluded)
	// and r3 (author3, NULL pds → passes through). Result should be
	// just r3.
	if len(got) != 1 || got[0].URI != "at://did:plc:author3/org.hypercerts.claim.activity/r3" {
		t.Errorf("expected only r3 after excludePds, got %d: %+v", len(got), got)
	}
}

func TestGetByCollectionFiltered_Contributor_StoredDIDWithWhitespace(t *testing.T) {
	// Symmetric policy with the extractor: a stored DID with stray
	// whitespace is data-quality noise, not a match. The SQL matches
	// bytes exactly so `"  did:plc:alice  "` (with padding) does not
	// match a filter for `"did:plc:alice"`. Operators detect this via
	// the contributor_identity_total{outcome="non_did"} counter.
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	if _, err := db.Records.Insert(ctx,
		"at://did:plc:padded/"+col+"/r1", "cidpadded", "did:plc:padded", col,
		`{"contributors":[{"contributorIdentity":"  did:plc:alice  "}]}`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:clean/"+col+"/r1", "cidclean", "did:plc:clean", col,
		`{"contributors":[{"contributorIdentity":"did:plc:alice"}]}`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	fg := contributorFilterGroup(repositories.OpEq, "did:plc:alice")
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].URI != "at://did:plc:clean/"+col+"/r1" {
		t.Errorf("expected only the unpadded record to match, got %d: %+v", len(got), got)
	}
}

// --- T-COV-3: Postgres-backed shape tests for contributor + badge-award
// subject filters. ---
//
// The 2026-05-13 audit found that filter SQL was being tested at the
// substring level only — PR #75 shipped a `subject` filter whose SQL
// didn't match 70% of real records (the `defs#did` object variant)
// and the substring tests still passed. These tests pin the filter
// behaviour against every shape observed in production JSON. If the
// SQL drifts to miss a shape the assertion fires; if a new shape
// lands in production, this is the file that grows.
//
// Each test seeds records covering the full real-world shape matrix,
// runs the filter, and asserts the URI set. Shape-by-shape
// expectations avoid "I matched 1 of 3 — which?" ambiguity that an
// integer count would let through.

// seedBadgeAwardSubjectShapes inserts three badge.award records,
// each carrying a different `subject` shape. The shape strings were
// extracted from production data + the upstream
// app.certified.defs lexicon. Don't simplify these without
// consulting the lexicon — the SQL has been broken before by
// shape-set assumptions that didn't survive a production audit.
//
// Shapes:
//
//	(a) bare string at-uri              — defensive; no real records observed
//	(b) strongRef object {uri, cid}     — historical shape pre-defs#did
//	(c) defs#did object                 — current production shape
//	                                       (PR #75 fix)
func seedBadgeAwardSubjectShapes(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	const col = "app.certified.badge.award"
	records := []struct {
		uri, did, body string
	}{
		// (a) Bare string at-uri. The subject_did generated column
		// splits the DID out of the at-uri (split_part on the
		// component after "at://").
		{
			uri:  "at://did:plc:author1/" + col + "/r1",
			did:  "did:plc:author1",
			body: `{"subject":"at://did:plc:a/app.certified.badge.def/skill","val":"endorsed"}`,
		},
		// (b) strongRef object — generated column reads .uri and
		// splits out the DID.
		{
			uri: "at://did:plc:author2/" + col + "/r2",
			did: "did:plc:author2",
			body: `{"subject":{"$type":"com.atproto.repo.strongRef",` +
				`"uri":"at://did:plc:b/app.certified.badge.def/skill","cid":"bafyref"},` +
				`"val":"endorsed"}`,
		},
		// (c) defs#did object — generated column reads .did directly.
		// This is the shape PR #75 had to add support for.
		{
			uri: "at://did:plc:author3/" + col + "/r3",
			did: "did:plc:author3",
			body: `{"subject":{"$type":"app.certified.defs#did","did":"did:plc:c"},` +
				`"val":"endorsed"}`,
		},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, "cid"+r.uri, r.did, col, r.body); err != nil {
			t.Fatalf("seed %s: %v", r.uri, err)
		}
	}
	return db
}

// subjectFilterGroup wraps the BadgeAward subject filter in the
// FilterGroup shape `GetByCollectionFiltered` expects. Mirror of
// contributorFilterGroup but with Kind=KindUnionSubject.
func subjectFilterGroup(op repositories.FilterOperator, value interface{}) *repositories.FilterGroup {
	return &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Filters: []repositories.FieldFilter{{
			FieldName: "subject",
			Operator:  op,
			Value:     value,
			IsJSON:    true,
			Kind:      repositories.KindUnionSubject,
		}},
	}
}

// TestBadgeAwardSubject_ProductionShapes pins the subject filter
// behaviour against each of the three real shapes. The SQL targets
// the generated column `subject_did` (migration 025); this verifies
// the column expression actually materialises the right DID for
// each shape AND that the filter SQL queries it correctly.
func TestBadgeAwardSubject_ProductionShapes(t *testing.T) {
	db := seedBadgeAwardSubjectShapes(t)
	ctx := context.Background()
	const col = "app.certified.badge.award"

	type shape struct {
		name   string
		did    string // candidate filter DID
		wantOK string // expected matching record URI
	}
	cases := []shape{
		{"bare-string", "did:plc:a", "at://did:plc:author1/" + col + "/r1"},
		{"strongRef", "did:plc:b", "at://did:plc:author2/" + col + "/r2"},
		{"defs#did", "did:plc:c", "at://did:plc:author3/" + col + "/r3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fg := subjectFilterGroup(repositories.OpEq, c.did)
			got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
				repositories.RecordFilter{}, nil, fg)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("subject=%s: got %d records, want exactly 1 (%s)",
					c.did, len(got), c.wantOK)
			}
			if got[0].URI != c.wantOK {
				t.Errorf("subject=%s: got URI %s, want %s",
					c.did, got[0].URI, c.wantOK)
			}
		})
	}
}

// TestBadgeAwardSubject_InMatchesAcrossShapes verifies the `in`
// operator picks up rows of different subject shapes in a single
// query. (a) bare-string and (c) defs#did, mixed — must yield
// exactly those two records, NOT also (b) strongRef.
func TestBadgeAwardSubject_InMatchesAcrossShapes(t *testing.T) {
	db := seedBadgeAwardSubjectShapes(t)
	ctx := context.Background()
	const col = "app.certified.badge.award"

	fg := subjectFilterGroup(repositories.OpIn, []string{"did:plc:a", "did:plc:c"})
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	gotURIs := uriSet(got)
	want := map[string]bool{
		"at://did:plc:author1/" + col + "/r1": true,
		"at://did:plc:author3/" + col + "/r3": true,
	}
	for uri := range want {
		if !gotURIs[uri] {
			t.Errorf("missing expected URI: %s", uri)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d records, want 2: %+v", len(got), gotURIs)
	}
}

// TestBadgeAwardSubject_GeneratedColumnPopulation reads the
// generated column directly (no filter SQL involved) and confirms
// it extracts the correct DID for each shape. If this fires, the
// migration 025 generated-column expression has drifted from the
// shape matrix. Acts as a low-level guard so a column regression
// shows up even when the filter SQL has been re-routed.
func TestBadgeAwardSubject_GeneratedColumnPopulation(t *testing.T) {
	db := seedBadgeAwardSubjectShapes(t)
	ctx := context.Background()
	const col = "app.certified.badge.award"

	// Pull subject_did for each seeded URI via raw SQL — the
	// repository interface doesn't expose this column to Go code
	// (it's a Postgres-internal optimisation surface) so the
	// underlying *sql.DB is the right escape hatch here.
	rows, err := db.Records.DB().QueryContext(ctx,
		`SELECT uri, subject_did FROM record WHERE collection = $1 ORDER BY uri`,
		col)
	if err != nil {
		t.Fatalf("query subject_did: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var uri string
		var subjectDID *string
		if err := rows.Scan(&uri, &subjectDID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if subjectDID == nil {
			got[uri] = ""
		} else {
			got[uri] = *subjectDID
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	want := map[string]string{
		"at://did:plc:author1/" + col + "/r1": "did:plc:a",
		"at://did:plc:author2/" + col + "/r2": "did:plc:b",
		"at://did:plc:author3/" + col + "/r3": "did:plc:c",
	}
	for uri, wantDID := range want {
		if got[uri] != wantDID {
			t.Errorf("subject_did for %s = %q, want %q", uri, got[uri], wantDID)
		}
	}
}

// TestBadgeAwardSubject_NonMatchingShapesReturnNull guards the
// generated column's NULL branch — a malformed `subject` (number,
// bool, array, missing field) must leave subject_did NULL so the
// partial-index WHERE clause keeps it out of the filter query path.
// Without this, every malformed record would be a full-row scan.
func TestBadgeAwardSubject_NonMatchingShapesReturnNull(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "app.certified.badge.award"

	// Insert one malformed record per shape we want to guard
	// against. Each must produce subject_did = NULL.
	malformed := []struct {
		uri, did, body string
	}{
		{"at://did:plc:m1/" + col + "/r1", "did:plc:m1", `{"subject":42,"val":"x"}`},
		{"at://did:plc:m2/" + col + "/r2", "did:plc:m2", `{"subject":true,"val":"x"}`},
		{"at://did:plc:m3/" + col + "/r3", "did:plc:m3", `{"subject":["nope"],"val":"x"}`},
		{"at://did:plc:m4/" + col + "/r4", "did:plc:m4", `{"val":"x"}`},
		// object without .did and without recognisable .uri.
		{"at://did:plc:m5/" + col + "/r5", "did:plc:m5", `{"subject":{"unrelated":"foo"},"val":"x"}`},
		// string subject without the "at://" prefix.
		{"at://did:plc:m6/" + col + "/r6", "did:plc:m6", `{"subject":"plain-text","val":"x"}`},
	}
	for _, m := range malformed {
		if _, err := db.Records.Insert(ctx, m.uri, "cid"+m.uri, m.did, col, m.body); err != nil {
			t.Fatalf("seed %s: %v", m.uri, err)
		}
	}
	// A filter for ANY DID must not match any of these — they all
	// hit the NULL branch of the generated column.
	for _, target := range []string{"did:plc:a", "did:plc:m1", "did:plc:foo"} {
		fg := subjectFilterGroup(repositories.OpEq, target)
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query subject=%s: %v", target, err)
		}
		if len(got) != 0 {
			t.Errorf("malformed-subject records matched subject=%s: %+v", target, got)
		}
	}
}

// TestContributorFilter_ProductionShapes pins the contributor filter
// against the three shapes observed in real
// org.hypercerts.claim.activity records: bare-string DID, object-form
// DID, and a mixed-shape entry. Each shape is one record so the
// assertion fires shape-by-shape, not "1 of 3 missing — which?".
//
// Overlaps with the existing per-shape tests above
// (TestGetByCollectionFiltered_Contributor_*), but as a single
// table-driven test it acts as the canonical contributor-shape
// regression guard for the audit's T-COV-3.
func TestContributorFilter_ProductionShapes(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	// (a) bare-string DID
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:author1/"+col+"/r1", "cid1", "did:plc:author1", col,
		`{"contributors":[{"contributorIdentity":"did:plc:a"}]}`); err != nil {
		t.Fatalf("seed r1: %v", err)
	}
	// (b) object-form DID
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:author2/"+col+"/r2", "cid2", "did:plc:author2", col,
		`{"contributors":[{"contributorIdentity":{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"did:plc:b"}}]}`); err != nil {
		t.Fatalf("seed r2: %v", err)
	}
	// (c) two contributors, mixed shapes — bare-string + object form
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:author3/"+col+"/r3", "cid3", "did:plc:author3", col,
		`{"contributors":[`+
			`{"contributorIdentity":"did:plc:a"},`+
			`{"contributorIdentity":{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"did:plc:c"}}`+
			`]}`); err != nil {
		t.Fatalf("seed r3: %v", err)
	}

	// eq=did:plc:a → r1 (bare) + r3 (mixed includes bare did:plc:a).
	t.Run("eq-bare", func(t *testing.T) {
		fg := contributorFilterGroup(repositories.OpEq, "did:plc:a")
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		gotURIs := uriSet(got)
		want := map[string]bool{
			"at://did:plc:author1/" + col + "/r1": true,
			"at://did:plc:author3/" + col + "/r3": true,
		}
		assertURISet(t, "eq=did:plc:a", gotURIs, want)
	})

	// eq=did:plc:b → only r2 (object).
	t.Run("eq-object", func(t *testing.T) {
		fg := contributorFilterGroup(repositories.OpEq, "did:plc:b")
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		gotURIs := uriSet(got)
		want := map[string]bool{
			"at://did:plc:author2/" + col + "/r2": true,
		}
		assertURISet(t, "eq=did:plc:b", gotURIs, want)
	})

	// in=[did:plc:a, did:plc:b] → r1 (a) + r2 (b) + r3 (has a).
	t.Run("in-bare-and-object", func(t *testing.T) {
		fg := contributorFilterGroup(repositories.OpIn, []string{"did:plc:a", "did:plc:b"})
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		gotURIs := uriSet(got)
		want := map[string]bool{
			"at://did:plc:author1/" + col + "/r1": true,
			"at://did:plc:author2/" + col + "/r2": true,
			"at://did:plc:author3/" + col + "/r3": true,
		}
		assertURISet(t, "in=[did:plc:a, did:plc:b]", gotURIs, want)
	})

	// in=[did:plc:c] → only r3 (which contains an object-form
	// contributor for did:plc:c as its second array entry — proving
	// the filter scans the full array, not just the first element).
	t.Run("in-deep-array-entry", func(t *testing.T) {
		fg := contributorFilterGroup(repositories.OpIn, []string{"did:plc:c"})
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		gotURIs := uriSet(got)
		want := map[string]bool{
			"at://did:plc:author3/" + col + "/r3": true,
		}
		assertURISet(t, "in=[did:plc:c]", gotURIs, want)
	})
}

// uriSet collects record URIs into a set for stable comparison.
// Order isn't part of the filter contract for these tests; a URI set
// is the right granularity. Lives below the tests that use it
// because it's local scaffolding, not a public helper.
func uriSet(records []*repositories.Record) map[string]bool {
	out := map[string]bool{}
	for _, r := range records {
		out[r.URI] = true
	}
	return out
}

// assertURISet reports any extra or missing URIs in `got` relative
// to `want`. Both arguments are sets; ordering is unconstrained.
func assertURISet(t *testing.T, name string, got, want map[string]bool) {
	t.Helper()
	for uri := range want {
		if !got[uri] {
			t.Errorf("%s: missing expected URI %s", name, uri)
		}
	}
	for uri := range got {
		if !want[uri] {
			t.Errorf("%s: unexpected URI %s", name, uri)
		}
	}
}

// TestContributorFilter_NonArrayGuard verifies the partial-index
// WHERE clause + the IMMUTABLE wrapper function keep records out
// when `contributors` is a string (not an array). Without this guard
// the SQL function `record_contributor_identities` would raise on
// `jsonb_array_elements(<string>)` and brick every filter query.
//
// Overlaps with the existing
// TestGetByCollectionFiltered_Contributor_NonArrayContributorsDoesNotError
// but asserts the audit-specific guarantee: a malformed record is
// invisible to a filter for ANY candidate DID, not just absent from
// the result set when a well-formed record is present.
func TestContributorFilter_NonArrayGuard(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	// One malformed record; no well-formed records — so any match
	// would have to come from the bad row, which would be the bug.
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:bad/"+col+"/r1", "cidbad", "did:plc:bad", col,
		`{"contributors":"not-an-array"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	candidates := []string{
		"did:plc:a", "did:plc:b", "did:plc:not-an-array", "not-an-array",
	}
	for _, c := range candidates {
		fg := contributorFilterGroup(repositories.OpEq, c)
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query candidate=%s: %v", c, err)
		}
		if len(got) != 0 {
			t.Errorf("candidate %q matched malformed record: %+v", c, got)
		}
	}
}

// TestContributorFilter_OverlongArrayGuard verifies the
// MaxArrayContributorScan cap (200): a record with > 200 entries
// becomes invisible to the filter. The cap mirrors notifications'
// MaxContributorsBeforeReject so a record the notifications layer
// drops also fails to match.
//
// Overlaps with the existing
// TestGetByCollectionFiltered_Contributor_LargeArrayInvisible — kept
// separate because the audit explicitly calls out the cap behaviour
// as the documented expectation, and the existing test only
// exercises the immediately-past-cap boundary. This one also
// confirms invisibility holds for multiple candidate DIDs at once
// (in-filter shape).
func TestContributorFilter_OverlongArrayGuard(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.claim.activity"

	// Build a record with MaxArrayContributorScan + 1 entries, the
	// first being a DID we'll search for.
	contribs := make([]string, 0, repositories.MaxArrayContributorScan+1)
	contribs = append(contribs, `{"contributorIdentity":"did:plc:target"}`)
	for i := 0; i < repositories.MaxArrayContributorScan; i++ {
		contribs = append(contribs, fmt.Sprintf(`{"contributorIdentity":"did:plc:filler%d"}`, i))
	}
	body := `{"contributors":[` + strings.Join(contribs, ",") + `]}`
	if _, err := db.Records.Insert(ctx,
		"at://did:plc:over/"+col+"/r1", "cidover", "did:plc:over", col, body); err != nil {
		t.Fatalf("seed: %v", err)
	}

	candidates := []string{"did:plc:target", "did:plc:filler0", "did:plc:filler199"}
	for _, c := range candidates {
		fg := contributorFilterGroup(repositories.OpEq, c)
		got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
			repositories.RecordFilter{}, nil, fg)
		if err != nil {
			t.Fatalf("query candidate=%s: %v", c, err)
		}
		if len(got) != 0 {
			t.Errorf("candidate %q matched overlong-array record: %+v", c, got)
		}
	}
	// in-filter shape: also invisible.
	fg := contributorFilterGroup(repositories.OpIn, candidates)
	got, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("in-query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("in-filter matched overlong-array record: %+v", got)
	}
}

// -----------------------------------------------------------------------------
// Case-insensitive operators: eqi / ini (Postgres-backed integration)
// -----------------------------------------------------------------------------

// seedCaseInsensitiveRecords inserts a mixed-casing fixture for the
// "all projects of a user" view exercised by the certified-app.
// The collection NSID matches the live deployment's
// `org.hypercerts.collection` lexicon (the testdata lexicon at
// `org.hypercerts.claim.collection` is documented as stale in issue
// #68); the JSON shape is the same regardless.
func seedCaseInsensitiveRecords(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	type rec struct {
		uri, did, body string
	}
	const col = "org.hypercerts.collection"
	records := []rec{
		// Same author, different casings — all should match `eqi:"project"`.
		{"at://did:plc:alice/" + col + "/r1", "did:plc:alice", `{"type":"project","title":"P1"}`},
		{"at://did:plc:alice/" + col + "/r2", "did:plc:alice", `{"type":"Project","title":"P2"}`},
		{"at://did:plc:alice/" + col + "/r3", "did:plc:alice", `{"type":"PROJECT","title":"P3"}`},
		{"at://did:plc:alice/" + col + "/r4", "did:plc:alice", `{"type":"PrOjEcT","title":"P4"}`},
		// Same author, different type — NOT a project.
		{"at://did:plc:alice/" + col + "/r5", "did:plc:alice", `{"type":"projects","title":"NotAProject"}`},
		{"at://did:plc:alice/" + col + "/r6", "did:plc:alice", `{"type":"favorites","title":"Favs"}`},
		{"at://did:plc:alice/" + col + "/r7", "did:plc:alice", `{"type":"FAVORITES","title":"FavsUpper"}`},
		// Whitespace producer drift — eqi must NOT trim.
		{"at://did:plc:alice/" + col + "/r8", "did:plc:alice", `{"type":" project ","title":"WhitespaceDrift"}`},
		// Empty-string type — distinct from absent type.
		{"at://did:plc:alice/" + col + "/r9", "did:plc:alice", `{"type":"","title":"EmptyType"}`},
		// Non-ASCII look-alike: Cyrillic 'р' (U+0440), not Latin 'p'.
		{"at://did:plc:alice/" + col + "/r10", "did:plc:alice", `{"type":"р","title":"Cyrillic"}`},
		// Different author with a Project — for did-scoping tests.
		{"at://did:plc:bob/" + col + "/r11", "did:plc:bob", `{"type":"Project","title":"BobProject"}`},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, "cid"+r.uri, r.did, col, r.body); err != nil {
			t.Fatalf("insert %s: %v", r.uri, err)
		}
	}
	return db
}

// typeFilterGroup wraps a single FieldFilter on `type` in a FilterGroup,
// reusing the shape extractFieldFiltersRecursive emits at runtime.
func typeFilterGroup(op repositories.FilterOperator, value interface{}) *repositories.FilterGroup {
	return &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Filters: []repositories.FieldFilter{{
			FieldName: "type",
			Operator:  op,
			Value:     value,
			IsJSON:    true,
		}},
	}
}

func TestGetByCollectionFiltered_Eqi_MatchesAllCasings(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpEqi, "project")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// r1..r4 (Alice's four casings) + r11 (Bob's Project) match; r5 ("projects"),
	// r6/r7 (favorites), r8 (whitespace drift), r9 (empty), r10 (Cyrillic) do not.
	wantURIs := map[string]bool{
		"at://did:plc:alice/org.hypercerts.collection/r1": false,
		"at://did:plc:alice/org.hypercerts.collection/r2": false,
		"at://did:plc:alice/org.hypercerts.collection/r3": false,
		"at://did:plc:alice/org.hypercerts.collection/r4": false,
		"at://did:plc:bob/org.hypercerts.collection/r11":  false,
	}
	if len(got) != len(wantURIs) {
		t.Fatalf("got %d records, want %d (uris: %v)", len(got), len(wantURIs), uriList(got))
	}
	for _, rec := range got {
		if _, ok := wantURIs[rec.URI]; !ok {
			t.Errorf("unexpected match: %s", rec.URI)
			continue
		}
		wantURIs[rec.URI] = true
	}
	for uri, seen := range wantURIs {
		if !seen {
			t.Errorf("expected match missing: %s", uri)
		}
	}
}

func TestGetByCollectionFiltered_Eqi_AndComposition_ProjectsOfUser(t *testing.T) {
	// The certified-app's actual use case: "all projects of a user."
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	// did { eq } AND type { eqi }
	fg := &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Filters: []repositories.FieldFilter{
			{FieldName: "did", Operator: repositories.OpEq, Value: "did:plc:alice", IsJSON: false},
			{FieldName: "type", Operator: repositories.OpEqi, Value: "project", IsJSON: true},
		},
	}
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("expected 4 of Alice's projects, got %d", len(got))
	}
	for _, rec := range got {
		if rec.DID != "did:plc:alice" {
			t.Errorf("expected did:plc:alice, got %s", rec.DID)
		}
	}
}

func TestGetByCollectionFiltered_Eqi_DoesNotTrim(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	// eqi:"project" must NOT match the " project " whitespace-drift
	// record (r8). The test in `MatchesAllCasings` covered the
	// positive matches; this pins the negative side explicitly.
	fg := typeFilterGroup(repositories.OpEqi, "project")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range got {
		if strings.HasSuffix(rec.URI, "/r8") {
			t.Errorf("eqi unexpectedly matched whitespace-drift record %s", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_Eqi_EmptyStringMatchesOnlyEmpty(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpEqi, "")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 empty-type record, got %d (%+v)", len(got), got)
	}
	if !strings.HasSuffix(got[0].URI, "/r9") {
		t.Errorf("expected r9, got %s", got[0].URI)
	}
}

func TestGetByCollectionFiltered_Eqi_NoUnicodeFold(t *testing.T) {
	// Cyrillic 'р' (U+0440) record vs Latin 'eqi:"p"' (U+0070).
	// asciiToLower + lower(... COLLATE "C") only fold ASCII A-Z, so
	// the two characters remain distinct. Pins R3 nit #11.
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpEqi, "p")
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range got {
		if strings.HasSuffix(rec.URI, "/r10") {
			t.Errorf("eqi unexpectedly folded Cyrillic 'р' onto Latin 'p': %s", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_Ini_MultiValue(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpIni, []interface{}{"project", "favorites"})
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// r1..r4 (Alice's projects), r6/r7 (Alice's favorites, both casings),
	// r11 (Bob's Project) = 7 total. r5 "projects" must not match.
	if len(got) != 7 {
		t.Errorf("expected 7 matches, got %d (%+v)", len(got), uriList(got))
	}
	for _, rec := range got {
		if strings.HasSuffix(rec.URI, "/r5") {
			t.Errorf("ini matched 'projects' (substring of 'project') — wrong semantics: %s", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_Eq_Regression_CaseSensitive(t *testing.T) {
	// Regression for acceptance criterion 6: `eq` continues to be
	// case-sensitive. Pin BOTH directions so a future `lower()`
	// regression on the containment path is caught: querying for
	// `"project"` returns only r1; querying for `"Project"` returns
	// only r2 (NOT r1). The first assertion alone would survive a
	// containment-side `lower()` because the payload would still
	// contain mixed-case `"Project"`.
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()

	fgLower := typeFilterGroup(repositories.OpEq, "project")
	gotLower, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fgLower)
	if err != nil {
		t.Fatalf("query (lowercase): %v", err)
	}
	if len(gotLower) != 1 || !strings.HasSuffix(gotLower[0].URI, "/r1") {
		t.Errorf("eq case-sensitive contract broken (lowercase); expected only r1, got %+v", uriList(gotLower))
	}

	fgUpper := typeFilterGroup(repositories.OpEq, "Project")
	gotUpper, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fgUpper)
	if err != nil {
		t.Fatalf("query (capitalized): %v", err)
	}
	if len(gotUpper) != 2 {
		t.Errorf("eq:'Project' expected to match r2 (Alice) + r11 (Bob), got %+v", uriList(gotUpper))
	}
	for _, rec := range gotUpper {
		if !strings.HasSuffix(rec.URI, "/r2") && !strings.HasSuffix(rec.URI, "/r11") {
			t.Errorf("eq:'Project' returned unexpected URI %s — case-sensitive contract broken", rec.URI)
		}
	}
}

func TestGetByCollectionFiltered_In_Regression_CaseSensitive(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpIn, []interface{}{"project", "favorites"})
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Only r1 ("project") + r6 ("favorites") — case-sensitive.
	if len(got) != 2 {
		t.Errorf("in case-sensitive contract broken; expected 2, got %d (%+v)", len(got), uriList(got))
	}
}

func TestGetByCollectionFiltered_Eqi_OrComposition(t *testing.T) {
	// Pins `_or` composition: "rows authored by Alice OR with
	// type=project case-insensitive." Should return Alice's full set
	// (11 minus the Bob row) plus Bob's Project record.
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Children: []repositories.FilterGroup{{
			Operator: repositories.GroupOR,
			Filters: []repositories.FieldFilter{
				{FieldName: "did", Operator: repositories.OpEq, Value: "did:plc:alice", IsJSON: false},
				{FieldName: "type", Operator: repositories.OpEqi, Value: "project", IsJSON: true},
			},
		}},
	}
	got, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// 10 Alice rows (r1..r10) + Bob's r11 = 11. Verify the URI set,
	// not just the count — a count-only assertion would silently
	// pass if a future regression returned a different 11-row union
	// (e.g. wrong author scoping).
	wantURIs := map[string]bool{}
	for i := 1; i <= 10; i++ {
		wantURIs[fmt.Sprintf("at://did:plc:alice/org.hypercerts.collection/r%d", i)] = false
	}
	wantURIs["at://did:plc:bob/org.hypercerts.collection/r11"] = false
	if len(got) != len(wantURIs) {
		t.Fatalf("expected %d records, got %d (%+v)", len(wantURIs), len(got), uriList(got))
	}
	for _, rec := range got {
		if _, ok := wantURIs[rec.URI]; !ok {
			t.Errorf("unexpected match: %s", rec.URI)
			continue
		}
		wantURIs[rec.URI] = true
	}
	for uri, seen := range wantURIs {
		if !seen {
			t.Errorf("expected match missing: %s", uri)
		}
	}
}

func TestGetByCollectionFiltered_Ini_RejectsEmpty(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()
	fg := typeFilterGroup(repositories.OpIni, []interface{}{})
	_, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err == nil {
		t.Fatalf("expected error on empty ini list, got nil")
	}
	if !strings.Contains(err.Error(), "1 to 50") {
		t.Errorf("error should mention unified 1 to 50 range; got: %v", err)
	}
}

func uriList(recs []*repositories.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.URI
	}
	return out
}

// TestGetByCollectionFiltered_Eqi_SingleElementIniBehavesLikeEqi pins
// that `ini` with a single value is equivalent to `eqi` — the SQL
// shape is different (`= ANY` vs `=`) but the matched set must be
// identical. A future refactor that special-cases single-element
// `ini` would regress here.
func TestGetByCollectionFiltered_Eqi_SingleElementIniBehavesLikeEqi(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()

	fgEqi := typeFilterGroup(repositories.OpEqi, "project")
	gotEqi, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fgEqi)
	if err != nil {
		t.Fatalf("eqi query: %v", err)
	}

	fgIni := typeFilterGroup(repositories.OpIni, []interface{}{"project"})
	gotIni, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
		100, "", "", repositories.RecordFilter{}, nil, fgIni)
	if err != nil {
		t.Fatalf("ini query: %v", err)
	}

	if len(gotEqi) != len(gotIni) {
		t.Fatalf("single-element ini diverged from eqi: eqi=%v ini=%v", uriList(gotEqi), uriList(gotIni))
	}
	got := map[string]bool{}
	for _, r := range gotIni {
		got[r.URI] = true
	}
	for _, r := range gotEqi {
		if !got[r.URI] {
			t.Errorf("eqi matched %s but single-element ini did not", r.URI)
		}
	}
}

// TestGetByCollectionFiltered_Eqi_AndCompositionOfTwoEqi pins
// AND-composition of two `eqi` filters on different JSON properties.
// Covers the missing case in implementation-review R3 #6d.
func TestGetByCollectionFiltered_Eqi_AndCompositionOfTwoEqi(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.collection"
	records := []struct {
		uri, did, body string
	}{
		{"at://did:plc:alice/" + col + "/a1", "did:plc:alice", `{"type":"Project","title":"Match"}`},
		{"at://did:plc:alice/" + col + "/a2", "did:plc:alice", `{"type":"project","title":"MATCH"}`},
		{"at://did:plc:alice/" + col + "/a3", "did:plc:alice", `{"type":"Project","title":"Different"}`},
		{"at://did:plc:alice/" + col + "/a4", "did:plc:alice", `{"type":"favorites","title":"Match"}`},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, "cid"+r.uri, r.did, col, r.body); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	fg := &repositories.FilterGroup{
		Operator: repositories.GroupAND,
		Filters: []repositories.FieldFilter{
			{FieldName: "type", Operator: repositories.OpEqi, Value: "project", IsJSON: true},
			{FieldName: "title", Operator: repositories.OpEqi, Value: "match", IsJSON: true},
		},
	}
	got, err := db.Records.GetByCollectionFiltered(ctx, col,
		100, "", "", repositories.RecordFilter{}, nil, fg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// a1 ("Project" + "Match") and a2 ("project" + "MATCH") match.
	// a3 (different title) and a4 (different type) do not.
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d (%+v)", len(got), uriList(got))
	}
	for _, rec := range got {
		if !strings.HasSuffix(rec.URI, "/a1") && !strings.HasSuffix(rec.URI, "/a2") {
			t.Errorf("unexpected URI: %s", rec.URI)
		}
	}
}

// TestGetByCollectionFiltered_Eqi_AbsentAndNonStringType pins behaviour
// for two producer-drift shapes we have to document explicitly:
//
//   - `type` field absent entirely — eqi:"project" must exclude the
//     record (json->>'absent' returns NULL, lower(NULL) is NULL,
//     `NULL = $n` is NULL → falsy).
//
//   - `type` is a non-string JSON scalar (numeric, boolean, null).
//     Postgres `json->>` text-coerces these to "42", "true", "null".
//     eqi:"42" matches a numeric-42 record by design — the AppView
//     reads the network truthfully; producer-side type drift is the
//     producer's concern. A future refactor of the JSON extractor
//     that changes this would silently shift the semantics.
func TestGetByCollectionFiltered_Eqi_AbsentAndNonStringType(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	const col = "org.hypercerts.collection"
	records := []struct {
		uri, did, body string
	}{
		{"at://did:plc:alice/" + col + "/n1", "did:plc:alice", `{"title":"AbsentType"}`},
		{"at://did:plc:alice/" + col + "/n2", "did:plc:alice", `{"type":42,"title":"NumericType"}`},
		{"at://did:plc:alice/" + col + "/n3", "did:plc:alice", `{"type":true,"title":"BoolType"}`},
		{"at://did:plc:alice/" + col + "/n4", "did:plc:alice", `{"type":null,"title":"NullType"}`},
		{"at://did:plc:alice/" + col + "/n5", "did:plc:alice", `{"type":"project","title":"StringType"}`},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(ctx, r.uri, "cid"+r.uri, r.did, col, r.body); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Absent type does NOT match any eqi value.
	gotAbsent, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, typeFilterGroup(repositories.OpEqi, "project"))
	if err != nil {
		t.Fatalf("query (project): %v", err)
	}
	for _, rec := range gotAbsent {
		if strings.HasSuffix(rec.URI, "/n1") {
			t.Errorf("eqi:'project' unexpectedly matched record with absent type field: %s", rec.URI)
		}
	}
	// String "project" still matches n5.
	if len(gotAbsent) != 1 || !strings.HasSuffix(gotAbsent[0].URI, "/n5") {
		t.Errorf("eqi:'project' expected to match only n5, got %+v", uriList(gotAbsent))
	}

	// Numeric type: eqi:"42" matches the n2 record (json->>'type' = "42").
	got42, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, typeFilterGroup(repositories.OpEqi, "42"))
	if err != nil {
		t.Fatalf("query (42): %v", err)
	}
	if len(got42) != 1 || !strings.HasSuffix(got42[0].URI, "/n2") {
		t.Errorf("eqi:'42' expected to match only n2 (numeric type), got %+v", uriList(got42))
	}

	// JSON null type passes through json->>'type' as NULL (not the
	// text "null"). eqi:"null" therefore does NOT match n4.
	gotNull, err := db.Records.GetByCollectionFiltered(ctx, col, 100, "", "",
		repositories.RecordFilter{}, nil, typeFilterGroup(repositories.OpEqi, "null"))
	if err != nil {
		t.Fatalf("query (null): %v", err)
	}
	for _, rec := range gotNull {
		if strings.HasSuffix(rec.URI, "/n4") {
			t.Errorf("eqi:'null' unexpectedly matched record with JSON-null type: %s", rec.URI)
		}
	}
}

// TestGetByCollectionFiltered_RecursiveValidate_RejectsBadShapes
// closes the P0 gap from implementation-review R3 #8: the
// validate-on-recurse tightening added to buildFilterGroupRecursive
// must surface validation errors from every branch of Validate(),
// not just the empty-list path the existing test pinned.
//
// A failure here means a programmer-constructed FieldFilter reaches
// the SQL emitter without value-level validation — exactly the gap
// the recursive-validate tightening was supposed to close.
func TestGetByCollectionFiltered_RecursiveValidate_RejectsBadShapes(t *testing.T) {
	db := seedCaseInsensitiveRecords(t)
	ctx := context.Background()

	overMax := make([]interface{}, repositories.MaxInListSize+1)
	for i := range overMax {
		overMax[i] = fmt.Sprintf("v%d", i)
	}

	cases := []struct {
		name      string
		filter    repositories.FieldFilter
		errSubstr string
	}{
		{
			name:      "eqi_on_column_level_field",
			filter:    repositories.FieldFilter{FieldName: "did", Operator: repositories.OpEqi, Value: "did:plc:abc", IsJSON: false},
			errSubstr: "eqi",
		},
		{
			name:      "eqi_with_non_string_value",
			filter:    repositories.FieldFilter{FieldName: "type", Operator: repositories.OpEqi, Value: 42, IsJSON: true},
			errSubstr: "string",
		},
		{
			name:      "ini_on_column_level_field",
			filter:    repositories.FieldFilter{FieldName: "did", Operator: repositories.OpIni, Value: []interface{}{"did:plc:abc"}, IsJSON: false},
			errSubstr: "ini",
		},
		{
			name:      "ini_oversize_list",
			filter:    repositories.FieldFilter{FieldName: "type", Operator: repositories.OpIni, Value: overMax, IsJSON: true},
			errSubstr: "1 to 50",
		},
		{
			name:      "in_oversize_list",
			filter:    repositories.FieldFilter{FieldName: "type", Operator: repositories.OpIn, Value: overMax, IsJSON: true},
			errSubstr: "1 to 50",
		},
		{
			name:      "in_non_scalar_element",
			filter:    repositories.FieldFilter{FieldName: "type", Operator: repositories.OpIn, Value: []interface{}{"project", map[string]interface{}{"nested": 1}}, IsJSON: true},
			errSubstr: "non-scalar",
		},
		{
			name:      "contains_below_min_length",
			filter:    repositories.FieldFilter{FieldName: "type", Operator: repositories.OpContains, Value: "ab", IsJSON: true},
			errSubstr: "characters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := &repositories.FilterGroup{
				Operator: repositories.GroupAND,
				Filters:  []repositories.FieldFilter{tc.filter},
			}
			_, err := db.Records.GetByCollectionFiltered(ctx, "org.hypercerts.collection",
				100, "", "", repositories.RecordFilter{}, nil, fg)
			if err == nil {
				t.Fatalf("expected error from recursive Validate(), got nil")
			}
			if !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("error should contain %q; got: %v", tc.errSubstr, err)
			}
		})
	}
}
