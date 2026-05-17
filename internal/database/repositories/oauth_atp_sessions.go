package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthATPSessionsRepository handles ATP session persistence.
type OAuthATPSessionsRepository struct {
	db database.Executor
}

// NewOAuthATPSessionsRepository creates a new OAuth ATP sessions repository.
func NewOAuthATPSessionsRepository(db database.Executor) *OAuthATPSessionsRepository {
	return &OAuthATPSessionsRepository{db: db}
}

// Insert creates a new ATP session.
func (r *OAuthATPSessionsRepository) Insert(ctx context.Context, session *oauth.ATPSession) error {
	const sqlStr = `INSERT INTO oauth_atp_session (
		session_id, iteration, did, session_created_at, atp_oauth_state,
		signing_key_jkt, dpop_key, access_token, refresh_token,
		access_token_created_at, access_token_expires_at, access_token_scopes,
		session_exchanged_at, exchange_error
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	params := []database.Value{
		database.Text(session.SessionID),
		database.Int(session.Iteration),
		database.NullableText(session.DID),
		database.Int(session.SessionCreatedAt),
		database.Text(session.ATPOAuthState),
		database.Text(session.SigningKeyJKT),
		database.Text(session.DPoPKey),
		database.NullableText(session.AccessToken),
		database.NullableText(session.RefreshToken),
		database.NullableInt(session.AccessTokenCreatedAt),
		database.NullableInt(session.AccessTokenExpiresAt),
		database.NullableText(session.AccessTokenScopes),
		database.NullableInt(session.SessionExchangedAt),
		database.NullableText(session.ExchangeError),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// Get retrieves an ATP session by session ID and iteration.
func (r *OAuthATPSessionsRepository) Get(ctx context.Context, sessionID string, iteration int64) (*oauth.ATPSession, error) {
	const sqlStr = `SELECT session_id, iteration, did, session_created_at, atp_oauth_state,
		signing_key_jkt, dpop_key, access_token, refresh_token,
		access_token_created_at, access_token_expires_at, access_token_scopes,
		session_exchanged_at, exchange_error
	FROM oauth_atp_session WHERE session_id = $1 AND iteration = $2`

	session, err := r.scanSession(ctx, sqlStr, []database.Value{database.Text(sessionID), database.Int(iteration)})
	if err != nil {
		return nil, err
	}

	return session, nil
}

// GetLatest retrieves the latest iteration of an ATP session.
func (r *OAuthATPSessionsRepository) GetLatest(ctx context.Context, sessionID string) (*oauth.ATPSession, error) {
	const sqlStr = `SELECT session_id, iteration, did, session_created_at, atp_oauth_state,
		signing_key_jkt, dpop_key, access_token, refresh_token,
		access_token_created_at, access_token_expires_at, access_token_scopes,
		session_exchanged_at, exchange_error
	FROM oauth_atp_session WHERE session_id = $1 ORDER BY iteration DESC LIMIT 1`

	session, err := r.scanSession(ctx, sqlStr, []database.Value{database.Text(sessionID)})
	if err != nil {
		return nil, err
	}

	return session, nil
}

// GetByDID retrieves the latest ATP session for a DID.
func (r *OAuthATPSessionsRepository) GetByDID(ctx context.Context, did string) (*oauth.ATPSession, error) {
	const sqlStr = `SELECT session_id, iteration, did, session_created_at, atp_oauth_state,
		signing_key_jkt, dpop_key, access_token, refresh_token,
		access_token_created_at, access_token_expires_at, access_token_scopes,
		session_exchanged_at, exchange_error
	FROM oauth_atp_session WHERE did = $1 ORDER BY session_created_at DESC LIMIT 1`

	session, err := r.scanSession(ctx, sqlStr, []database.Value{database.Text(did)})
	if err != nil {
		return nil, err
	}

	return session, nil
}

// GetByAccessToken retrieves an ATP session by access token.
func (r *OAuthATPSessionsRepository) GetByAccessToken(ctx context.Context, accessToken string) (*oauth.ATPSession, error) {
	const sqlStr = `SELECT session_id, iteration, did, session_created_at, atp_oauth_state,
		signing_key_jkt, dpop_key, access_token, refresh_token,
		access_token_created_at, access_token_expires_at, access_token_scopes,
		session_exchanged_at, exchange_error
	FROM oauth_atp_session WHERE access_token = $1`

	session, err := r.scanSession(ctx, sqlStr, []database.Value{database.Text(accessToken)})
	if err != nil {
		return nil, err
	}

	return session, nil
}

// Update updates an existing ATP session.
func (r *OAuthATPSessionsRepository) Update(ctx context.Context, session *oauth.ATPSession) error {
	const sqlStr = `UPDATE oauth_atp_session SET
		did = $1, access_token = $2, refresh_token = $3,
		access_token_created_at = $4, access_token_expires_at = $5, access_token_scopes = $6,
		session_exchanged_at = $7, exchange_error = $8
	WHERE session_id = $9 AND iteration = $10`

	params := []database.Value{
		database.NullableText(session.DID),
		database.NullableText(session.AccessToken),
		database.NullableText(session.RefreshToken),
		database.NullableInt(session.AccessTokenCreatedAt),
		database.NullableInt(session.AccessTokenExpiresAt),
		database.NullableText(session.AccessTokenScopes),
		database.NullableInt(session.SessionExchangedAt),
		database.NullableText(session.ExchangeError),
		database.Text(session.SessionID),
		database.Int(session.Iteration),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// Delete removes an ATP session.
func (r *OAuthATPSessionsRepository) Delete(ctx context.Context, sessionID string, iteration int64) error {
	_, err := r.db.Exec(ctx,
		"DELETE FROM oauth_atp_session WHERE session_id = $1 AND iteration = $2",
		[]database.Value{database.Text(sessionID), database.Int(iteration)})
	return err
}

// DeleteAllIterations removes all iterations of an ATP session.
func (r *OAuthATPSessionsRepository) DeleteAllIterations(ctx context.Context, sessionID string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_atp_session WHERE session_id = $1",
		[]database.Value{database.Text(sessionID)})
	return err
}

// Helper to scan session from query
func (r *OAuthATPSessionsRepository) scanSession(ctx context.Context, sqlStr string, params []database.Value) (*oauth.ATPSession, error) {
	var (
		session              oauth.ATPSession
		did                  sql.NullString
		accessToken          sql.NullString
		refreshToken         sql.NullString
		accessTokenCreatedAt sql.NullInt64
		accessTokenExpiresAt sql.NullInt64
		accessTokenScopes    sql.NullString
		sessionExchangedAt   sql.NullInt64
		exchangeError        sql.NullString
	)

	err := r.db.QueryRow(ctx, sqlStr, params,
		&session.SessionID, &session.Iteration, &did, &session.SessionCreatedAt, &session.ATPOAuthState,
		&session.SigningKeyJKT, &session.DPoPKey, &accessToken, &refreshToken,
		&accessTokenCreatedAt, &accessTokenExpiresAt, &accessTokenScopes,
		&sessionExchangedAt, &exchangeError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if did.Valid {
		session.DID = &did.String
	}
	if accessToken.Valid {
		session.AccessToken = &accessToken.String
	}
	if refreshToken.Valid {
		session.RefreshToken = &refreshToken.String
	}
	if accessTokenCreatedAt.Valid {
		session.AccessTokenCreatedAt = &accessTokenCreatedAt.Int64
	}
	if accessTokenExpiresAt.Valid {
		session.AccessTokenExpiresAt = &accessTokenExpiresAt.Int64
	}
	if accessTokenScopes.Valid {
		session.AccessTokenScopes = &accessTokenScopes.String
	}
	if sessionExchangedAt.Valid {
		session.SessionExchangedAt = &sessionExchangedAt.Int64
	}
	if exchangeError.Valid {
		session.ExchangeError = &exchangeError.String
	}

	return &session, nil
}
