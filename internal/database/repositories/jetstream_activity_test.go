package repositories_test

import (
	"context"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

func setupActivityTest(t *testing.T) *repositories.JetstreamActivityRepository {
	t.Helper()
	db := testutil.SetupTestDB(t)
	return db.Activity
}

func TestJetstreamActivity_LogActivity(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	id, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, nil)
	if err != nil {
		t.Fatalf("LogActivity() error = %v", err)
	}
	if id <= 0 {
		t.Errorf("LogActivity() returned id = %d, want > 0", id)
	}

	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("GetCount() = %d, want 1", count)
	}
}

// TestJetstreamActivity_LogActivity_EmptyEventJSON pins the fix for
// the dialect-parity bug discovered in the live Postgres deployment:
// the Jetstream consumer passes an empty event_json on delete events
// (commit.Record is nil), and Postgres rejects empty strings as
// invalid JSONB. The LogActivity helper normalises empty payloads to
// the JSON literal "null" so the row is accepted.
func TestJetstreamActivity_LogActivity_EmptyEventJSON(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		eventJSON string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"newline only", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := repo.LogActivity(ctx, time.Now(), "delete", "app.bsky.feed.post", "did:plc:test1", "abc123", tc.eventJSON, nil)
			if err != nil {
				t.Fatalf("LogActivity() error = %v", err)
			}
			if id <= 0 {
				t.Errorf("LogActivity() returned id = %d, want > 0", id)
			}
		})
	}
}

func TestJetstreamActivity_LogActivityWithStatus(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	id, err := repo.LogActivityWithStatus(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, "success", nil)
	if err != nil {
		t.Fatalf("LogActivityWithStatus() error = %v", err)
	}
	if id <= 0 {
		t.Errorf("LogActivityWithStatus() returned id = %d, want > 0", id)
	}

	// Verify the custom status is stored by checking recent activity
	entries, err := repo.GetRecentActivity(ctx, 1)
	if err != nil {
		t.Fatalf("GetRecentActivity() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("GetRecentActivity() returned %d entries, want 1", len(entries))
	}
	if entries[0].Status != "success" {
		t.Errorf("Status = %q, want %q", entries[0].Status, "success")
	}
}

func TestJetstreamActivity_UpdateStatus(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	id, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, nil)
	if err != nil {
		t.Fatalf("LogActivity() error = %v", err)
	}

	errMsg := "something went wrong"
	err = repo.UpdateStatus(ctx, id, "error", &errMsg, nil)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	entries, err := repo.GetRecentActivity(ctx, 1)
	if err != nil {
		t.Fatalf("GetRecentActivity() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("GetRecentActivity() returned %d entries, want 1", len(entries))
	}
	if entries[0].Status != "error" {
		t.Errorf("Status = %q, want %q", entries[0].Status, "error")
	}
	if entries[0].ErrorMessage == nil {
		t.Fatal("ErrorMessage is nil, want non-nil")
	}
	if *entries[0].ErrorMessage != errMsg {
		t.Errorf("ErrorMessage = %q, want %q", *entries[0].ErrorMessage, errMsg)
	}
}

func TestJetstreamActivity_GetRecentActivity(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	now := time.Now()

	// Log several entries with recent timestamps
	for i := 0; i < 3; i++ {
		_, err := repo.LogActivity(ctx, now, "create", "app.bsky.feed.post", "did:plc:test1", "rkey", `{}`, nil)
		if err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	entries, err := repo.GetRecentActivity(ctx, 1)
	if err != nil {
		t.Fatalf("GetRecentActivity() error = %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("GetRecentActivity(1 hour) returned %d entries, want 3", len(entries))
	}
}

func TestJetstreamActivity_GetActivityBuckets(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	now := time.Now()

	// Log activities with different operations
	operations := []string{"create", "update", "delete"}
	for _, op := range operations {
		_, err := repo.LogActivity(ctx, now, op, "app.bsky.feed.post", "did:plc:test1", "rkey", `{}`, nil)
		if err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	buckets, err := repo.GetActivityBuckets(ctx, "ONE_HOUR")
	if err != nil {
		t.Fatalf("GetActivityBuckets() error = %v", err)
	}
	if len(buckets) == 0 {
		t.Fatal("GetActivityBuckets() returned 0 buckets, want at least 1")
	}

	// Verify aggregation: total across all buckets should be 3
	var totalSum int64
	for _, b := range buckets {
		totalSum += b.Total
	}
	if totalSum != 3 {
		t.Errorf("total across buckets = %d, want 3", totalSum)
	}
}

func TestJetstreamActivity_CleanupOldActivity(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	// Log a recent entry
	_, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "rkey", `{}`, nil)
	if err != nil {
		t.Fatalf("LogActivity() error = %v", err)
	}

	// Cleanup entries older than 24 hours - should not delete the recent one
	err = repo.CleanupOldActivity(ctx, 24)
	if err != nil {
		t.Fatalf("CleanupOldActivity() error = %v", err)
	}

	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("GetCount() after cleanup = %d, want 1 (recent entry should remain)", count)
	}
}

func TestJetstreamActivity_GetCount(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	// Empty table
	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("GetCount() on empty table = %d, want 0", count)
	}

	// After logging entries
	for i := 0; i < 3; i++ {
		_, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "rkey", `{}`, nil)
		if err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	count, err = repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 3 {
		t.Errorf("GetCount() after 3 inserts = %d, want 3", count)
	}
}

func TestJetstreamActivity_DeleteAll(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	// Insert entries
	for i := 0; i < 3; i++ {
		_, err := repo.LogActivity(ctx, time.Now(), "delete", "app.bsky.feed.post", "did:plc:test1", "rkey", `{}`, nil)
		if err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	// Verify they exist
	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("GetCount() = %d, want 3 before delete", count)
	}

	// Delete all
	err = repo.DeleteAll(ctx)
	if err != nil {
		t.Fatalf("DeleteAll() error = %v", err)
	}

	// Verify empty
	count, err = repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("GetCount() after DeleteAll = %d, want 0", count)
	}
}

