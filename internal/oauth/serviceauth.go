// Package oauth — service-auth JWT verification for AT Protocol.
//
// This file implements ATProto service-auth per https://atproto.com/specs/xrpc.
// Callers (typically the notifications middleware) invoke Verify with a
// bearer token + the expected `lxm`; on success we return the issuer
// DID which the handler treats as the authenticated user.
//
// Key design choices:
//   - `alg` allowlist via jwt.WithValidMethods, rejecting alg=none before
//     key resolution.
//   - Multibase / multicodec / compressed-point parsing in a dedicated
//     helper, with a real varint decoder.
//   - Low-s NOT enforced for ES256K: indigo and some TS signers emit
//     high-s; ecosystem convention is lenient verify.
//   - `jti` required only when `iat` is absent — ECDSA is non-deterministic
//     so a synthetic key built from sig bytes + (iss|aud|exp|iat) needs at
//     least one of those claims to be unique across re-signings.
//
// Deferred (sentinels exist, behaviour does not yet — issue #57 follow-up):
// per-iss resolver throttle, negative cache on resolve failure,
// serve-stale on PLC outage, bad-signature key-rotation retry.
package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Tuning constants. Left as consts (not env) to avoid speculative knobs —
// promote to config only when a real need arises.
const (
	serviceAuthMaxLifetime = 60 * time.Second // max (exp - now) we accept
	serviceAuthMaxSkew     = 30 * time.Second // max (iat - now) we tolerate
	serviceAuthMaxJWTBytes = 8 * 1024         // pre-parse size cap
	jtiCacheCapacity       = 100_000
)

// ServiceAuthConfig configures a ServiceAuthVerifier.
type ServiceAuthConfig struct {
	// Audience is our own DID — every incoming token's `aud` must match.
	Audience string
	// Resolver supplies DID documents. The interface allows test doubles
	// without touching the network.
	Resolver ServiceAuthDIDResolver
}

// ServiceAuthDIDResolver is the minimal DID-resolution contract the
// verifier needs. The production implementation (*DIDResolver) satisfies
// this; tests can substitute an in-memory map.
type ServiceAuthDIDResolver interface {
	ResolveDID(did string) (*DIDDocument, error)
}

// ServiceAuthVerifier verifies AT Protocol service-auth JWTs. Safe for
// concurrent use.
type ServiceAuthVerifier struct {
	cfg      ServiceAuthConfig
	jtiCache *jtiReplayCache
}

// NewServiceAuthVerifier constructs a verifier. `cfg.Audience` must be a
// syntactically valid DID — NewServiceAuthVerifier does not validate
// that here; config.Validate() in the caller must check ahead of time.
func NewServiceAuthVerifier(cfg ServiceAuthConfig) *ServiceAuthVerifier {
	return &ServiceAuthVerifier{
		cfg:      cfg,
		jtiCache: newJTIReplayCache(jtiCacheCapacity),
	}
}

// serviceAuthClaims is the subset of service-auth JWT claims we inspect.
// Unknown fields are ignored (json.Decoder default).
type serviceAuthClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat,omitempty"`
	Jti string `json:"jti,omitempty"`
	Lxm string `json:"lxm,omitempty"`
}

// GetExpirationTime satisfies jwt.Claims so we can use ParseWithClaims.
func (c *serviceAuthClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	if c.Exp == 0 {
		return nil, nil
	}
	return jwt.NewNumericDate(time.Unix(c.Exp, 0)), nil
}
func (c *serviceAuthClaims) GetIssuedAt() (*jwt.NumericDate, error) {
	if c.Iat == 0 {
		return nil, nil
	}
	return jwt.NewNumericDate(time.Unix(c.Iat, 0)), nil
}
func (c *serviceAuthClaims) GetNotBefore() (*jwt.NumericDate, error) { return nil, nil }
func (c *serviceAuthClaims) GetIssuer() (string, error)              { return c.Iss, nil }
func (c *serviceAuthClaims) GetSubject() (string, error)             { return "", nil }
func (c *serviceAuthClaims) GetAudience() (jwt.ClaimStrings, error) {
	return jwt.ClaimStrings{c.Aud}, nil
}

