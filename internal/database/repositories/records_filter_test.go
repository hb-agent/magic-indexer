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
