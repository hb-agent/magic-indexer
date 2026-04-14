package notifications_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/notifications"
	"github.com/GainForest/hypergoat/internal/testutil"
)

// setupNotificationsTest returns a fresh test DB and a notifications
// repo bound to it. Uses Postgres via testutil; no t.Parallel() since
// the DB state is shared across tests.
func setupNotificationsTest(t *testing.T) (*testutil.TestDB, *notifications.Repository) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	return db, notifications.NewRepository(db.Executor)
}

// envelopeCount returns the number of rows in the notification table.
func envelopeCount(t *testing.T, db *testutil.TestDB) int {
	t.Helper()
	var n int
	if err := db.Executor.DB().QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM notification`,
	).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// participantCount returns the number of rows in notification_participant.
func participantCount(t *testing.T, db *testutil.TestDB) int {
	t.Helper()
	var n int
	if err := db.Executor.DB().QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM notification_participant`,
	).Scan(&n); err != nil {
		t.Fatalf("count participants: %v", err)
	}
	return n
}

// envelopeFor returns the envelope row (count, sort_at, latest_record_uri)
// for a given (did, group_key). group_key="" matches group_key IS NULL.
func envelopeFor(t *testing.T, db *testutil.TestDB, did, groupKey string) (count int, sortAt time.Time, latestURI string) {
	t.Helper()
	var (
		q    string
		args []any
	)
	if groupKey == "" {
		q = `SELECT count, sort_at, latest_record_uri FROM notification WHERE did = $1 AND group_key IS NULL`
		args = []any{did}
	} else {
		q = `SELECT count, sort_at, latest_record_uri FROM notification WHERE did = $1 AND group_key = $2`
		args = []any{did, groupKey}
	}
	err := db.Executor.DB().QueryRowContext(context.Background(), q, args...).Scan(&count, &sortAt, &latestURI)
	if err != nil {
		t.Fatalf("fetch envelope for did=%q group_key=%q: %v", did, groupKey, err)
	}
	return count, sortAt, latestURI
}

// mkNotif builds a Notification with sensible defaults.
func mkNotif(recipient, author, uri, groupKey string, sortAt time.Time) notifications.Notification {
	return notifications.Notification{
		Recipient:     recipient,
		Author:        author,
		RecordURI:     uri,
		RecordCID:     "bafyreia" + uri[len(uri)-4:], // stable-ish fake CID
		Reason:        notifications.ReasonEndorsement,
		ReasonSubject: "",
		SortAt:        sortAt,
		GroupKey:      groupKey,
	}
}

// TestApply_Aggregated_NewEnvelope is the regression test for issue #61.
// Before the fix this failed with Postgres SQLSTATE 42P10 because the
// ON CONFLICT clause didn't include the partial index's WHERE predicate.
func TestApply_Aggregated_NewEnvelope(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/app.certified.temp.graph.endorsement/rk1", "endorse:did:plc:alice", now)
	if err := repo.Apply(ctx, []notifications.Notification{n}); err != nil {
		t.Fatalf("Apply failed (regression!): %v", err)
	}

	if got := envelopeCount(t, db); got != 1 {
		t.Fatalf("want 1 envelope, got %d", got)
	}
	if got := participantCount(t, db); got != 1 {
		t.Fatalf("want 1 participant, got %d", got)
	}
	count, _, latestURI := envelopeFor(t, db, "did:plc:alice", "endorse:did:plc:alice")
	if count != 1 {
		t.Errorf("envelope count: want 1, got %d", count)
	}
	if latestURI != n.RecordURI {
		t.Errorf("latest_record_uri: want %q, got %q", n.RecordURI, latestURI)
	}
}