// Verify parses, validates, and signature-checks a bearer token. On
// success returns the issuer DID; on failure returns one of the
// ErrServiceAuth* sentinels wrapped with context. `expectedLxm` must be
// a non-empty string — every token we accept must declare the same lxm
// (per-endpoint lxm pinning, R2.2).
func (v *ServiceAuthVerifier) Verify(ctx context.Context, tokenStr, expectedLxm string) (string, error) {
	if expectedLxm == "" {
		return "", fmt.Errorf("service-auth: expectedLxm must be set by caller")
	}
	if tokenStr == "" {
		return "", ErrServiceAuthMalformedHeader
	}
	if len(tokenStr) > serviceAuthMaxJWTBytes {
		return "", ErrServiceAuthJWTTooLarge
	}

	// Pre-parse `typ` header (optional per spec). We do this before
	// jwt.ParseWithClaims so a bad `typ` can't slip past.
	if err := checkTypHeader(tokenStr); err != nil {
		return "", err
	}

	claims := &serviceAuthClaims{}
	token, err := jwt.ParseWithClaims(
		tokenStr, claims,
		v.keyFunc,
		jwt.WithValidMethods([]string{"ES256", es256kAlg}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		// Map jwt parse errors back to our sentinels; fall through to
		// our own claim checks for structural issues.
		return "", mapParseError(err)
	}
	if !token.Valid {
		return "", ErrServiceAuthBadSignature
	}

	now := time.Now()

	// Audience. Spec allows string-or-array; we only accept single string.
	if claims.Aud != v.cfg.Audience {
		return "", ErrServiceAuthBadAudience
	}

	// exp within MaxLifetime.
	if claims.Exp == 0 {
		return "", ErrServiceAuthExpired
	}
	exp := time.Unix(claims.Exp, 0)
	if !exp.After(now.Add(-serviceAuthMaxSkew)) {
		return "", ErrServiceAuthExpired
	}
	if exp.After(now.Add(serviceAuthMaxLifetime + serviceAuthMaxSkew)) {
		return "", ErrServiceAuthExpired
	}

	// iat future-skew (past is unbounded; exp already caps overall lifetime).
	if claims.Iat != 0 {
		iat := time.Unix(claims.Iat, 0)
		if iat.After(now.Add(serviceAuthMaxSkew)) {
			return "", ErrServiceAuthFutureIAT
		}
	}

	// lxm pinned to caller's expectation.
	if claims.Lxm != expectedLxm {
		return "", ErrServiceAuthBadLxm
	}

	// At least one of jti/iat must be present (see R3.2).
	if claims.Jti == "" && claims.Iat == 0 {
		return "", ErrServiceAuthMissingJTI
	}

	// Replay defence. Use jti if supplied; otherwise build a synthetic
	// key from the claims + signature. ECDSA is non-deterministic so
	// re-signing gives a different sig, which means "same claims, fresh
	// sig" is still accepted — matches the spirit of jti (nonce).
	replayKey := claims.Jti
	if replayKey == "" {
		replayKey = synthReplayKey(claims, tokenStr)
	}
	replayKey = claims.Iss + "|" + replayKey
	if !v.jtiCache.checkAndSet(replayKey, exp.Add(serviceAuthMaxSkew), now) {
		return "", ErrServiceAuthReplay
	}

	return claims.Iss, nil
}

// keyFunc is the golang-jwt callback that resolves the signing key. It
// runs after header/alg validation (WithValidMethods) but before
// signature verification. Returns the parsed public key type matching
// the declared alg.
func (v *ServiceAuthVerifier) keyFunc(token *jwt.Token) (any, error) {
	claims, ok := token.Claims.(*serviceAuthClaims)
	if !ok {
		return nil, ErrServiceAuthMalformedHeader
	}
	if claims.Iss == "" {
		return nil, fmt.Errorf("%w: missing iss", ErrServiceAuthMalformedHeader)
	}
	if !HasDIDMethodPrefix(claims.Iss) {
		return nil, fmt.Errorf("%w: malformed iss %q", ErrServiceAuthUnsupportedDIDMethod, claims.Iss)
	}

	doc, err := v.cfg.Resolver.ResolveDID(claims.Iss)
	if err != nil {
		return nil, classifyResolveError(err)
	}
	vm := doc.AtprotoSigningKey()
	if vm == nil {
		return nil, ErrServiceAuthVerificationMethodMissing
	}
	parsed, err := parsePublicKeyMultibase(vm.PublicKeyMultibase)
	if err != nil {
		return nil, err
	}

	switch token.Method.Alg() {
	case "ES256":
		if parsed.P256 == nil {
			return nil, fmt.Errorf("%w: alg=ES256 with non-P256 key", ErrServiceAuthBadSignature)
		}
		return parsed.P256, nil
	case es256kAlg:
		if parsed.Secp256k1 == nil {
			return nil, fmt.Errorf("%w: alg=ES256K with non-secp256k1 key", ErrServiceAuthBadSignature)
		}
		return parsed.Secp256k1, nil
	default:
		return nil, ErrServiceAuthUnsupportedAlg
	}
}

// checkTypHeader enforces the optional `typ` header: if present it must
// be "JWT" or "at+jwt"; absent is allowed.
func checkTypHeader(tokenStr string) error {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return ErrServiceAuthMalformedHeader
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: header decode: %w", ErrServiceAuthMalformedHeader, err)
	}
	var hdr struct {
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return fmt.Errorf("%w: header JSON: %w", ErrServiceAuthMalformedHeader, err)
	}
	if hdr.Typ != "" && hdr.Typ != "JWT" && hdr.Typ != "at+jwt" {
		return fmt.Errorf("%w: unsupported typ %q", ErrServiceAuthMalformedHeader, hdr.Typ)
	}
	return nil
}

