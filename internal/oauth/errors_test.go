package oauth

import (
	"net/http"
	"strings"
	"testing"
)

func TestOAuthError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *OAuthError
		want string
	}{
		{
			name: "with description",
			err:  NewOAuthError("invalid_request", "Missing parameter"),
			want: "invalid_request: Missing parameter",
		},
		{
			name: "without description",
			err:  &OAuthError{Code: "invalid_request"},
			want: "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOAuthError_JSON(t *testing.T) {
	err := NewOAuthError("invalid_request", "Missing parameter")
	json := err.JSON()

	if !strings.Contains(json, `"error":"invalid_request"`) {
		t.Errorf("JSON() missing error code: %s", json)
	}
	if !strings.Contains(json, `"error_description":"Missing parameter"`) {
		t.Errorf("JSON() missing error description: %s", json)
	}
}

func TestOAuthError_HTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		errorCode  string
		wantStatus int
	}{
		{
			name:       "invalid_client",
			errorCode:  ErrorInvalidClient,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unauthorized_client",
			errorCode:  ErrorUnauthorizedClient,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "access_denied",
			errorCode:  ErrorAccessDenied,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "server_error",
			errorCode:  ErrorServerError,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "temporarily_unavailable",
			errorCode:  ErrorTemporarilyUnavailable,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "invalid_request (default)",
			errorCode:  ErrorInvalidRequest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid_grant (default)",
			errorCode:  ErrorInvalidGrant,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewOAuthError(tt.errorCode, "description")
			got := err.HTTPStatus()
			if got != tt.wantStatus {
				t.Errorf("HTTPStatus() = %d, want %d", got, tt.wantStatus)
			}
		})
	}
}

func TestErrorConstructors(t *testing.T) {
	tests := []struct {
		name     string
		fn       func(string) *OAuthError
		wantCode string
	}{
		{"InvalidRequest", InvalidRequest, ErrorInvalidRequest},
		{"UnauthorizedClient", UnauthorizedClient, ErrorUnauthorizedClient},
		{"AccessDenied", AccessDenied, ErrorAccessDenied},
		{"UnsupportedResponseType", UnsupportedResponseType, ErrorUnsupportedResponseType},
		{"InvalidScope", InvalidScope, ErrorInvalidScope},
		{"ServerError", ServerError, ErrorServerError},
		{"TemporarilyUnavailable", TemporarilyUnavailable, ErrorTemporarilyUnavailable},
		{"InvalidClient", InvalidClient, ErrorInvalidClient},
		{"InvalidGrant", InvalidGrant, ErrorInvalidGrant},
		{"UnsupportedGrantType", UnsupportedGrantType, ErrorUnsupportedGrantType},
		{"InvalidDPoPProof", InvalidDPoPProof, ErrorInvalidDPoPProof},
		{"UseDPoPNonce", UseDPoPNonce, ErrorUseDPoPNonce},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn("test description")
			if err.Code != tt.wantCode {
				t.Errorf("%s() Code = %q, want %q", tt.name, err.Code, tt.wantCode)
			}
			if err.Description != "test description" {
				t.Errorf("%s() Description = %q, want %q", tt.name, err.Description, "test description")
			}
		})
	}
}

func TestNewOAuthErrorWithState(t *testing.T) {
	err := NewOAuthErrorWithState("invalid_request", "Missing parameter", "abc123")

	if err.Code != "invalid_request" {
		t.Errorf("Code = %q, want %q", err.Code, "invalid_request")
	}
	if err.Description != "Missing parameter" {
		t.Errorf("Description = %q, want %q", err.Description, "Missing parameter")
	}
	if err.State != "abc123" {
		t.Errorf("State = %q, want %q", err.State, "abc123")
	}

	// State should appear in JSON
	json := err.JSON()
	if !strings.Contains(json, `"state":"abc123"`) {
		t.Errorf("JSON() missing state: %s", json)
	}
}
