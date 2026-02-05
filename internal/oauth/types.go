// Package oauth provides AT Protocol OAuth implementation.
package oauth

import "time"

// GrantType represents an OAuth grant type.
type GrantType string

const (
	GrantAuthorizationCode GrantType = "authorization_code"
	GrantRefreshToken      GrantType = "refresh_token"
	GrantClientCredentials GrantType = "client_credentials"
)

// ResponseType represents an OAuth response type.
type ResponseType string

const (
	ResponseCode  ResponseType = "code"
	ResponseToken ResponseType = "token"
)

// TokenType represents an OAuth token type.
type TokenType string

const (
	TokenBearer TokenType = "Bearer"
	TokenDPoP   TokenType = "DPoP"
)

// ClientType represents an OAuth client type.
type ClientType string

const (
	ClientPublic       ClientType = "public"
	ClientConfidential ClientType = "confidential"
)

// AuthMethod represents a token endpoint auth method.
type AuthMethod string

const (
	AuthNone          AuthMethod = "none"
	AuthClientSecret  AuthMethod = "client_secret_basic"
	AuthClientPost    AuthMethod = "client_secret_post"
	AuthPrivateKeyJWT AuthMethod = "private_key_jwt"
)

// Client represents an OAuth client (registered application).
type Client struct {
	ClientID                string
	ClientSecret            *string
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []GrantType
	ResponseTypes           []ResponseType
	Scope                   *string
	TokenEndpointAuthMethod AuthMethod
	ClientType              ClientType
	CreatedAt               int64
	UpdatedAt               int64
	Metadata                string
	AccessTokenExpiration   int64
	RefreshTokenExpiration  int64
	RequireRedirectExact    bool
	RegistrationAccessToken *string
	JWKS                    *string
}

// AccessToken represents an OAuth access token.
type AccessToken struct {
	Token            string
	TokenType        TokenType
	ClientID         string
	UserID           *string
	SessionID        *string
	SessionIteration *int64
	Scope            *string
	CreatedAt        int64
	ExpiresAt        int64
	Revoked          bool
	DPoPJKT          *string
}

// IsExpired returns true if the token has expired.
func (t *AccessToken) IsExpired() bool {
	return time.Now().Unix() > t.ExpiresAt
}

// RefreshToken represents an OAuth refresh token.
type RefreshToken struct {
	Token            string
	AccessToken      string
	ClientID         string
	UserID           string
	SessionID        *string
	SessionIteration *int64
	Scope            *string
	CreatedAt        int64
	ExpiresAt        *int64
	Revoked          bool
}

// IsExpired returns true if the token has expired (if it has an expiry).
func (t *RefreshToken) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().Unix() > *t.ExpiresAt
}

// AuthorizationCode represents an OAuth authorization code.
type AuthorizationCode struct {
	Code                string
	ClientID            string
	UserID              string
	SessionID           *string
	SessionIteration    *int64
	RedirectURI         string
	Scope               *string
	CodeChallenge       *string
	CodeChallengeMethod *string
	Nonce               *string
	CreatedAt           int64
	ExpiresAt           int64
	Used                bool
}

// IsExpired returns true if the code has expired.
func (c *AuthorizationCode) IsExpired() bool {
	return time.Now().Unix() > c.ExpiresAt
}

// PARRequest represents a Pushed Authorization Request.
type PARRequest struct {
	RequestURI           string
	AuthorizationRequest string
	ClientID             string
	CreatedAt            int64
	ExpiresAt            int64
	Subject              *string
	Metadata             string
}

// IsExpired returns true if the PAR has expired.
func (r *PARRequest) IsExpired() bool {
	return time.Now().Unix() > r.ExpiresAt
}

// DPoPNonce represents a DPoP nonce for replay protection.
type DPoPNonce struct {
	Nonce     string
	ExpiresAt int64
}

// IsExpired returns true if the nonce has expired.
func (n *DPoPNonce) IsExpired() bool {
	return time.Now().Unix() > n.ExpiresAt
}

// DPoPJTI represents a DPoP JTI for replay protection.
type DPoPJTI struct {
	JTI       string
	CreatedAt int64
}

// AuthRequest represents an OAuth authorization request (client flow state).
type AuthRequest struct {
	SessionID           string
	ClientID            string
	RedirectURI         string
	Scope               *string
	State               *string
	CodeChallenge       *string
	CodeChallengeMethod *string
	ResponseType        string
	Nonce               *string
	LoginHint           *string
	CreatedAt           int64
	ExpiresAt           int64
}

// IsExpired returns true if the request has expired.
func (r *AuthRequest) IsExpired() bool {
	return time.Now().Unix() > r.ExpiresAt
}

// ATPSession represents a bridge session to AT Protocol.
type ATPSession struct {
	SessionID            string
	Iteration            int64
	DID                  *string
	SessionCreatedAt     int64
	ATPOAuthState        string
	SigningKeyJKT        string
	DPoPKey              string
	AccessToken          *string
	RefreshToken         *string
	AccessTokenCreatedAt *int64
	AccessTokenExpiresAt *int64
	AccessTokenScopes    *string
	SessionExchangedAt   *int64
	ExchangeError        *string
}

// ATPRequest represents an outbound OAuth request to AT Protocol.
type ATPRequest struct {
	OAuthState          string
	AuthorizationServer string
	Nonce               string
	PKCEVerifier        string
	SigningPublicKey    string
	DPoPPrivateKey      string
	CreatedAt           int64
	ExpiresAt           int64
}

// IsExpired returns true if the request has expired.
func (r *ATPRequest) IsExpired() bool {
	return time.Now().Unix() > r.ExpiresAt
}

// AdminSession represents an admin browser session.
type AdminSession struct {
	SessionID    string
	ATPSessionID string
	CreatedAt    int64
}