// TestApply_Aggregated_SecondRecordSameGroup — two different records in
// the same group collapse into one envelope with count=2.
func TestApply_Aggregated_SecondRecordSameGroup(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	n1 := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "g1", base)
	n2 := mkNotif("did:plc:alice", "did:plc:carol", "at://did:plc:carol/col/rk2", "g1", base.Add(time.Minute))

	if err := repo.Apply(ctx, []notifications.Notification{n1, n2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := envelopeCount(t, db); got != 1 {
		t.Errorf("want 1 envelope, got %d", got)
	}
	if got := participantCount(t, db); got != 2 {
		t.Errorf("want 2 participants, got %d", got)
	}
	count, sortAt, latestURI := envelopeFor(t, db, "did:plc:alice", "g1")
	if count != 2 {
		t.Errorf("count: want 2, got %d", count)
	}
	if !sortAt.Equal(n2.SortAt) {
		t.Errorf("sort_at: want %v, got %v", n2.SortAt, sortAt)
	}
	if latestURI != n2.RecordURI {
		t.Errorf("latest_record_uri: want %q, got %q", n2.RecordURI, latestURI)
	}
}

// TestApply_Aggregated_Replay — replaying the exact same notification is
// a no-op (no double count).
func TestApply_Aggregated_Replay(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "g1", now)
	if err := repo.Apply(ctx, []notifications.Notification{n, n}); err != nil {
		t.Fatalf("Apply (replay): %v", err)
	}

	if got := envelopeCount(t, db); got != 1 {
		t.Errorf("want 1 envelope, got %d", got)
	}
	if got := participantCount(t, db); got != 1 {
		t.Errorf("want 1 participant, got %d", got)
	}
	count, _, _ := envelopeFor(t, db, "did:plc:alice", "g1")
	if count != 1 {
		t.Errorf("count after replay: want 1, got %d", count)
	}
}

// TestApply_Aggregated_ReplayAcrossCalls — calling Apply twice with the
// same notification (separate calls, not same slice) is still idempotent.
// Simulates the realistic firehose-replay scenario.
func TestApply_Aggregated_ReplayAcrossCalls(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "g1", now)
	if err := repo.Apply(ctx, []notifications.Notification{n}); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}
	if err := repo.Apply(ctx, []notifications.Notification{n}); err != nil {
		t.Fatalf("Apply #2 (replay): %v", err)
	}

	if got := envelopeCount(t, db); got != 1 {
		t.Errorf("want 1 envelope, got %d", got)
	}
	if got := participantCount(t, db); got != 1 {
		t.Errorf("want 1 participant, got %d", got)
	}
	count, _, _ := envelopeFor(t, db, "did:plc:alice", "g1")
	if count != 1 {
		t.Errorf("count after cross-call replay: want 1, got %d", count)
	}
}

