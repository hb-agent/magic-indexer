package repositories_test

import (
	"context"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

// seedLabeledRecords inserts records and labels for the label filter tests.
// It uses two hypercerts from one labeler: one high-quality, one draft,
// plus a third unlabeled record.
func seedLabeledRecords(t *testing.T) *testutil.TestDB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	// Ensure the two domain labels exist in the definition table.
	// (The default seed only has Bluesky labels.)
	if err := db.LabelDefinitions.Insert(
		ctx, "high-quality", "", repositories.SeverityInform, repositories.VisibilityShow,
	); err != nil {
		t.Fatalf("insert label definition high-quality: %v", err)
	}
	if err := db.LabelDefinitions.Insert(
		ctx, "draft", "", repositories.SeverityInform, repositories.VisibilityWarn,
	); err != nil {
		t.Fatalf("insert label definition draft: %v", err)
	}

	records := []struct {
		uri, cid string
	}{
		{"at://did:plc:alice/social.cert.hypercert/rec1", "bafyrec1"},
		{"at://did:plc:alice/social.cert.hypercert/rec2", "bafyrec2"},
		{"at://did:plc:alice/social.cert.hypercert/rec3", "bafyrec3"},
	}
	for _, r := range records {
		if _, err := db.Records.Insert(
			ctx, r.uri, r.cid, "did:plc:alice", "social.cert.hypercert",
			`{"title":"t"}`,
		); err != nil {
			t.Fatalf("insert record %s: %v", r.uri, err)
		}
	}

	labeler := "did:plc:labelerz"
	// rec1 -> high-quality
	if _, err := db.Labels.Insert(ctx, labeler, records[0].uri, nil, "high-quality", nil, nil); err != nil {
		t.Fatalf("label rec1: %v", err)
	}
	// rec2 -> draft
	if _, err := db.Labels.Insert(ctx, labeler, records[1].uri, nil, "draft", nil, nil); err != nil {
		t.Fatalf("label rec2: %v", err)
	}
	// rec3 -> (unlabeled)

	return db
}

func TestRecordsRepository_LabelFilter_Include(t *testing.T) {
	db := seedLabeledRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{
			LabelerSrcs: []string{"did:plc:labelerz"},
			Include:     []string{"high-quality"},
		},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].URI != "at://did:plc:alice/social.cert.hypercert/rec1" {
		t.Errorf("expected rec1, got %s", got[0].URI)
	}
}

func TestRecordsRepository_LabelFilter_Exclude(t *testing.T) {
	db := seedLabeledRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{
			LabelerSrcs: []string{"did:plc:labelerz"},
			Exclude:     []string{"draft"},
		},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	// Should return rec1 (high-quality) and rec3 (unlabeled), but not rec2 (draft).
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d (%v)", len(got), recordURIs(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.URI] = true
	}
	if seen["at://did:plc:alice/social.cert.hypercert/rec2"] {
		t.Errorf("rec2 should have been excluded")
	}
}

func TestRecordsRepository_LabelFilter_Empty_DelegatesUnfiltered(t *testing.T) {
	db := seedLabeledRecords(t)
	ctx := context.Background()

	got, err := db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected all 3 records when filter empty, got %d", len(got))
	}
}

func TestRecordsRepository_LabelFilter_HonorsNegation(t *testing.T) {
	db := seedLabeledRecords(t)
	ctx := context.Background()

	// Negate rec1's high-quality label. It should no longer match the include.
	if _, err := db.Labels.InsertNegation(
		ctx, "did:plc:labelerz",
		"at://did:plc:alice/social.cert.hypercert/rec1",
		"high-quality",
		nil,
	); err != nil {
		t.Fatalf("negate: %v", err)
	}

	got, err := db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{
			LabelerSrcs: []string{"did:plc:labelerz"},
			Include:     []string{"high-quality"},
		},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no records after negation, got %d (%v)", len(got), recordURIs(got))
	}
}

func TestRecordsRepository_LabelFilter_NeutralAcrossLabelers(t *testing.T) {
	db := seedLabeledRecords(t)
	ctx := context.Background()

	// Add a second labeler asserting high-quality on rec3. Without
	// LabelerSrcs the filter should match labels from BOTH labelers,
	// so the query returns rec1 (labelerz) and rec3 (labelerz2).
	ctx2 := context.Background()
	if _, err := db.Labels.Insert(
		ctx2, "did:plc:labelerz2",
		"at://did:plc:alice/social.cert.hypercert/rec3",
		nil, "high-quality", nil, nil,
	); err != nil {
		t.Fatalf("insert second labeler: %v", err)
	}

	// Neutral filter — no LabelerSrcs → union across all labelers.
	got, err := db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{
			Include: []string{"high-quality"},
		},
	)
	if err != nil {
		t.Fatalf("query (neutral): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records (union across labelers), got %d: %v",
			len(got), recordURIs(got))
	}

	// Narrowed to just the second labeler — should only see rec3.
	got, err = db.Records.GetByCollectionWithLabelFilterAndKeysetCursor(
		ctx, "social.cert.hypercert", 10, "", "",
		repositories.LabelFilter{
			LabelerSrcs: []string{"did:plc:labelerz2"},
			Include:     []string{"high-quality"},
		},
	)
	if err != nil {
		t.Fatalf("query (scoped): %v", err)
	}
	if len(got) != 1 || got[0].URI != "at://did:plc:alice/social.cert.hypercert/rec3" {
		t.Errorf("expected only rec3, got %d: %v", len(got), recordURIs(got))
	}
}

func recordURIs(records []*repositories.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.URI
	}
	return out
}