// mapParseError converts the golang-jwt error soup into our sentinel set.
// Our sentinels (returned from keyFunc) are checked FIRST — golang-jwt
// joins them with jwt.ErrTokenUnverifiable and/or jwt.ErrSignatureInvalid,
// and if we checked the jwt sentinels first we'd lose the specific
// reason (did_resolve_not_found, verification_method_not_found, etc.).
func mapParseError(err error) error {
	// Preserve our sentinel set — they carry the metric reason label.
	for _, sentinel := range allServiceAuthSentinels {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrServiceAuthExpired
	case errors.Is(err, jwt.ErrTokenUsedBeforeIssued):
		return ErrServiceAuthFutureIAT
	case errors.Is(err, jwt.ErrTokenSignatureInvalid),
		errors.Is(err, jwt.ErrSignatureInvalid):
		return ErrServiceAuthBadSignature
	case errors.Is(err, jwt.ErrTokenMalformed):
		return ErrServiceAuthMalformedHeader
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return ErrServiceAuthBadSignature
	}
	return fmt.Errorf("%w: %w", ErrServiceAuthBadSignature, err)
}

// allServiceAuthSentinels is scanned in mapParseError to pull nested
// keyFunc errors back out. Kept in one place so new sentinels stay in
// sync — if you add one in serviceauth_errors.go, add it here too.
var allServiceAuthSentinels = []error{
	ErrServiceAuthMissingHeader,
	ErrServiceAuthBadAudience,
	ErrServiceAuthBadLxm,
	ErrServiceAuthExpired,
	ErrServiceAuthFutureIAT,
	ErrServiceAuthMissingJTI,
	ErrServiceAuthJWTTooLarge,
	ErrServiceAuthUnsupportedAlg,
	ErrServiceAuthBadSignature,
	ErrServiceAuthMalformedHeader,
	ErrServiceAuthMalformedMultibase,
	ErrServiceAuthKeyParseFailed,
	ErrServiceAuthDIDResolveTimeout,
	ErrServiceAuthDIDResolveNotFound,
	ErrServiceAuthDIDResolveNetwork,
	ErrServiceAuthDIDResolveUnavailable,
	ErrServiceAuthUnsupportedDIDMethod,
	ErrServiceAuthVerificationMethodMissing,
	ErrServiceAuthReplay,
	ErrServiceAuthThrottled,
}

