package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthDPoPJTIRepository handles DPoP JTI replay protection.
type OAuthDPoPJTIRepository struct {
	db database.Executor
}

// NewOAuthDPoPJTIRepository creates a new OAuth DPoP JTI repository.
func NewOAuthDPoPJTIRepository(db database.Executor) *OAuthDPoPJTIRepository {
	return &OAuthDPoPJTIRepository{db: db}
}

// Insert creates a new DPoP JTI record (for replay protection).
// Prefer InsertIfNew for race-safe replay detection — Insert will
// return a raw unique-constraint error on a concurrent replay
// attempt instead of the structured (bool, error) contract.
func (r *OAuthDPoPJTIRepository) Insert(ctx context.Context, jti *oauth.DPoPJTI) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO oauth_dpop_jti (jti, created_at) VALUES ($1, $2)`,
		[]database.Value{
			database.Text(jti.JTI),
			database.Int(jti.CreatedAt),
		})
	return err
}

// InsertIfNew inserts a JTI row and reports whether it was newly
// created. Two concurrent DPoP proofs with the same jti previously
// both passed the Exists() pre-check and then collided on Insert —
// one succeeded, the other got a raw unique-constraint error that
// wasn't recognised as a replay. This method makes the insert
// race-free: it relies on the jti primary key + ON CONFLICT to
// either insert or no-op, and uses RowsAffected to tell the caller
// which happened.
func (r *OAuthDPoPJTIRepository) InsertIfNew(ctx context.Context, jti *oauth.DPoPJTI) (bool, error) {
	res, err := r.db.Exec(ctx,
		`INSERT INTO oauth_dpop_jti (jti, created_at) VALUES ($1, $2) ON CONFLICT (jti) DO NOTHING`,
		[]database.Value{
			database.Text(jti.JTI),
			database.Int(jti.CreatedAt),
		})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Get retrieves a DPoP JTI by JTI string.
func (r *OAuthDPoPJTIRepository) Get(ctx context.Context, jtiStr string) (*oauth.DPoPJTI, error) {
	var jti oauth.DPoPJTI

	err := r.db.QueryRow(ctx,
		`SELECT jti, created_at FROM oauth_dpop_jti WHERE jti = $1`,
		[]database.Value{database.Text(jtiStr)},
		&jti.JTI, &jti.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &jti, nil
}

// Exists checks if a JTI has been used (replay detection).
func (r *OAuthDPoPJTIRepository) Exists(ctx context.Context, jtiStr string) (bool, error) {
	var exists int
	err := r.db.QueryRow(ctx,
		`SELECT 1 FROM oauth_dpop_jti WHERE jti = $1`,
		[]database.Value{database.Text(jtiStr)}, &exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// Delete removes a DPoP JTI record.
func (r *OAuthDPoPJTIRepository) Delete(ctx context.Context, jtiStr string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_dpop_jti WHERE jti = $1",
		[]database.Value{database.Text(jtiStr)})
	return err
}

// DeleteOlderThan removes all JTI records older than the specified timestamp.
// This is used to clean up old replay protection records.
func (r *OAuthDPoPJTIRepository) DeleteOlderThan(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_dpop_jti WHERE created_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}
