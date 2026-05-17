package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthClientsRepository handles OAuth client persistence.
type OAuthClientsRepository struct {
	db database.Executor
}

// NewOAuthClientsRepository creates a new OAuth clients repository.
func NewOAuthClientsRepository(db database.Executor) *OAuthClientsRepository {
	return &OAuthClientsRepository{db: db}
}

// Insert creates a new OAuth client.
func (r *OAuthClientsRepository) Insert(ctx context.Context, client *oauth.Client) error {
	redirectURIsJSON, _ := json.Marshal(client.RedirectURIs)
	grantTypesJSON, _ := json.Marshal(grantTypesToStrings(client.GrantTypes))
	responseTypesJSON, _ := json.Marshal(responseTypesToStrings(client.ResponseTypes))

	const sqlStr = `INSERT INTO oauth_client (
		client_id, client_secret, client_name, redirect_uris,
		grant_types, response_types, scope, token_endpoint_auth_method,
		client_type, created_at, updated_at, metadata,
		access_token_expiration, refresh_token_expiration,
		require_redirect_exact, registration_access_token, jwks
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, $15, $16, $17::jsonb)`

	params := []database.Value{
		database.Text(client.ClientID),
		database.NullableText(client.ClientSecret),
		database.Text(client.ClientName),
		database.Text(string(redirectURIsJSON)),
		database.Text(string(grantTypesJSON)),
		database.Text(string(responseTypesJSON)),
		database.NullableText(client.Scope),
		database.Text(string(client.TokenEndpointAuthMethod)),
		database.Text(string(client.ClientType)),
		database.Int(client.CreatedAt),
		database.Int(client.UpdatedAt),
		database.Text(client.Metadata),
		database.Int(client.AccessTokenExpiration),
		database.Int(client.RefreshTokenExpiration),
		database.Bool(client.RequireRedirectExact),
		database.NullableText(client.RegistrationAccessToken),
		database.NullableText(client.JWKS),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// Get retrieves an OAuth client by client_id.
func (r *OAuthClientsRepository) Get(ctx context.Context, clientID string) (*oauth.Client, error) {
	const sqlStr = `SELECT client_id, client_secret, client_name, redirect_uris,
		grant_types, response_types, scope, token_endpoint_auth_method,
		client_type, created_at, updated_at, metadata::text,
		access_token_expiration, refresh_token_expiration,
		require_redirect_exact, registration_access_token, jwks::text
	FROM oauth_client WHERE client_id = $1`

	var (
		client            oauth.Client
		clientSecret      sql.NullString
		redirectURIsJSON  string
		grantTypesJSON    string
		responseTypesJSON string
		scope             sql.NullString
		authMethod        string
		clientType        string
		requireExact      bool
		regToken          sql.NullString
		jwks              sql.NullString
	)

	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(clientID)},
		&client.ClientID, &clientSecret, &client.ClientName, &redirectURIsJSON,
		&grantTypesJSON, &responseTypesJSON, &scope, &authMethod,
		&clientType, &client.CreatedAt, &client.UpdatedAt, &client.Metadata,
		&client.AccessTokenExpiration, &client.RefreshTokenExpiration,
		&requireExact, &regToken, &jwks)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// Parse nullable fields
	if clientSecret.Valid {
		client.ClientSecret = &clientSecret.String
	}
	if scope.Valid {
		client.Scope = &scope.String
	}
	if regToken.Valid {
		client.RegistrationAccessToken = &regToken.String
	}
	if jwks.Valid {
		client.JWKS = &jwks.String
	}

	// Parse JSON arrays
	_ = json.Unmarshal([]byte(redirectURIsJSON), &client.RedirectURIs)

	var grantTypeStrs []string
	_ = json.Unmarshal([]byte(grantTypesJSON), &grantTypeStrs)
	client.GrantTypes = stringsToGrantTypes(grantTypeStrs)

	var responseTypeStrs []string
	_ = json.Unmarshal([]byte(responseTypesJSON), &responseTypeStrs)
	client.ResponseTypes = stringsToResponseTypes(responseTypeStrs)

	client.TokenEndpointAuthMethod = oauth.AuthMethod(authMethod)
	client.ClientType = oauth.ClientType(clientType)
	client.RequireRedirectExact = requireExact

	return &client, nil
}

// GetAll retrieves all OAuth clients (excluding internal 'admin' client).
func (r *OAuthClientsRepository) GetAll(ctx context.Context) ([]*oauth.Client, error) {
	sqlStr := `SELECT client_id, client_secret, client_name, redirect_uris,
		grant_types, response_types, scope, token_endpoint_auth_method,
		client_type, created_at, updated_at, metadata::text,
		access_token_expiration, refresh_token_expiration,
		require_redirect_exact, registration_access_token, jwks::text
	FROM oauth_client WHERE client_id != 'admin' ORDER BY created_at DESC`

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []*oauth.Client
	for rows.Next() {
		client, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}

	return clients, rows.Err()
}

