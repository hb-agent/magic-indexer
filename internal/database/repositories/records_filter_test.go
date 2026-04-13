package repositories_test

import (
	"context"
	"errors"
	"fmt"
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
		repositories.RecordFilter{Authors: nil}, nil)
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
		repositories.RecordFilter{Authors: []string{}, nil)
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
		repositories.RecordFilter{Authors: []string{"did:plc:bob"}, nil)
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
		repositories.RecordFilter{Authors: []string{"did:plc:alice", "did:plc:carol"}, nil)
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
		repositories.RecordFilter{Authors: dids, nil)
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
		repositories.RecordFilter{Authors: dids, nil)
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
		repositories.RecordFilter{Authors: []string{"did:plc:bob", "did:plc:bob", "did:plc:bob"}, nil)
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
		repositories.RecordFilter{Authors: []string{"did:plc:alice"}, nil)
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
		page, err := db.Records.GetByCollectionFiltered(ctx, "col.pag", 3, afterTS, afterURI, filter, nil)
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
		}, nil)
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
		}, nil)
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
		repositories.RecordFilter{Authors: []string{"did:plc:abc"}, nil)
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
