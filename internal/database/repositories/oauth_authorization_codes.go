package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthAuthorizationCodesRepository handles OAuth authorization code persistence.
type OAuthAuthorizationCodesRepository struct {
	db database.Executor
}

// NewOAuthAuthorizationCodesRepository creates a new OAuth authorization codes repository.
func NewOAuthAuthorizationCodesRepository(db database.Executor) *OAuthAuthorizationCodesRepository {
	return &OAuthAuthorizationCodesRepository{db: db}
}

// Insert creates a new OAuth authorization code.
func (r *OAuthAuthorizationCodesRepository) Insert(ctx context.Context, code *oauth.AuthorizationCode) error {
	_, err := r.db.Exec(ctx, `INSERT INTO oauth_authorization_code (
		code, client_id, user_id, session_id, session_iteration, redirect_uri,
		scope, code_challenge, code_challenge_method, nonce, created_at, expires_at, used
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		[]database.Value{
			database.Text(code.Code),
			database.Text(code.ClientID),
			database.Text(code.UserID),
			database.NullableText(code.SessionID),
			database.NullableInt(code.SessionIteration),
			database.Text(code.RedirectURI),
			database.NullableText(code.Scope),
			database.NullableText(code.CodeChallenge),
			database.NullableText(code.CodeChallengeMethod),
			database.NullableText(code.Nonce),
			database.Int(code.CreatedAt),
			database.Int(code.ExpiresAt),
			database.Bool(code.Used),
		})
	return err
}

// Get retrieves an OAuth authorization code by code string.
func (r *OAuthAuthorizationCodesRepository) Get(ctx context.Context, codeStr string) (*oauth.AuthorizationCode, error) {
	var (
		code                oauth.AuthorizationCode
		sessionID           sql.NullString
		sessionIteration    sql.NullInt64
		scope               sql.NullString
		codeChallenge       sql.NullString
		codeChallengeMethod sql.NullString
		nonce               sql.NullString
		used                bool
	)

	err := r.db.QueryRow(ctx, `SELECT code, client_id, user_id, session_id, session_iteration, redirect_uri,
		scope, code_challenge, code_challenge_method, nonce, created_at, expires_at, used
	FROM oauth_authorization_code WHERE code = $1`,
		[]database.Value{database.Text(codeStr)},
		&code.Code, &code.ClientID, &code.UserID, &sessionID, &sessionIteration, &code.RedirectURI,
		&scope, &codeChallenge, &codeChallengeMethod, &nonce, &code.CreatedAt, &code.ExpiresAt, &used)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if sessionID.Valid {
		code.SessionID = &sessionID.String
	}
	if sessionIteration.Valid {
		code.SessionIteration = &sessionIteration.Int64
	}
	if scope.Valid {
		code.Scope = &scope.String
	}
	if codeChallenge.Valid {
		code.CodeChallenge = &codeChallenge.String
	}
	if codeChallengeMethod.Valid {
		code.CodeChallengeMethod = &codeChallengeMethod.String
	}
	if nonce.Valid {
		code.Nonce = &nonce.String
	}
	code.Used = used

	return &code, nil
}

// MarkUsed marks an authorization code as used.
func (r *OAuthAuthorizationCodesRepository) MarkUsed(ctx context.Context, codeStr string) error {
	_, err := r.db.Exec(ctx, "UPDATE oauth_authorization_code SET used = true WHERE code = $1",
		[]database.Value{database.Text(codeStr)})
	return err
}

// Delete removes an authorization code.
func (r *OAuthAuthorizationCodesRepository) Delete(ctx context.Context, codeStr string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_authorization_code WHERE code = $1",
		[]database.Value{database.Text(codeStr)})
	return err
}

// DeleteExpired removes all expired authorization codes.
func (r *OAuthAuthorizationCodesRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_authorization_code WHERE expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}