// Update updates an existing OAuth client.
func (r *OAuthClientsRepository) Update(ctx context.Context, client *oauth.Client) error {
	redirectURIsJSON, _ := json.Marshal(client.RedirectURIs)
	grantTypesJSON, _ := json.Marshal(grantTypesToStrings(client.GrantTypes))
	responseTypesJSON, _ := json.Marshal(responseTypesToStrings(client.ResponseTypes))

	const sqlStr = `UPDATE oauth_client SET
		client_secret = $1, client_name = $2, redirect_uris = $3,
		grant_types = $4, response_types = $5, scope = $6,
		token_endpoint_auth_method = $7, updated_at = $8,
		metadata = $9::jsonb, access_token_expiration = $10,
		refresh_token_expiration = $11, require_redirect_exact = $12, jwks = $13::jsonb
	WHERE client_id = $14`

	params := []database.Value{
		database.NullableText(client.ClientSecret),
		database.Text(client.ClientName),
		database.Text(string(redirectURIsJSON)),
		database.Text(string(grantTypesJSON)),
		database.Text(string(responseTypesJSON)),
		database.NullableText(client.Scope),
		database.Text(string(client.TokenEndpointAuthMethod)),
		database.Int(client.UpdatedAt),
		database.Text(client.Metadata),
		database.Int(client.AccessTokenExpiration),
		database.Int(client.RefreshTokenExpiration),
		database.Bool(client.RequireRedirectExact),
		database.NullableText(client.JWKS),
		database.Text(client.ClientID),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	return err
}

// Delete removes an OAuth client.
func (r *OAuthClientsRepository) Delete(ctx context.Context, clientID string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM oauth_client WHERE client_id = $1",
		[]database.Value{database.Text(clientID)})
	return err
}

// EnsureAdminClient ensures the internal "admin" client exists.
func (r *OAuthClientsRepository) EnsureAdminClient(ctx context.Context) error {
	existing, err := r.Get(ctx, "admin")
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // Already exists
	}

	now := time.Now().Unix()
	adminClient := &oauth.Client{
		ClientID:                "admin",
		ClientName:              "Admin UI",
		RedirectURIs:            []string{},
		GrantTypes:              []oauth.GrantType{},
		ResponseTypes:           []oauth.ResponseType{},
		TokenEndpointAuthMethod: oauth.AuthNone,
		ClientType:              oauth.ClientConfidential,
		CreatedAt:               now,
		UpdatedAt:               now,
		Metadata:                "{}",
		AccessTokenExpiration:   86400 * 7,  // 7 days
		RefreshTokenExpiration:  86400 * 30, // 30 days
		RequireRedirectExact:    false,
	}

	return r.Insert(ctx, adminClient)
}

// GetCount returns the total number of OAuth clients.
func (r *OAuthClientsRepository) GetCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM oauth_client WHERE client_id != 'admin'", nil, &count)
	return count, err
}

// Helper functions

func scanOAuthClient(rows *sql.Rows) (*oauth.Client, error) {
	var (
		client            oauth.Client
		clientSecret      sql.NullString
		redirectURIsJSON  string
		grantTypesJSON    string
		responseTypesJSON string
		scope             sql.NullString
		authMethod        string
		clientType        string
		requireExact      bool
		regToken          sql.NullString
		jwks              sql.NullString
	)

	err := rows.Scan(
		&client.ClientID, &clientSecret, &client.ClientName, &redirectURIsJSON,
		&grantTypesJSON, &responseTypesJSON, &scope, &authMethod,
		&clientType, &client.CreatedAt, &client.UpdatedAt, &client.Metadata,
		&client.AccessTokenExpiration, &client.RefreshTokenExpiration,
		&requireExact, &regToken, &jwks)
	if err != nil {
		return nil, err
	}

	if clientSecret.Valid {
		client.ClientSecret = &clientSecret.String
	}
	if scope.Valid {
		client.Scope = &scope.String
	}
	if regToken.Valid {
		client.RegistrationAccessToken = &regToken.String
	}
	if jwks.Valid {
		client.JWKS = &jwks.String
	}

	_ = json.Unmarshal([]byte(redirectURIsJSON), &client.RedirectURIs)

	var grantTypeStrs []string
	_ = json.Unmarshal([]byte(grantTypesJSON), &grantTypeStrs)
	client.GrantTypes = stringsToGrantTypes(grantTypeStrs)

	var responseTypeStrs []string
	_ = json.Unmarshal([]byte(responseTypesJSON), &responseTypeStrs)
	client.ResponseTypes = stringsToResponseTypes(responseTypeStrs)

	client.TokenEndpointAuthMethod = oauth.AuthMethod(authMethod)
	client.ClientType = oauth.ClientType(clientType)
	client.RequireRedirectExact = requireExact

	return &client, nil
}

func grantTypesToStrings(gts []oauth.GrantType) []string {
	result := make([]string, len(gts))
	for i, gt := range gts {
		result[i] = string(gt)
	}
	return result
}

func stringsToGrantTypes(strs []string) []oauth.GrantType {
	result := make([]oauth.GrantType, len(strs))
	for i, s := range strs {
		result[i] = oauth.GrantType(s)
	}
	return result
}

func responseTypesToStrings(rts []oauth.ResponseType) []string {
	result := make([]string, len(rts))
	for i, rt := range rts {
		result[i] = string(rt)
	}
	return result
}

func stringsToResponseTypes(strs []string) []oauth.ResponseType {
	result := make([]oauth.ResponseType, len(strs))
	for i, s := range strs {
		result[i] = oauth.ResponseType(s)
	}
	return result
}
