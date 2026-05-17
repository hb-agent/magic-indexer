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
	_, err := r.db.Exec(ctx, `INSERT INTO actor (did, handle, pds, indexed_at)
		VALUES ($1, $2, NULLIF($3, ''), NOW())
		ON CONFLICT(did) DO UPDATE SET
			handle = EXCLUDED.handle,
			pds = COALESCE(EXCLUDED.pds, actor.pds),
			indexed_at = NOW()`, []database.Value{
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
		valueSet := fmt.Sprintf("($%d, $%d, NOW())", base+1, base+2)
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
	_, err := r.db.Exec(ctx,
		"UPDATE actor SET pds = NULLIF($1, '') WHERE did = $2",
		[]database.Value{
			database.Text(pds),
			database.Text(did),
		})
	return err
}

// GetByDID retrieves an actor by their DID.
func (r *ActorsRepository) GetByDID(ctx context.Context, did string) (*Actor, error) {
	var actor Actor
	var indexedAtStr string
	err := r.db.QueryRow(ctx,
		"SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE did = $1",
		[]database.Value{database.Text(did)},
		&actor.DID, &actor.Handle, &indexedAtStr, &actor.PDS)
	if err != nil {
		return nil, err
	}

	actor.IndexedAt, _ = time.Parse(time.RFC3339, indexedAtStr)
	return &actor, nil
}

// GetByHandle retrieves an actor by their handle.
func (r *ActorsRepository) GetByHandle(ctx context.Context, handle string) (*Actor, error) {
	var actor Actor
	var indexedAtStr string
	err := r.db.QueryRow(ctx,
		"SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE handle = $1",
		[]database.Value{database.Text(handle)},
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

// DeleteByDIDTx removes the actor row for a single DID inside an
// existing transaction. Used by the actor-purge admin mutation so
// record and actor deletion commit (or fail) atomically. Returns the
// number of rows affected — caller should treat 0 as "actor was
// already absent" rather than an error.
func (r *ActorsRepository) DeleteByDIDTx(ctx context.Context, tx *sql.Tx, did string) (int64, error) {
	res, err := tx.ExecContext(ctx, "DELETE FROM actor WHERE did = $1", did)
	if err != nil {
		return 0, fmt.Errorf("delete actor: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// Exists checks if an actor exists by DID.
func (r *ActorsRepository) Exists(ctx context.Context, did string) (bool, error) {
	var count int64
	err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM actor WHERE did = $1",
		[]database.Value{database.Text(did)}, &count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
