package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// Actor represents an AT Protocol user/actor.
type Actor struct {
	DID       string
	Handle    string
	IndexedAt time.Time
	// PDS is the actor's Personal Data Server endpoint, resolved from the
	// DID document. Empty string means "not yet resolved" — the upsert
	// path is best-effort and may persist an actor row before resolution
	// completes.
	PDS string
}

// ActorsRepository handles actor persistence.
type ActorsRepository struct {
	db database.Executor
}

// NewActorsRepository creates a new actors repository.
func NewActorsRepository(db database.Executor) *ActorsRepository {
	return &ActorsRepository{db: db}
}

// Upsert inserts or updates an actor without setting a PDS. Thin wrapper
// over UpsertWithPDS for callers that have not yet resolved the actor's
// PDS endpoint.
func (r *ActorsRepository) Upsert(ctx context.Context, did, handle string) error {
	return r.UpsertWithPDS(ctx, did, handle, "")
}

// UpsertWithPDS inserts or updates an actor, optionally setting their
// PDS service endpoint. An empty pds is treated as "do not overwrite":
// a previously-resolved value on the row is preserved via COALESCE so
// transient resolution failures don't blank the field.
func (r *ActorsRepository) UpsertWithPDS(ctx context.Context, did, handle, pds string) error {
	p1 := r.db.Placeholder(1)
	p2 := r.db.Placeholder(2)
	p3 := r.db.Placeholder(3)

	sqlStr := fmt.Sprintf(`INSERT INTO actor (did, handle, pds, indexed_at)
		VALUES (%s, %s, NULLIF(%s, ''), NOW())
		ON CONFLICT(did) DO UPDATE SET
			handle = EXCLUDED.handle,
			pds = COALESCE(EXCLUDED.pds, actor.pds),
			indexed_at = NOW()`, p1, p2, p3)

	_, err := r.db.Exec(ctx, sqlStr, []database.Value{
		database.Text(did),
		database.Text(handle),
		database.Text(pds),
	})
	return err
}

// ActorData holds DID and Handle for batch operations.
type ActorData struct {
	DID    string
	Handle string
}

// BatchUpsert inserts or updates multiple actors efficiently.
func (r *ActorsRepository) BatchUpsert(ctx context.Context, actors []ActorData) error {
	if len(actors) == 0 {
		return nil
	}

	// Process in batches to stay within SQL parameter limits
	batchSize := BatchInsertSize
	for i := 0; i < len(actors); i += batchSize {
		end := i + batchSize
		if end > len(actors) {
			end = len(actors)
		}
		batch := actors[i:end]

		if err := r.batchUpsertChunk(ctx, batch); err != nil {
			return err
		}
	}

	return nil
}

func (r *ActorsRepository) batchUpsertChunk(ctx context.Context, actors []ActorData) error {
	// Build value placeholders
	var valueSets []string
	var params []database.Value

	for i, actor := range actors {
		base := i * 2
		valueSet := fmt.Sprintf("(%s, %s, NOW())",
			r.db.Placeholder(base+1),
			r.db.Placeholder(base+2))
		valueSets = append(valueSets, valueSet)

		params = append(params,
			database.Text(actor.DID),
			database.Text(actor.Handle),
		)
	}

	sqlStr := fmt.Sprintf(`INSERT INTO actor (did, handle, indexed_at)
		VALUES %s
		ON CONFLICT(did) DO UPDATE SET
			handle = EXCLUDED.handle,
			indexed_at = NOW()`, strings.Join(valueSets, ", "))

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// SetPDS updates only the pds column for an actor identified by DID.
// Used by the standalone backfill CLI: a pure UPDATE avoids the
// upsert path's side effects on handle (clobber) and indexed_at
// (refreshed to NOW()), both of which would corrupt actor metadata
// when the backfill runs against rows that ingestion is concurrently
// writing. Returns nil with no row update if the actor doesn't exist
// — the backfill treats that as a benign skip rather than a failure.
func (r *ActorsRepository) SetPDS(ctx context.Context, did, pds string) error {
	sqlStr := fmt.Sprintf(
		"UPDATE actor SET pds = NULLIF(%s, '') WHERE did = %s",
		r.db.Placeholder(1), r.db.Placeholder(2),
	)
	_, err := r.db.Exec(ctx, sqlStr, []database.Value{
		database.Text(pds),
		database.Text(did),
	})
	return err
}

// GetByDID retrieves an actor by their DID.
func (r *ActorsRepository) GetByDID(ctx context.Context, did string) (*Actor, error) {
	sqlStr := fmt.Sprintf("SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE did = %s",
		r.db.Placeholder(1))

	var actor Actor
	var indexedAtStr string
	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(did)},
		&actor.DID, &actor.Handle, &indexedAtStr, &actor.PDS)
	if err != nil {
		return nil, err
	}

	actor.IndexedAt, _ = time.Parse(time.RFC3339, indexedAtStr)
	return &actor, nil
}

// GetByHandle retrieves an actor by their handle.
func (r *ActorsRepository) GetByHandle(ctx context.Context, handle string) (*Actor, error) {
	sqlStr := fmt.Sprintf("SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE handle = %s",
		r.db.Placeholder(1))

	var actor Actor
	var indexedAtStr string
	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(handle)},
		&actor.DID, &actor.Handle, &indexedAtStr, &actor.PDS)
	if err != nil {
		return nil, err
	}

	actor.IndexedAt, _ = time.Parse(time.RFC3339, indexedAtStr)
	return &actor, nil
}

// GetCount returns the total number of actors.
func (r *ActorsRepository) GetCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM actor", nil, &count)
	return count, err
}

// DeleteAll removes all actors.
func (r *ActorsRepository) DeleteAll(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM actor", nil)
	return err
}

// Exists checks if an actor exists by DID.
func (r *ActorsRepository) Exists(ctx context.Context, did string) (bool, error) {
	var count int64
	sqlStr := fmt.Sprintf("SELECT COUNT(*) FROM actor WHERE did = %s", r.db.Placeholder(1))
	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(did)}, &count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
