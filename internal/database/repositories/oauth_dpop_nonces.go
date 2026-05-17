package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthDPoPNoncesRepository handles DPoP nonce persistence.
type OAuthDPoPNoncesRepository struct {
	db database.Executor
}

// NewOAuthDPoPNoncesRepository creates a new OAuth DPoP nonces repository.
func NewOAuthDPoPNoncesRepository(db database.Executor) *OAuthDPoPNoncesRepository {
	return &OAuthDPoPNoncesRepository{db: db}
}

// Insert creates a new DPoP nonce.
func (r *OAuthDPoPNoncesRepository) Insert(ctx context.Context, nonce *oauth.DPoPNonce) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO oauth_dpop_nonce (nonce, expires_at) VALUES ($1, $2)`,
		[]database.Value{
			database.Text(nonce.Nonce),
			database.Int(nonce.ExpiresAt),
		})
	return err
}

// Get retrieves a DPoP nonce by nonce string.
func (r *OAuthDPoPNoncesRepository) Get(ctx context.Context, nonceStr string) (*oauth.DPoPNonce, error) {
	var nonce oauth.DPoPNonce

	err := r.db.QueryRow(ctx,
		`SELECT nonce, expires_at FROM oauth_dpop_nonce WHERE nonce = $1`,
		[]database.Value{database.Text(nonceStr)},
		&nonce.Nonce, &nonce.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &nonce, nil
}

// Exists checks if a nonce exists and is not expired.
func (r *OAuthDPoPNoncesRepository) Exists(ctx context.Context, nonceStr string, currentTimestamp int64) (bool, error) {
	var exists int
	err := r.db.QueryRow(ctx,
		`SELECT 1 FROM oauth_dpop_nonce WHERE nonce = $1 AND expires_at > $2`,
		[]database.Value{database.Text(nonceStr), database.Int(currentTimestamp)}, &exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// Delete removes a DPoP nonce.
func (r *OAuthDPoPNoncesRepository) Delete(ctx context.Context, nonceStr string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_dpop_nonce WHERE nonce = $1",
		[]database.Value{database.Text(nonceStr)})
	return err
}

// DeleteExpired removes all expired DPoP nonces.
func (r *OAuthDPoPNoncesRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_dpop_nonce WHERE expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}