func classifyResolveError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return fmt.Errorf("%w: %w", ErrServiceAuthDIDResolveTimeout, err)
	case strings.Contains(msg, "not found") || strings.Contains(msg, "404"):
		return fmt.Errorf("%w: %w", ErrServiceAuthDIDResolveNotFound, err)
	}
	return fmt.Errorf("%w: %w", ErrServiceAuthDIDResolveNetwork, err)
}

// synthReplayKey builds a deterministic-per-signature key for tokens
// that omit jti. Derived from the literal signature bytes of the token,
// which includes every ECDSA nonce — different re-signings yield
// different keys, matching the "fresh nonce per token" guarantee the
// caller would have gotten from a random jti.
func synthReplayKey(claims *serviceAuthClaims, tokenStr string) string {
	parts := strings.SplitN(tokenStr, ".", 3)
	sig := ""
	if len(parts) == 3 {
		sig = parts[2]
	}
	return fmt.Sprintf("synth:%s:%s:%d:%d:%s", claims.Iss, claims.Aud, claims.Exp, claims.Iat, sig)
}

// ReasonFor returns the metric reason label for an error returned by
// Verify. Unknown errors map to "other" so dashboards never see an
// unbounded label. Kept in this file so adding a sentinel stays local.
func ReasonFor(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrServiceAuthMissingHeader):
		return "missing_header"
	case errors.Is(err, ErrServiceAuthMalformedHeader):
		return "malformed_header"
	case errors.Is(err, ErrServiceAuthJWTTooLarge):
		return "too_large"
	case errors.Is(err, ErrServiceAuthUnsupportedAlg):
		return "unsupported_alg"
	case errors.Is(err, ErrServiceAuthBadAudience):
		return "bad_audience"
	case errors.Is(err, ErrServiceAuthBadLxm):
		return "bad_lxm"
	case errors.Is(err, ErrServiceAuthMissingJTI):
		return "missing_jti"
	case errors.Is(err, ErrServiceAuthExpired):
		return "expired"
	case errors.Is(err, ErrServiceAuthFutureIAT):
		return "future_iat"
	case errors.Is(err, ErrServiceAuthDIDResolveTimeout):
		return "did_resolve_timeout"
	case errors.Is(err, ErrServiceAuthDIDResolveNotFound):
		return "did_resolve_not_found"
	case errors.Is(err, ErrServiceAuthDIDResolveNetwork):
		return "did_resolve_network"
	case errors.Is(err, ErrServiceAuthDIDResolveUnavailable):
		return "did_resolve_unavailable"
	case errors.Is(err, ErrServiceAuthUnsupportedDIDMethod):
		return "unsupported_did_method"
	case errors.Is(err, ErrServiceAuthVerificationMethodMissing):
		return "verification_method_not_found"
	case errors.Is(err, ErrServiceAuthMalformedMultibase):
		return "malformed_multibase"
	case errors.Is(err, ErrServiceAuthKeyParseFailed):
		return "key_parse_failed"
	case errors.Is(err, ErrServiceAuthBadSignature):
		return "bad_signature"
	case errors.Is(err, ErrServiceAuthReplay):
		return "replay"
	case errors.Is(err, ErrServiceAuthThrottled):
		return "throttled"
	}
	return "other"
}