// TestApply_Aggregated_OlderSortAtPreservesLatest — when a second record
// arrives with an older SortAt, latest_* fields must NOT be overwritten.
func TestApply_Aggregated_OlderSortAtPreservesLatest(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	newer := time.Now().UTC().Truncate(time.Second)
	older := newer.Add(-time.Hour)

	first := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/newer", "g1", newer)
	second := mkNotif("did:plc:alice", "did:plc:carol", "at://did:plc:carol/col/older", "g1", older)

	if err := repo.Apply(ctx, []notifications.Notification{first, second}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	count, sortAt, latestURI := envelopeFor(t, db, "did:plc:alice", "g1")
	if count != 2 {
		t.Errorf("count: want 2, got %d", count)
	}
	// sort_at should be max of the two
	if !sortAt.Equal(newer) {
		t.Errorf("sort_at: want newer %v, got %v", newer, sortAt)
	}
	// latest_* should still point to the newer record, not the older one
	if latestURI != first.RecordURI {
		t.Errorf("latest_record_uri: want newer %q, got %q", first.RecordURI, latestURI)
	}
}

// TestApply_NonAggregated — with empty GroupKey, every record creates a
// new envelope, group_key is NULL.
func TestApply_NonAggregated(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "", now)
	n2 := mkNotif("did:plc:alice", "did:plc:carol", "at://did:plc:carol/col/rk2", "", now.Add(time.Minute))

	if err := repo.Apply(ctx, []notifications.Notification{n1, n2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := envelopeCount(t, db); got != 2 {
		t.Errorf("want 2 envelopes (non-aggregated), got %d", got)
	}
	if got := participantCount(t, db); got != 2 {
		t.Errorf("want 2 participants, got %d", got)
	}
	// Both envelopes have group_key IS NULL
	var nullCount int
	if err := db.Executor.DB().QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM notification WHERE did = $1 AND group_key IS NULL`,
		"did:plc:alice",
	).Scan(&nullCount); err != nil {
		t.Fatalf("count null group_keys: %v", err)
	}
	if nullCount != 2 {
		t.Errorf("want 2 rows with group_key IS NULL, got %d", nullCount)
	}
}

// TestApply_NonAggregated_Replay — replaying a non-aggregated
// notification is a no-op (participant conflict triggers the DELETE
// cleanup path in repo.go for the envelope with count=0).
func TestApply_NonAggregated_Replay(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "", now)
	if err := repo.Apply(ctx, []notifications.Notification{n, n}); err != nil {
		t.Fatalf("Apply (replay): %v", err)
	}

	if got := envelopeCount(t, db); got != 1 {
		t.Errorf("want 1 envelope, got %d", got)
	}
	if got := participantCount(t, db); got != 1 {
		t.Errorf("want 1 participant, got %d", got)
	}
	// Count should be exactly 1 — the replay must not bump it and the
	// envelope must not have been deleted by the count=0 cleanup path.
	count, _, _ := envelopeFor(t, db, "did:plc:alice", "")
	if count != 1 {
		t.Errorf("count after non-aggregated replay: want 1, got %d", count)
	}
}

// TestApply_DifferentDIDs_SameGroupKey — two different recipients with
// the same group_key get separate envelopes (uniqueness is scoped by did).
func TestApply_DifferentDIDs_SameGroupKey(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	nAlice := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "shared-g", now)
	nCarol := mkNotif("did:plc:carol", "did:plc:bob", "at://did:plc:bob/col/rk2", "shared-g", now)

	if err := repo.Apply(ctx, []notifications.Notification{nAlice, nCarol}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := envelopeCount(t, db); got != 2 {
		t.Errorf("want 2 envelopes (one per did), got %d", got)
	}
	// Each envelope should be attributed to the right DID with count=1.
	aliceCount, _, aliceURI := envelopeFor(t, db, "did:plc:alice", "shared-g")
	if aliceCount != 1 {
		t.Errorf("alice envelope count: want 1, got %d", aliceCount)
	}
	if aliceURI != nAlice.RecordURI {
		t.Errorf("alice latest_record_uri: want %q, got %q", nAlice.RecordURI, aliceURI)
	}
	carolCount, _, carolURI := envelopeFor(t, db, "did:plc:carol", "shared-g")
	if carolCount != 1 {
		t.Errorf("carol envelope count: want 1, got %d", carolCount)
	}
	if carolURI != nCarol.RecordURI {
		t.Errorf("carol latest_record_uri: want %q, got %q", nCarol.RecordURI, carolURI)
	}
}

// TestApply_NullReasonSubject — empty ReasonSubject stores as SQL NULL.
func TestApply_NullReasonSubject(t *testing.T) {
	db, repo := setupNotificationsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := mkNotif("did:plc:alice", "did:plc:bob", "at://did:plc:bob/col/rk1", "g1", now)
	n.ReasonSubject = ""
	if err := repo.Apply(ctx, []notifications.Notification{n}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var rs sql.NullString
	if err := db.Executor.DB().QueryRowContext(
		ctx,
		`SELECT reason_subject FROM notification WHERE did = $1`,
		"did:plc:alice",
	).Scan(&rs); err != nil {
		t.Fatalf("fetch reason_subject: %v", err)
	}
	if rs.Valid {
		t.Errorf("reason_subject: want NULL, got %q", rs.String)
	}
}
