package server

import (
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/oauth"
)

// TestCheckRefreshTokenDPoPBinding covers the matrix in issue #24:
// bound token + matching/missing/mismatched incoming JKT; unbound legacy
// token inside and outside the sunset window. This isolates the policy
// check from the surrounding refresh handler, which would need a full
// DB setup.
func TestCheckRefreshTokenDPoPBinding(t *testing.T) {
	const cutoff int64 = 1_700_000_000

	h := &OAuthHandlers{
		config: OAuthHandlerConfig{LegacyDPoPJKTCutoff: cutoff},
	}

	mkStr := func(s string) *string { return &s }

	cases := []struct {
		name        string
		storedJKT   string
		issuedAt    int64
		incomingJKT *string
		wantErr     bool
		errContains string
	}{
		{
			name:        "bound token, matching JKT",
			storedJKT:   "jkt-abc",
			issuedAt:    cutoff + 100,
			incomingJKT: mkStr("jkt-abc"),
			wantErr:     false,
		},
		{
			name:        "bound token, missing proof",
			storedJKT:   "jkt-abc",
			issuedAt:    cutoff + 100,
			incomingJKT: nil,
			wantErr:     true,
			errContains: "required",
		},
		{
			name:        "bound token, mismatched JKT",
			storedJKT:   "jkt-abc",
			issuedAt:    cutoff + 100,
			incomingJKT: mkStr("jkt-xyz"),
			wantErr:     true,
			errContains: "mismatch",
		},
		{
			name:        "legacy unbound token, before cutoff",
			storedJKT:   "",
			issuedAt:    cutoff - 1,
			incomingJKT: nil,
			wantErr:     false,
		},
		{
			name:        "legacy unbound token, after cutoff",
			storedJKT:   "",
			issuedAt:    cutoff + 1,
			incomingJKT: nil,
			wantErr:     true,
			errContains: "sunset",
		},
		{
			name:        "legacy unbound token, at cutoff boundary",
			storedJKT:   "",
			issuedAt:    cutoff,
			incomingJKT: nil,
			wantErr:     true,
			errContains: "sunset",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := &oauth.RefreshToken{
				DPoPJKT:          tc.storedJKT,
				OriginalIssuedAt: tc.issuedAt,
			}
			err := h.checkRefreshTokenDPoPBinding(tok, tc.incomingJKT)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil && tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("err=%q, want substring %q", err.Error(), tc.errContains)
			}
		})
	}
}
