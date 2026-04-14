package oauth

import (
	"errors"
	"testing"
)

// TestAllServiceAuthSentinelsCovered is a belt-and-suspenders check that
// every ErrServiceAuth* sentinel in this package is listed in
// allServiceAuthSentinels. mapParseError walks that list to bubble keyFunc
// errors through golang-jwt's error-joining, so a forgotten sentinel
// silently downgrades to ErrServiceAuthBadSignature and produces wrong
// metric labels.
func TestAllServiceAuthSentinelsCovered(t *testing.T) {
	// Single source of truth: the errors.go var block. Listed here
	// manually because Go gives no runtime reflection over `var (...)`
	// blocks — a change in errors.go requires a change here too, which
	// is the whole point of this test.
	declared := []error{
		ErrServiceAuthMissingHeader,
		ErrServiceAuthMalformedHeader,
		ErrServiceAuthJWTTooLarge,
		ErrServiceAuthUnsupportedAlg,
		ErrServiceAuthBadAudience,
		ErrServiceAuthBadLxm,
		ErrServiceAuthMissingJTI,
		ErrServiceAuthExpired,
		ErrServiceAuthFutureIAT,
		ErrServiceAuthDIDResolveTimeout,
		ErrServiceAuthDIDResolveNotFound,
		ErrServiceAuthDIDResolveNetwork,
		ErrServiceAuthDIDResolveUnavailable,
		ErrServiceAuthUnsupportedDIDMethod,
		ErrServiceAuthVerificationMethodMissing,
		ErrServiceAuthMalformedMultibase,
		ErrServiceAuthKeyParseFailed,
		ErrServiceAuthBadSignature,
		ErrServiceAuthReplay,
		ErrServiceAuthThrottled,
	}
	covered := make(map[error]bool, len(allServiceAuthSentinels))
	for _, s := range allServiceAuthSentinels {
		covered[s] = true
	}
	for _, d := range declared {
		if !covered[d] {
			t.Errorf("sentinel %v is not in allServiceAuthSentinels", d)
		}
	}
	// Also assert the reason map is exhaustive — ReasonFor should never
	// return "other" for a declared sentinel.
	for _, d := range declared {
		if r := ReasonFor(d); r == "" || r == "other" {
			t.Errorf("sentinel %v maps to %q in ReasonFor", d, r)
		}
	}

	// Sanity: random errors still fall through.
	if r := ReasonFor(errors.New("nope")); r != "other" {
		t.Errorf("unknown err should map to 'other', got %q", r)
	}
}
