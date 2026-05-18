// Package oauth provides AT Protocol OAuth implementation.
// OAuth error types per RFC 6749.
package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Standard OAuth error codes per RFC 6749.
const (
	ErrorInvalidRequest          = "invalid_request"
	ErrorUnauthorizedClient      = "unauthorized_client"
	ErrorAccessDenied            = "access_denied"
	ErrorUnsupportedResponseType = "unsupported_response_type"
	ErrorInvalidScope            = "invalid_scope"
	ErrorServerError             = "server_error"
	ErrorTemporarilyUnavailable  = "temporarily_unavailable"

	// Token endpoint errors
	ErrorInvalidClient        = "invalid_client"
	ErrorInvalidGrant         = "invalid_grant"
	ErrorUnsupportedGrantType = "unsupported_grant_type"

	// DPoP errors
	ErrorInvalidDPoPProof = "invalid_dpop_proof"
	ErrorUseDPoPNonce     = "use_dpop_nonce"
)

// Error represents an OAuth error response.
//
//nolint:revive // OAuthError is the established name for this type
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	URI         string `json:"error_uri,omitempty"`
	State       string `json:"state,omitempty"`
}

// Error implements the error interface.
func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// JSON returns the error as a JSON string.
func (e *OAuthError) JSON() string {
	data, _ := json.Marshal(e)
	return string(data)
}

// HTTPStatus returns the appropriate HTTP status code for the error.
func (e *OAuthError) HTTPStatus() int {
	switch e.Code {
	case ErrorInvalidClient, ErrorUnauthorizedClient:
		return http.StatusUnauthorized
	case ErrorAccessDenied:
		return http.StatusForbidden
	case ErrorServerError:
		return http.StatusInternalServerError
	case ErrorTemporarilyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

// NewOAuthError creates a new OAuth error.
func NewOAuthError(code, description string) *OAuthError {
	return &OAuthError{
		Code:        code,
		Description: description,
	}
}

// NewOAuthErrorWithState creates a new OAuth error with state.
func NewOAuthErrorWithState(code, description, state string) *OAuthError {
	return &OAuthError{
		Code:        code,
		Description: description,
		State:       state,
	}
}

// Error constructors for common errors.

// InvalidRequest creates an invalid_request error.
func InvalidRequest(description string) *OAuthError {
	return NewOAuthError(ErrorInvalidRequest, description)
}

// UnauthorizedClient creates an unauthorized_client error.
func UnauthorizedClient(description string) *OAuthError {
	return NewOAuthError(ErrorUnauthorizedClient, description)
}

// AccessDenied creates an access_denied error.
func AccessDenied(description string) *OAuthError {
	return NewOAuthError(ErrorAccessDenied, description)
}

// UnsupportedResponseType creates an unsupported_response_type error.
func UnsupportedResponseType(description string) *OAuthError {
	return NewOAuthError(ErrorUnsupportedResponseType, description)
}

// InvalidScope creates an invalid_scope error.
func InvalidScope(description string) *OAuthError {
	return NewOAuthError(ErrorInvalidScope, description)
}

// ServerError creates a server_error error.
func ServerError(description string) *OAuthError {
	return NewOAuthError(ErrorServerError, description)
}

// TemporarilyUnavailable creates a temporarily_unavailable error.
func TemporarilyUnavailable(description string) *OAuthError {
	return NewOAuthError(ErrorTemporarilyUnavailable, description)
}

// InvalidClient creates an invalid_client error.
func InvalidClient(description string) *OAuthError {
	return NewOAuthError(ErrorInvalidClient, description)
}

// InvalidGrant creates an invalid_grant error.
func InvalidGrant(description string) *OAuthError {
	return NewOAuthError(ErrorInvalidGrant, description)
}

// UnsupportedGrantType creates an unsupported_grant_type error.
func UnsupportedGrantType(description string) *OAuthError {
	return NewOAuthError(ErrorUnsupportedGrantType, description)
}

// InvalidDPoPProof creates an invalid_dpop_proof error.
func InvalidDPoPProof(description string) *OAuthError {
	return NewOAuthError(ErrorInvalidDPoPProof, description)
}

// UseDPoPNonce creates a use_dpop_nonce error (requires retry with nonce).
func UseDPoPNonce(description string) *OAuthError {
	return NewOAuthError(ErrorUseDPoPNonce, description)
}

// WriteErrorResponse writes an OAuth error as an HTTP response.
func WriteErrorResponse(w http.ResponseWriter, err *OAuthError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(err.HTTPStatus())
	_, _ = w.Write([]byte(err.JSON()))
}
