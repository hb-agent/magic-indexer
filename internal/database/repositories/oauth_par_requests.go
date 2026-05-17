package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthPARRequestsRepository handles Pushed Authorization Request persistence.
type OAuthPARRequestsRepository struct {
	db database.Executor
}

// NewOAuthPARRequestsRepository creates a new OAuth PAR requests repository.
func NewOAuthPARRequestsRepository(db database.Executor) *OAuthPARRequestsRepository {
	return &OAuthPARRequestsRepository{db: db}
}

// Insert creates a new PAR request.
func (r *OAuthPARRequestsRepository) Insert(ctx context.Context, par *oauth.PARRequest) error {
	_, err := r.db.Exec(ctx, `INSERT INTO oauth_par_request (
		request_uri, authorization_request, client_id, created_at, expires_at, subject, metadata
	) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`,
		[]database.Value{
			database.Text(par.RequestURI),
			database.Text(par.AuthorizationRequest),
			database.Text(par.ClientID),
			database.Int(par.CreatedAt),
			database.Int(par.ExpiresAt),
			database.NullableText(par.Subject),
			database.Text(par.Metadata),
		})
	return err
}

// Get retrieves a PAR request by request URI.
func (r *OAuthPARRequestsRepository) Get(ctx context.Context, requestURI string) (*oauth.PARRequest, error) {
	var (
		par     oauth.PARRequest
		subject sql.NullString
	)

	err := r.db.QueryRow(ctx, `SELECT request_uri, authorization_request, client_id, created_at, expires_at, subject, metadata::text
	FROM oauth_par_request WHERE request_uri = $1`,
		[]database.Value{database.Text(requestURI)},
		&par.RequestURI, &par.AuthorizationRequest, &par.ClientID, &par.CreatedAt, &par.ExpiresAt, &subject, &par.Metadata)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if subject.Valid {
		par.Subject = &subject.String
	}

	return &par, nil
}

// UpdateSubject sets the subject on a PAR request (after user authenticates).
func (r *OAuthPARRequestsRepository) UpdateSubject(ctx context.Context, requestURI, subject string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE oauth_par_request SET subject = $1 WHERE request_uri = $2",
		[]database.Value{database.Text(subject), database.Text(requestURI)})
	return err
}

// Delete removes a PAR request.
func (r *OAuthPARRequestsRepository) Delete(ctx context.Context, requestURI string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_par_request WHERE request_uri = $1",
		[]database.Value{database.Text(requestURI)})
	return err
}

// DeleteExpired removes all expired PAR requests.
func (r *OAuthPARRequestsRepository) DeleteExpired(ctx context.Context, beforeTimestamp int64) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_par_request WHERE expires_at < $1",
		[]database.Value{database.Int(beforeTimestamp)})
	return err
}
