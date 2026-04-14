package oauth

import "errors"

// Sentinel errors returned by ServiceAuthVerifier.Verify. Middleware maps
// each one to a bounded metric `reason` label via errors.Is, so every new
// error here must also appear in the verifier's rejection-label constants.
//
// The granularity is deliberate: operators tracking a 401 spike need to
// tell "PLC directory is down" from "caller minted a bad aud" from "a
// legitimate client's clock drifted." Collapsing these into one generic
// error would force log-grepping to diagnose prod incidents. See §Metrics
// in the plan for the expected per-reason dashboards.
var (
	ErrServiceAuthMissingHeader             = errors.New("service-auth: missing Authorization header")
	ErrServiceAuthMalformedHeader           = errors.New("service-auth: malformed Authorization header")
	ErrServiceAuthJWTTooLarge               = errors.New("service-auth: token exceeds max size")
	ErrServiceAuthUnsupportedAlg            = errors.New("service-auth: unsupported alg")
	ErrServiceAuthBadAudience               = errors.New("service-auth: audience mismatch")
	ErrServiceAuthBadLxm                    = errors.New("service-auth: lxm mismatch")
	ErrServiceAuthMissingJTI                = errors.New("service-auth: jti and iat both missing")
	ErrServiceAuthExpired                   = errors.New("service-auth: token expired or exp out of window")
	ErrServiceAuthFutureIAT                 = errors.New("service-auth: iat in future beyond skew")
	ErrServiceAuthDIDResolveTimeout         = errors.New("service-auth: DID resolve timed out")
	ErrServiceAuthDIDResolveNotFound        = errors.New("service-auth: DID not found")
	ErrServiceAuthDIDResolveNetwork         = errors.New("service-auth: DID resolve network error")
	ErrServiceAuthDIDResolveUnavailable     = errors.New("service-auth: DID resolve unavailable, no usable cached entry")
	ErrServiceAuthUnsupportedDIDMethod      = errors.New("service-auth: unsupported DID method")
	ErrServiceAuthVerificationMethodMissing = errors.New("service-auth: #atproto verification method missing")
	ErrServiceAuthMalformedMultibase        = errors.New("service-auth: publicKeyMultibase malformed")
	ErrServiceAuthKeyParseFailed            = errors.New("service-auth: signing key parse failed")
	ErrServiceAuthBadSignature              = errors.New("service-auth: signature invalid")
	ErrServiceAuthReplay                    = errors.New("service-auth: token replay detected")
	ErrServiceAuthThrottled                 = errors.New("service-auth: resolver throttled for this caller")
)
