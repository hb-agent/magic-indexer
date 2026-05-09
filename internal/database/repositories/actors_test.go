package repositories_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

func setupActorsTest(t *testing.T) *repositories.ActorsRepository {
	t.Helper()
	db := testutil.SetupTestDB(t)
	return db.Actors
}

func TestActorsRepository_Upsert(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Insert new actor
	err := repo.Upsert(ctx, "did:plc:testactor1", "alice.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}

	actor, err := repo.GetByDID(ctx, "did:plc:testactor1")
	if err != nil {
		t.Fatalf("failed to get actor after insert: %v", err)
	}
	if actor.DID != "did:plc:testactor1" {
		t.Errorf("DID = %q, want %q", actor.DID, "did:plc:testactor1")
	}
	if actor.Handle != "alice.bsky.social" {
		t.Errorf("Handle = %q, want %q", actor.Handle, "alice.bsky.social")
	}

	// Update handle via upsert
	err = repo.Upsert(ctx, "did:plc:testactor1", "alice-new.bsky.social")
	if err != nil {
		t.Fatalf("failed to upsert actor: %v", err)
	}

	actor, err = repo.GetByDID(ctx, "did:plc:testactor1")
	if err != nil {
		t.Fatalf("failed to get actor after upsert: %v", err)
	}
	if actor.Handle != "alice-new.bsky.social" {
		t.Errorf("Handle after upsert = %q, want %q", actor.Handle, "alice-new.bsky.social")
	}
}

func TestActorsRepository_UpsertWithPDS_SetsPDSOnInsert(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.UpsertWithPDS(ctx, "did:plc:withpds", "alice", "https://pds.example.com"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetByDID(ctx, "did:plc:withpds")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PDS != "https://pds.example.com" {
		t.Errorf("PDS = %q, want %q", got.PDS, "https://pds.example.com")
	}
}

func TestActorsRepository_UpsertWithPDS_EmptyPDSPreservesPriorValue(t *testing.T) {
	// COALESCE(EXCLUDED.pds, actor.pds) means an upsert with pds="" must
	// not blank a previously-resolved value. This protects against
	// transient resolver failures resetting good data.
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.UpsertWithPDS(ctx, "did:plc:trans", "alice", "https://good.example.com"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repo.UpsertWithPDS(ctx, "did:plc:trans", "alice", ""); err != nil {
		t.Fatalf("second upsert (empty pds): %v", err)
	}
	got, err := repo.GetByDID(ctx, "did:plc:trans")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PDS != "https://good.example.com" {
		t.Errorf("PDS = %q, want preserved %q", got.PDS, "https://good.example.com")
	}
}

func TestActorsRepository_UpsertWithPDS_NonEmptyOverwritesPrior(t *testing.T) {
	// PDS migrations across servers happen (rarely, but they happen).
	// A non-empty new pds should overwrite an existing value.
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.UpsertWithPDS(ctx, "did:plc:migrant", "alice", "https://old.example.com"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repo.UpsertWithPDS(ctx, "did:plc:migrant", "alice", "https://new.example.com"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := repo.GetByDID(ctx, "did:plc:migrant")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PDS != "https://new.example.com" {
		t.Errorf("PDS = %q, want new %q", got.PDS, "https://new.example.com")
	}
}

func TestActorsRepository_Upsert_LeavesPDSEmpty(t *testing.T) {
	// The plain Upsert wrapper must persist actors with empty pds —
	// it's used by the legacy callers that haven't been wired into
	// the resolver yet.
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, "did:plc:legacy", "alice"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetByDID(ctx, "did:plc:legacy")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PDS != "" {
		t.Errorf("PDS = %q, want empty", got.PDS)
	}
}

func TestActorsRepository_SetPDS_UpdatesExistingActor(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, "did:plc:setpds", "alice"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.SetPDS(ctx, "did:plc:setpds", "https://pds.example.com"); err != nil {
		t.Fatalf("set pds: %v", err)
	}
	got, err := repo.GetByDID(ctx, "did:plc:setpds")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PDS != "https://pds.example.com" {
		t.Errorf("PDS = %q, want %q", got.PDS, "https://pds.example.com")
	}
	if got.Handle != "alice" {
		t.Errorf("Handle clobbered: got %q, want %q", got.Handle, "alice")
	}
}

func TestActorsRepository_SetPDS_PreservesIndexedAt(t *testing.T) {
	// SetPDS must not touch indexed_at — the backfill CLI runs against
	// every existing actor and must not collapse the column to a single
	// timestamp. Compare with UpsertWithPDS, which deliberately refreshes
	// indexed_at to NOW().
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, "did:plc:keepts", "alice"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before, err := repo.GetByDID(ctx, "did:plc:keepts")
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	// Wait a microsecond so any clock-tick refresh would be visible.
	time.Sleep(20 * time.Millisecond)
	if err := repo.SetPDS(ctx, "did:plc:keepts", "https://pds.example.com"); err != nil {
		t.Fatalf("set pds: %v", err)
	}
	after, err := repo.GetByDID(ctx, "did:plc:keepts")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if !after.IndexedAt.Equal(before.IndexedAt) {
		t.Errorf("indexed_at moved: before=%v after=%v", before.IndexedAt, after.IndexedAt)
	}
}

func TestActorsRepository_SetPDS_MissingActorIsNoop(t *testing.T) {
	// Running the backfill against a row that vanished between scan and
	// resolve must not error — UPDATE matching zero rows is a clean
	// skip, not a failure to surface to the operator.
	repo := setupActorsTest(t)
	ctx := context.Background()

	if err := repo.SetPDS(ctx, "did:plc:doesnotexist", "https://pds.example.com"); err != nil {
		t.Errorf("expected no error for missing actor, got: %v", err)
	}
}

func TestActorsRepository_BatchUpsert(t *testing.T) {
	tests := []struct {
		name   string
		actors []repositories.ActorData
		want   int64
	}{
		{
			name:   "empty slice",
			actors: []repositories.ActorData{},
			want:   0,
		},
		{
			name: "single actor",
			actors: []repositories.ActorData{
				{DID: "did:plc:testactor1", Handle: "alice.bsky.social"},
			},
			want: 1,
		},
		{
			name: "multiple actors",
			actors: []repositories.ActorData{
				{DID: "did:plc:testactor1", Handle: "alice.bsky.social"},
				{DID: "did:plc:testactor2", Handle: "bob.bsky.social"},
				{DID: "did:plc:testactor3", Handle: "carol.bsky.social"},
			},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := setupActorsTest(t)
			ctx := context.Background()

			err := repo.BatchUpsert(ctx, tt.actors)
			if err != nil {
				t.Fatalf("BatchUpsert() error = %v", err)
			}

			count, err := repo.GetCount(ctx)
			if err != nil {
				t.Fatalf("GetCount() error = %v", err)
			}
			if count != tt.want {
				t.Errorf("GetCount() = %d, want %d", count, tt.want)
			}
		})
	}
}

func TestActorsRepository_GetByDID(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Setup: insert an actor
	err := repo.Upsert(ctx, "did:plc:testactor1", "alice.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		actor, err := repo.GetByDID(ctx, "did:plc:testactor1")
		if err != nil {
			t.Fatalf("GetByDID() error = %v", err)
		}
		if actor.DID != "did:plc:testactor1" {
			t.Errorf("DID = %q, want %q", actor.DID, "did:plc:testactor1")
		}
		if actor.Handle != "alice.bsky.social" {
			t.Errorf("Handle = %q, want %q", actor.Handle, "alice.bsky.social")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByDID(ctx, "did:plc:nonexistent")
		if err == nil {
			t.Fatal("GetByDID() expected error for non-existing DID, got nil")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetByDID() error = %v, want sql.ErrNoRows", err)
		}
	})
}

func TestActorsRepository_GetByHandle(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Setup: insert an actor
	err := repo.Upsert(ctx, "did:plc:testactor1", "alice.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		actor, err := repo.GetByHandle(ctx, "alice.bsky.social")
		if err != nil {
			t.Fatalf("GetByHandle() error = %v", err)
		}
		if actor.DID != "did:plc:testactor1" {
			t.Errorf("DID = %q, want %q", actor.DID, "did:plc:testactor1")
		}
		if actor.Handle != "alice.bsky.social" {
			t.Errorf("Handle = %q, want %q", actor.Handle, "alice.bsky.social")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByHandle(ctx, "nobody.bsky.social")
		if err == nil {
			t.Fatal("GetByHandle() expected error for non-existing handle, got nil")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetByHandle() error = %v, want sql.ErrNoRows", err)
		}
	})
}

func TestActorsRepository_GetCount(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Empty database
	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("GetCount() on empty db = %d, want 0", count)
	}

	// After inserts
	err = repo.Upsert(ctx, "did:plc:testactor1", "alice.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}
	err = repo.Upsert(ctx, "did:plc:testactor2", "bob.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}

	count, err = repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("GetCount() after 2 inserts = %d, want 2", count)
	}
}

func TestActorsRepository_DeleteAll(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Insert some actors
	err := repo.BatchUpsert(ctx, []repositories.ActorData{
		{DID: "did:plc:testactor1", Handle: "alice.bsky.social"},
		{DID: "did:plc:testactor2", Handle: "bob.bsky.social"},
	})
	if err != nil {
		t.Fatalf("BatchUpsert() error = %v", err)
	}

	// Verify they exist
	count, err := repo.GetCount(ctx)
	if err != nil {
		t.Fatalf("GetCount() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("GetCount() = %d, want 2 before delete", count)
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

func TestActorsRepository_Exists(t *testing.T) {
	repo := setupActorsTest(t)
	ctx := context.Background()

	// Insert an actor
	err := repo.Upsert(ctx, "did:plc:testactor1", "alice.bsky.social")
	if err != nil {
		t.Fatalf("failed to insert actor: %v", err)
	}

	t.Run("existing actor", func(t *testing.T) {
		exists, err := repo.Exists(ctx, "did:plc:testactor1")
		if err != nil {
			t.Fatalf("Exists() error = %v", err)
		}
		if !exists {
			t.Error("Exists() = false, want true for existing actor")
		}
	})

	t.Run("non-existing actor", func(t *testing.T) {
		exists, err := repo.Exists(ctx, "did:plc:nonexistent")
		if err != nil {
			t.Fatalf("Exists() error = %v", err)
		}
		if exists {
			t.Error("Exists() = true, want false for non-existing actor")
		}
	})
}
