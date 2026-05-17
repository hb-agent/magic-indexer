package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthAccessTokensRepository handles OAuth access token persistence.
type OAuthAccessTokensRepository struct {
	db database.Executor
}

// NewOAuthAccessTokensRepository creates a new OAuth access tokens repository.
func NewOAuthAccessTokensRepository(db database.Executor) *OAuthAccessTokensRepository {
	return &OAuthAccessTokensRepository{db: db}
}

// Insert creates a new OAuth access token.
func (r *OAuthAccessTokensRepository) Insert(ctx context.Context, token *oauth.AccessToken) error {
	_, err := r.db.Exec(ctx, `INSERT INTO oauth_access_token (
		token, token_type, client_id, user_id, session_id,
		session_iteration, scope, created_at, expires_at, revoked, dpop_jkt
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		[]database.Value{
			database.Text(token.Token),
			database.Text(string(token.TokenType)),
			database.Text(token.ClientID),
			database.NullableText(token.UserID),
			database.NullableText(token.SessionID),
			database.NullableInt(token.SessionIteration),
			database.NullableText(token.Scope),
			database.Int(token.CreatedAt),
			database.Int(token.ExpiresAt),
			database.Bool(token.Revoked),
			database.NullableText(token.DPoPJKT),
		})
	return err
}

// Get retrieves an OAuth access token by token string.
func (r *OAuthAccessTokensRepository) Get(ctx context.Context, tokenStr string) (*oauth.AccessToken, error) {
	var (
		token            oauth.AccessToken
		tokenType        string
		userID           sql.NullString
		sessionID        sql.NullString
		sessionIteration sql.NullInt64
		scope            sql.NullString
		revoked          bool
		dpopJKT          sql.NullString
	)

	err := r.db.QueryRow(ctx, `SELECT token, token_type, client_id, user_id, session_id,
		session_iteration, scope, created_at, expires_at, revoked, dpop_jkt
	FROM oauth_access_token WHERE token = $1`,
		[]database.Value{database.Text(tokenStr)},
		&token.Token, &tokenType, &token.ClientID, &userID, &sessionID,
		&sessionIteration, &scope, &token.CreatedAt, &token.ExpiresAt, &revoked, &dpopJKT)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	token.TokenType = oauth.TokenType(tokenType)
	if userID.Valid {
		token.UserID = &userID.String
	}
	if sessionID.Valid {
		token.SessionID = &sessionID.String
	}
	if sessionIteration.Valid {
		token.SessionIteration = &sessionIteration.Int64
	}
	if scope.Valid {
		token.Scope = &scope.String
	}
	token.Revoked = revoked
	if dpopJKT.Valid {
		token.DPoPJKT = &dpopJKT.String
	}

	return &token, nil
}

// GetByUserID retrieves all access tokens for a user.
func (r *OAuthAccessTokensRepository) GetByUserID(ctx context.Context, userID string) ([]*oauth.AccessToken, error) {
	rows, err := r.db.DB().QueryContext(ctx, `SELECT token, token_type, client_id, user_id, session_id,
		session_iteration, scope, created_at, expires_at, revoked, dpop_jkt
	FROM oauth_access_token WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*oauth.AccessToken
	for rows.Next() {
		token, err := scanAccessToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

// GetByDPoPJKT retrieves all access tokens for a DPoP JKT.
func (r *OAuthAccessTokensRepository) GetByDPoPJKT(ctx context.Context, jkt string) ([]*oauth.AccessToken, error) {
	rows, err := r.db.DB().QueryContext(ctx, `SELECT token, token_type, client_id, user_id, session_id,
		session_iteration, scope, created_at, expires_at, revoked, dpop_jkt
	FROM oauth_access_token WHERE dpop_jkt = $1 ORDER BY created_at DESC`, jkt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*oauth.AccessToken
	for rows.Next() {
		token, err := scanAccessToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

// Revoke marks an access token as revoked.
func (r *OAuthAccessTokensRepository) Revoke(ctx context.Context, tokenStr string) error {
	_, err := r.db.Exec(ctx, "UPDATE oauth_access_token SET revoked = true WHERE token = $1",
		[]database.Value{database.Text(tokenStr)})
	return err
}

// RevokeByUserID revokes all access tokens for a user.
func (r *OAuthAccessTokensRepository) RevokeByUserID(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, "UPDATE oauth_access_token SET revoked = true WHERE user_id = $1",
		[]database.Value{database.Text(userID)})
	return err
}

// Delete removes an access token.
func (r *OAuthAccessTokensRepository) Delete(ctx context.Context, tokenStr string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_access_token WHERE token = $1",
		[]database.Value{database.Text(tokenStr)})
	return err
}

// DeleteExpired removes all expired access tokens.
func (r *OAuthAccessTokensRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_access_token WHERE expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}

// Helper function to scan access token rows
func scanAccessToken(rows *sql.Rows) (*oauth.AccessToken, error) {
	var (
		token            oauth.AccessToken
		tokenType        string
		userID           sql.NullString
		sessionID        sql.NullString
		sessionIteration sql.NullInt64
		scope            sql.NullString
		revoked          bool
		dpopJKT          sql.NullString
	)

	err := rows.Scan(
		&token.Token, &tokenType, &token.ClientID, &userID, &sessionID,
		&sessionIteration, &scope, &token.CreatedAt, &token.ExpiresAt, &revoked, &dpopJKT)
	if err != nil {
		return nil, err
	}

	token.TokenType = oauth.TokenType(tokenType)
	if userID.Valid {
		token.UserID = &userID.String
	}
	if sessionID.Valid {
		token.SessionID = &sessionID.String
	}
	if sessionIteration.Valid {
		token.SessionIteration = &sessionIteration.Int64
	}
	if scope.Valid {
		token.Scope = &scope.String
	}
	token.Revoked = revoked
	if dpopJKT.Valid {
		token.DPoPJKT = &dpopJKT.String
	}

	return &token, nil
}
