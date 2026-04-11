package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	sqlStr := fmt.Sprintf(`INSERT INTO oauth_dpop_jti (jti, created_at) VALUES (%s, %s)`,
		r.db.Placeholder(1), r.db.Placeholder(2))

	params := []database.Value{
		database.Text(jti.JTI),
		database.Int(jti.CreatedAt),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
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
	sqlStr := fmt.Sprintf(
		`INSERT INTO oauth_dpop_jti (jti, created_at) VALUES (%s, %s) ON CONFLICT (jti) DO NOTHING`,
		r.db.Placeholder(1), r.db.Placeholder(2),
	)
	params := []database.Value{
		database.Text(jti.JTI),
		database.Int(jti.CreatedAt),
	}
	res, err := r.db.Exec(ctx, sqlStr, params)
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
	sqlStr := fmt.Sprintf(`SELECT jti, created_at FROM oauth_dpop_jti WHERE jti = %s`, r.db.Placeholder(1))

	var jti oauth.DPoPJTI

	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(jtiStr)},
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
	sqlStr := fmt.Sprintf(`SELECT 1 FROM oauth_dpop_jti WHERE jti = %s`, r.db.Placeholder(1))

	var exists int
	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(jtiStr)}, &exists)
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
	sqlStr := fmt.Sprintf("DELETE FROM oauth_dpop_jti WHERE jti = %s", r.db.Placeholder(1))
	_, err := r.db.Exec(ctx, sqlStr, []database.Value{database.Text(jtiStr)})
	return err
}

// DeleteOlderThan removes all JTI records older than the specified timestamp.
// This is used to clean up old replay protection records.
func (r *OAuthDPoPJTIRepository) DeleteOlderThan(ctx context.Context, beforeTimestamp int64) error {
	sqlStr := fmt.Sprintf("DELETE FROM oauth_dpop_jti WHERE created_at < %s", r.db.Placeholder(1))
	_, err := r.db.Exec(ctx, sqlStr, []database.Value{database.Int(beforeTimestamp)})
	return err
}