// TestJetstreamActivity_LogActivity_DedupOnSourceEventID guards
// the load-bearing UNION-SELECT fallback in LogActivityWithStatus:
// when a redelivered event arrives with the same source_event_id,
// the second call must NOT insert a new row, and must return the
// existing row's id so the caller's subsequent UpdateStatus
// targets that row instead of orphaning a successful redelivery
// (Track 4 of review-2026-05-17, item R1.5).
func TestJetstreamActivity_LogActivity_DedupOnSourceEventID(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()
	sourceID := int64(1000)

	id1, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, &sourceID)
	if err != nil {
		t.Fatalf("first LogActivity() error = %v", err)
	}
	if id1 <= 0 {
		t.Fatalf("first LogActivity() returned id = %d, want > 0", id1)
	}

	// Same source_event_id — must return the same id, not insert a new row.
	id2, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, &sourceID)
	if err != nil {
		t.Fatalf("redelivered LogActivity() error = %v", err)
	}
	if id2 != id1 {
		t.Errorf("redelivered LogActivity() returned id = %d, want %d (the existing row's id)", id2, id1)
	}

	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("GetCount() after redelivery = %d, want 1 (dedup must not produce a second row)", count)
	}
}

// TestJetstreamActivity_LogActivity_DistinctSourceEventIDs_NoDedup
// is the inverse guard: two distinct upstream events with the same
// (did, rkey, cid) tuple but different source_event_id must each
// produce their own row (a re-create after delete is legitimately
// a new event, not a duplicate).
func TestJetstreamActivity_LogActivity_DistinctSourceEventIDs_NoDedup(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()
	id1Source := int64(2000)
	id2Source := int64(2001)

	id1, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, &id1Source)
	if err != nil {
		t.Fatalf("first LogActivity() error = %v", err)
	}

	id2, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, &id2Source)
	if err != nil {
		t.Fatalf("second LogActivity() error = %v", err)
	}
	if id2 == id1 {
		t.Errorf("distinct source_event_ids returned the same id %d — dedup matched on the wrong key", id1)
	}

	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("GetCount() with two distinct sources = %d, want 2", count)
	}
}

// TestJetstreamActivity_LogActivity_NilSourceEventID_NoDedup
// covers historical and backfill rows: when sourceEventID is nil
// the partial unique index does not apply, so two consecutive
// calls must produce two rows (no accidental NULL-merge).
func TestJetstreamActivity_LogActivity_NilSourceEventID_NoDedup(t *testing.T) {
	repo := setupActivityTest(t)
	ctx := context.Background()

	id1, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, nil)
	if err != nil {
		t.Fatalf("first LogActivity() error = %v", err)
	}
	id2, err := repo.LogActivity(ctx, time.Now(), "create", "app.bsky.feed.post", "did:plc:test1", "abc123", `{"type":"create"}`, nil)
	if err != nil {
		t.Fatalf("second LogActivity() error = %v", err)
	}
	if id2 == id1 {
		t.Errorf("nil source_event_id should not dedup; got the same id %d twice", id1)
	}

	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("GetCount() with two nil-source rows = %d, want 2", count)
	}
}
