package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"fmt"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	multibase "github.com/multiformats/go-multibase"
	varint "github.com/multiformats/go-varint"
)

// AT Protocol multicodec identifiers for the two supported signing-key
// types. The leading byte sequence is the unsigned-varint encoding of
// the codec number — not the number itself (0xe7 encodes as [0xe7,0x01]
// because the high bit marks continuation; 0x1200 encodes as [0x80,0x24]).
const (
	multicodecP256Pub      = 0x1200 // "p256-pub"
	multicodecSecp256k1Pub = 0xe7   // "secp256k1-pub"
	compressedPointLen     = 33     // SEC1 compressed: 1 tag byte + 32 bytes X
)

// parsedSigningKey is the union of signing-key types we accept. Exactly
// one of P256 or Secp256k1 is non-nil.
type parsedSigningKey struct {
	P256      *ecdsa.PublicKey
	Secp256k1 *secp.PublicKey
}

// parsePublicKeyMultibase decodes a `publicKeyMultibase` field from a
// DID document's Multikey verification method. Accepts only base58btc
// (`z` prefix) + p256-pub or secp256k1-pub multicodec + SEC1 compressed
// point (33 bytes). Rejects everything else with a specific error so
// rejection reasons stay distinguishable in metrics.
//
// Spec references:
//   - Multibase: https://www.w3.org/TR/vc-data-integrity/#multibase-0
//   - Multicodec: https://github.com/multiformats/multicodec/blob/master/table.csv
//   - ATProto identity: https://atproto.com/specs/did#signing-keys
func parsePublicKeyMultibase(mb string) (*parsedSigningKey, error) {
	if mb == "" {
		return nil, fmt.Errorf("%w: empty", ErrServiceAuthMalformedMultibase)
	}
	// Strict base58btc only. go-multibase's Decode will accept other bases
	// if we leave it permissive, so we check the prefix byte ourselves.
	if mb[0] != byte(multibase.Base58BTC) {
		return nil, fmt.Errorf("%w: unsupported multibase %q", ErrServiceAuthMalformedMultibase, mb[0])
	}
	_, raw, err := multibase.Decode(mb)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrServiceAuthMalformedMultibase, err)
	}

	codec, n, err := varint.FromUvarint(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: varint decode: %w", ErrServiceAuthMalformedMultibase, err)
	}
	// Bound the varint to something sane so a hostile multi-byte varint
	// can't force us to chew through pathological input.
	if n > 3 {
		return nil, fmt.Errorf("%w: varint too long (%d bytes)", ErrServiceAuthMalformedMultibase, n)
	}
	body := raw[n:]
	if len(body) != compressedPointLen {
		return nil, fmt.Errorf("%w: expected %d-byte compressed point, got %d", ErrServiceAuthMalformedMultibase, compressedPointLen, len(body))
	}
	// SEC1 compressed form has tag byte 0x02 or 0x03.
	if body[0] != 0x02 && body[0] != 0x03 {
		return nil, fmt.Errorf("%w: compressed point tag must be 0x02/0x03, got 0x%02x", ErrServiceAuthMalformedMultibase, body[0])
	}

	switch codec {
	case multicodecP256Pub:
		x, y := elliptic.UnmarshalCompressed(elliptic.P256(), body)
		if x == nil || y == nil {
			return nil, fmt.Errorf("%w: P-256 point decode failed", ErrServiceAuthKeyParseFailed)
		}
		// Guard against the point at infinity (x,y == 0,0).
		if x.Sign() == 0 && y.Sign() == 0 {
			return nil, fmt.Errorf("%w: P-256 point at infinity", ErrServiceAuthKeyParseFailed)
		}
		return &parsedSigningKey{P256: &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}}, nil
	case multicodecSecp256k1Pub:
		pub, err := secp.ParsePubKey(body)
		if err != nil {
			return nil, fmt.Errorf("%w: secp256k1 parse: %w", ErrServiceAuthKeyParseFailed, err)
		}
		if !pub.IsOnCurve() {
			return nil, fmt.Errorf("%w: secp256k1 point not on curve", ErrServiceAuthKeyParseFailed)
		}
		return &parsedSigningKey{Secp256k1: pub}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported multicodec 0x%x", ErrServiceAuthMalformedMultibase, codec)
	}
}
