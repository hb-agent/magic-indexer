package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthRefreshTokensRepository handles OAuth refresh token persistence.
type OAuthRefreshTokensRepository struct {
	db database.Executor
}

// NewOAuthRefreshTokensRepository creates a new OAuth refresh tokens repository.
func NewOAuthRefreshTokensRepository(db database.Executor) *OAuthRefreshTokensRepository {
	return &OAuthRefreshTokensRepository{db: db}
}

// refreshTokenColumns is the canonical column list for SELECT statements.
// Kept here as a single source of truth so adding a column means editing
// this constant and the scanner, not cascading updates across call sites.
const refreshTokenColumns = `token, access_token, client_id, user_id, session_id,
	session_iteration, scope, created_at, expires_at, revoked, dpop_jkt, original_issued_at`

// refreshTokenScanner abstracts *sql.Row and *sql.Rows for the shared scan helper.
type refreshTokenScanner interface {
	Scan(dest ...any) error
}

// scanRefreshTokenRow populates a RefreshToken from a row produced by selecting
// refreshTokenColumns. Single-writer for the refresh-token row layout.
func scanRefreshTokenRow(r refreshTokenScanner) (*oauth.RefreshToken, error) {
	var (
		token            oauth.RefreshToken
		sessionID        sql.NullString
		sessionIteration sql.NullInt64
		scope            sql.NullString
		expiresAt        sql.NullInt64
		dpopJKT          sql.NullString
		originalIssuedAt sql.NullInt64
	)

	if err := r.Scan(
		&token.Token, &token.AccessToken, &token.ClientID, &token.UserID, &sessionID,
		&sessionIteration, &scope, &token.CreatedAt, &expiresAt, &token.Revoked,
		&dpopJKT, &originalIssuedAt,
	); err != nil {
		return nil, err
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
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Int64
	}
	if dpopJKT.Valid {
		token.DPoPJKT = dpopJKT.String
	}
	if originalIssuedAt.Valid {
		token.OriginalIssuedAt = originalIssuedAt.Int64
	} else {
		// Legacy tokens predate this column. Fall back to created_at so the
		// sunset cutoff check behaves as "token is as old as its creation time."
		token.OriginalIssuedAt = token.CreatedAt
	}

	return &token, nil
}

// Insert creates a new OAuth refresh token.
func (r *OAuthRefreshTokensRepository) Insert(ctx context.Context, token *oauth.RefreshToken) error {
	// If the caller hasn't set OriginalIssuedAt, default to CreatedAt — this is
	// the first token in a chain.
	originalIssuedAt := token.OriginalIssuedAt
	if originalIssuedAt == 0 {
		originalIssuedAt = token.CreatedAt
	}

	const sqlStr = `INSERT INTO oauth_refresh_token (
		token, access_token, client_id, user_id, session_id,
		session_iteration, scope, created_at, expires_at, revoked,
		dpop_jkt, original_issued_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	params := []database.Value{
		database.Text(token.Token),
		database.Text(token.AccessToken),
		database.Text(token.ClientID),
		database.Text(token.UserID),
		database.NullableText(token.SessionID),
		database.NullableInt(token.SessionIteration),
		database.NullableText(token.Scope),
		database.Int(token.CreatedAt),
		database.NullableInt(token.ExpiresAt),
		database.Bool(token.Revoked),
		database.NullableText(nilIfEmpty(token.DPoPJKT)),
		database.Int(originalIssuedAt),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// Get retrieves an OAuth refresh token by token string.
func (r *OAuthRefreshTokensRepository) Get(ctx context.Context, tokenStr string) (*oauth.RefreshToken, error) {
	sqlStr := `SELECT ` + refreshTokenColumns + ` FROM oauth_refresh_token WHERE token = $1`

	row := r.db.DB().QueryRowContext(ctx, sqlStr, tokenStr)
	tok, err := scanRefreshTokenRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return tok, nil
}

// GetByAccessToken retrieves a refresh token by its associated access token.
func (r *OAuthRefreshTokensRepository) GetByAccessToken(ctx context.Context, accessToken string) (*oauth.RefreshToken, error) {
	sqlStr := `SELECT ` + refreshTokenColumns + ` FROM oauth_refresh_token WHERE access_token = $1`

	row := r.db.DB().QueryRowContext(ctx, sqlStr, accessToken)
	tok, err := scanRefreshTokenRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return tok, nil
}

// GetByUserID retrieves all refresh tokens for a user.
func (r *OAuthRefreshTokensRepository) GetByUserID(ctx context.Context, userID string) ([]*oauth.RefreshToken, error) {
	sqlStr := `SELECT ` + refreshTokenColumns + ` FROM oauth_refresh_token WHERE user_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*oauth.RefreshToken
	for rows.Next() {
		tok, err := scanRefreshTokenRow(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	return tokens, rows.Err()
}

// Revoke marks a refresh token as revoked.
func (r *OAuthRefreshTokensRepository) Revoke(ctx context.Context, tokenStr string) error {
	_, err := r.db.Exec(ctx, "UPDATE oauth_refresh_token SET revoked = true WHERE token = $1",
		[]database.Value{database.Text(tokenStr)})
	return err
}

// RevokeByUserID revokes all refresh tokens for a user.
func (r *OAuthRefreshTokensRepository) RevokeByUserID(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, "UPDATE oauth_refresh_token SET revoked = true WHERE user_id = $1",
		[]database.Value{database.Text(userID)})
	return err
}

// Delete removes a refresh token.
func (r *OAuthRefreshTokensRepository) Delete(ctx context.Context, tokenStr string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_refresh_token WHERE token = $1",
		[]database.Value{database.Text(tokenStr)})
	return err
}

// DeleteExpired removes all expired refresh tokens.
func (r *OAuthRefreshTokensRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx,
		"DELETE FROM oauth_refresh_token WHERE expires_at IS NOT NULL AND expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}

// nilIfEmpty returns nil for empty strings so NullableText produces NULL.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
