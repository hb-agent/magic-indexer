package oauth

import (
	"crypto/sha256"
	"fmt"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa_k1 "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/golang-jwt/jwt/v5"
)

// es256kAlg is the JWS `alg` value for ECDSA over secp256k1 / SHA-256.
// ATProto's default signing curve is secp256k1 for bsky.social PDSes, so
// this is the hot path for real-world service-auth tokens.
const es256kAlg = "ES256K"

// signingMethodES256K implements jwt.SigningMethod for secp256k1 ECDSA.
// golang-jwt/v5's built-in ES256 handles P-256; there's no built-in for
// secp256k1. The signing half is unused here (we never mint) — we
// implement only Verify.
//
// The signature format is IEEE-P1363 `r || s` (32 bytes big-endian each,
// total 64 bytes). DER-encoded signatures are rejected — that's the
// JWS convention and the ATProto spec follows it.
//
// Low-s malleability: deliberately NOT enforced. The ATProto ecosystem
// (indigo, @atproto/xrpc-server) uses lenient verification and some
// signers emit high-s. Enforcing low-s would reject legitimate tokens.
// Reviewed in R2.5.
type signingMethodES256K struct{}

var es256kInstance = &signingMethodES256K{}

func (m *signingMethodES256K) Alg() string { return es256kAlg }

// Sign is unused — the indexer only verifies. Implemented only so this
// type satisfies the full jwt.SigningMethod interface.
func (m *signingMethodES256K) Sign(_ string, _ any) ([]byte, error) {
	return nil, fmt.Errorf("ES256K signing not implemented")
}

// Verify checks an IEEE-P1363 secp256k1 signature over signingString.
// Wraps the library call in defer-recover: a panic in the crypto path
// would otherwise surface as a 500 to the caller and potentially crash
// other in-flight goroutines. Panic → `ErrServiceAuthBadSignature`.
func (m *signingMethodES256K) Verify(signingString string, sig []byte, key any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: panic in ES256K verify: %v", ErrServiceAuthBadSignature, r)
		}
	}()

	pub, ok := key.(*secp.PublicKey)
	if !ok {
		return fmt.Errorf("%w: ES256K expects *secp256k1.PublicKey, got %T", ErrServiceAuthKeyParseFailed, key)
	}

	// IEEE-P1363: exactly 64 bytes, split evenly. DER is longer and has
	// a 0x30 leading byte — either way sig[0] == 0x30 with len == 64
	// is astronomically unlikely, so a strict length check is enough
	// to reject DER without an explicit sniff.
	if len(sig) != 64 {
		return fmt.Errorf("%w: IEEE-P1363 signature must be 64 bytes, got %d", ErrServiceAuthBadSignature, len(sig))
	}
	r := new(secp.ModNScalar)
	s := new(secp.ModNScalar)
	if overflow := r.SetByteSlice(sig[:32]); overflow {
		return fmt.Errorf("%w: r >= curve order", ErrServiceAuthBadSignature)
	}
	if overflow := s.SetByteSlice(sig[32:]); overflow {
		return fmt.Errorf("%w: s >= curve order", ErrServiceAuthBadSignature)
	}
	if r.IsZero() || s.IsZero() {
		return fmt.Errorf("%w: r or s is zero", ErrServiceAuthBadSignature)
	}

	// Hash the signing input per JWS (RFC 7515 §5.2): raw bytes of the
	// ASCII "<base64url(header)>.<base64url(payload)>" string.
	digest := sha256.Sum256([]byte(signingString))

	sigObj := ecdsa_k1.NewSignature(r, s)
	if !sigObj.Verify(digest[:], pub) {
		return ErrServiceAuthBadSignature
	}
	return nil
}

// registerES256K installs the ES256K signing method into golang-jwt's
// global registry. Idempotent — safe to call from init() in multiple
// packages.
func registerES256K() {
	jwt.RegisterSigningMethod(es256kAlg, func() jwt.SigningMethod { return es256kInstance })
}

func init() { registerES256K() }
