package admin

// Admin resolvers for OAuth client CRUD (registration, update,
// delete, list). The AddAdmin / RemoveAdmin mutations live in
// resolvers.go alongside Settings / UpdateSettings — they mutate
// the same admin_dids configuration string, not OAuth clients.
// Extracted from resolvers.go in 2026-05-17 Track 5; see
// docs/review-2026-05-17/plan.md and the R2.3 plan-review item.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/GainForest/hypergoat/internal/oauth"
)

// OAuthClients returns all OAuth client registrations.
func (r *Resolver) OAuthClients(ctx context.Context) ([]map[string]interface{}, error) {
	clients, err := r.repos.OAuthClients.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth clients: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(clients))
	for _, client := range clients {
		result = append(result, map[string]interface{}{
			"clientId":     client.ClientID,
			"clientSecret": client.ClientSecret,
			"clientName":   client.ClientName,
			"clientType":   string(client.ClientType),
			"redirectUris": client.RedirectURIs,
			"scope":        client.Scope,
			"createdAt":    client.CreatedAt,
		})
	}

	return result, nil
}

// CreateOAuthClient creates a new OAuth client.
func (r *Resolver) CreateOAuthClient(ctx context.Context, clientName, clientType string, redirectURIs []string) (map[string]interface{}, error) {
	// Generate client ID
	clientIDBytes := make([]byte, 16)
	if _, err := rand.Read(clientIDBytes); err != nil {
		return nil, fmt.Errorf("failed to generate client ID: %w", err)
	}
	clientID := hex.EncodeToString(clientIDBytes)

	// Generate client secret for confidential clients
	var clientSecret *string
	ct := oauth.ClientType(clientType)
	if ct == oauth.ClientConfidential {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, fmt.Errorf("failed to generate client secret: %w", err)
		}
		secret := hex.EncodeToString(secretBytes)
		clientSecret = &secret
	}

	now := time.Now().Unix()
	client := &oauth.Client{
		ClientID:                clientID,
		ClientSecret:            clientSecret,
		ClientName:              clientName,
		RedirectURIs:            redirectURIs,
		GrantTypes:              []oauth.GrantType{oauth.GrantAuthorizationCode, oauth.GrantRefreshToken},
		ResponseTypes:           []oauth.ResponseType{oauth.ResponseCode},
		TokenEndpointAuthMethod: oauth.AuthClientSecret,
		ClientType:              ct,
		CreatedAt:               now,
		UpdatedAt:               now,
		Metadata:                "{}",
		AccessTokenExpiration:   3600,       // 1 hour
		RefreshTokenExpiration:  86400 * 30, // 30 days
		RequireRedirectExact:    true,
	}

	if ct == oauth.ClientPublic {
		client.TokenEndpointAuthMethod = oauth.AuthNone
	}

	if err := r.repos.OAuthClients.Insert(ctx, client); err != nil {
		return nil, fmt.Errorf("failed to create OAuth client: %w", err)
	}

	result := map[string]interface{}{
		"clientId":     client.ClientID,
		"clientName":   client.ClientName,
		"clientType":   string(client.ClientType),
		"redirectUris": client.RedirectURIs,
		"createdAt":    client.CreatedAt,
		"scope":        client.Scope,
	}
	if client.ClientSecret != nil {
		result["clientSecret"] = *client.ClientSecret
	}

	return result, nil
}

// UpdateOAuthClient updates an existing OAuth client.
func (r *Resolver) UpdateOAuthClient(ctx context.Context, clientID, clientName string, redirectURIs []string) (map[string]interface{}, error) {
	// Get existing client
	client, err := r.repos.OAuthClients.Get(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth client: %w", err)
	}
	if client == nil {
		return nil, fmt.Errorf("OAuth client not found")
	}

	// Update fields
	client.ClientName = clientName
	client.RedirectURIs = redirectURIs
	client.UpdatedAt = time.Now().Unix()

	if err := r.repos.OAuthClients.Update(ctx, client); err != nil {
		return nil, fmt.Errorf("failed to update OAuth client: %w", err)
	}

	result := map[string]interface{}{
		"clientId":     client.ClientID,
		"clientName":   client.ClientName,
		"clientType":   string(client.ClientType),
		"redirectUris": client.RedirectURIs,
		"createdAt":    client.CreatedAt,
		"scope":        client.Scope,
	}
	if client.ClientSecret != nil {
		result["clientSecret"] = *client.ClientSecret
	}

	return result, nil
}

// DeleteOAuthClient deletes an OAuth client.
func (r *Resolver) DeleteOAuthClient(ctx context.Context, clientID string) (bool, error) {
	// Don't allow deleting the admin client
	if clientID == "admin" {
		return false, fmt.Errorf("cannot delete the admin client")
	}

	if err := r.repos.OAuthClients.Delete(ctx, clientID); err != nil {
		return false, fmt.Errorf("failed to delete OAuth client: %w", err)
	}

	return true, nil
}
