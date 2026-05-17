package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthATPRequestsRepository handles outbound ATP OAuth request persistence.
type OAuthATPRequestsRepository struct {
	db database.Executor
}

// NewOAuthATPRequestsRepository creates a new OAuth ATP requests repository.
func NewOAuthATPRequestsRepository(db database.Executor) *OAuthATPRequestsRepository {
	return &OAuthATPRequestsRepository{db: db}
}

// Insert creates a new ATP OAuth request.
func (r *OAuthATPRequestsRepository) Insert(ctx context.Context, req *oauth.ATPRequest) error {
	_, err := r.db.Exec(ctx, `INSERT INTO oauth_atp_request (
		oauth_state, authorization_server, nonce, pkce_verifier,
		signing_public_key, dpop_private_key, created_at, expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		[]database.Value{
			database.Text(req.OAuthState),
			database.Text(req.AuthorizationServer),
			database.Text(req.Nonce),
			database.Text(req.PKCEVerifier),
			database.Text(req.SigningPublicKey),
			database.Text(req.DPoPPrivateKey),
			database.Int(req.CreatedAt),
			database.Int(req.ExpiresAt),
		})
	return err
}

// Get retrieves an ATP OAuth request by OAuth state.
func (r *OAuthATPRequestsRepository) Get(ctx context.Context, oauthState string) (*oauth.ATPRequest, error) {
	var req oauth.ATPRequest

	err := r.db.QueryRow(ctx, `SELECT oauth_state, authorization_server, nonce, pkce_verifier,
		signing_public_key, dpop_private_key, created_at, expires_at
	FROM oauth_atp_request WHERE oauth_state = $1`,
		[]database.Value{database.Text(oauthState)},
		&req.OAuthState, &req.AuthorizationServer, &req.Nonce, &req.PKCEVerifier,
		&req.SigningPublicKey, &req.DPoPPrivateKey, &req.CreatedAt, &req.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &req, nil
}

// Delete removes an ATP OAuth request.
func (r *OAuthATPRequestsRepository) Delete(ctx context.Context, oauthState string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_atp_request WHERE oauth_state = $1",
		[]database.Value{database.Text(oauthState)})
	return err
}

// DeleteExpired removes all expired ATP OAuth requests.
func (r *OAuthATPRequestsRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_atp_request WHERE expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}
