package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	multibase "github.com/multiformats/go-multibase"
	varint "github.com/multiformats/go-varint"
)

func encodeMultibaseKey(t *testing.T, codec uint64, keyBytes []byte) string {
	t.Helper()
	body := append(varint.ToUvarint(codec), keyBytes...)
	s, err := multibase.Encode(multibase.Base58BTC, body)
	if err != nil {
		t.Fatalf("multibase.Encode: %v", err)
	}
	return s
}

func newP256MultibaseKey(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256: %v", err)
	}
	compressed := elliptic.MarshalCompressed(elliptic.P256(), priv.X, priv.Y)
	return encodeMultibaseKey(t, multicodecP256Pub, compressed)
}

func newSecp256k1MultibaseKey(t *testing.T) string {
	t.Helper()
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate secp256k1: %v", err)
	}
	compressed := priv.PubKey().SerializeCompressed()
	return encodeMultibaseKey(t, multicodecSecp256k1Pub, compressed)
}

func TestParsePublicKeyMultibase_P256_Valid(t *testing.T) {
	mb := newP256MultibaseKey(t)
	parsed, err := parsePublicKeyMultibase(mb)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.P256 == nil {
		t.Fatalf("want P256 non-nil, got %+v", parsed)
	}
	if parsed.Secp256k1 != nil {
		t.Errorf("Secp256k1 should be nil for P-256 key")
	}
}

func TestParsePublicKeyMultibase_Secp256k1_Valid(t *testing.T) {
	mb := newSecp256k1MultibaseKey(t)
	parsed, err := parsePublicKeyMultibase(mb)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Secp256k1 == nil {
		t.Fatalf("want Secp256k1 non-nil, got %+v", parsed)
	}
	if parsed.P256 != nil {
		t.Errorf("P256 should be nil for secp256k1 key")
	}
}

func TestParsePublicKeyMultibase_RejectEmpty(t *testing.T) {
	_, err := parsePublicKeyMultibase("")
	if !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want ErrServiceAuthMalformedMultibase, got %v", err)
	}
}

func TestParsePublicKeyMultibase_RejectUnsupportedBase(t *testing.T) {
	// 'm' = base64; legit multibase but not base58btc.
	s, _ := multibase.Encode(multibase.Base64, []byte{0xe7, 0x01, 0x02, 0x00})
	_, err := parsePublicKeyMultibase(s)
	if !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want ErrServiceAuthMalformedMultibase, got %v", err)
	}
}

func TestParsePublicKeyMultibase_RejectUnknownCodec(t *testing.T) {
	// 0xed = ed25519-pub — valid multicodec but not allowed here.
	junk := encodeMultibaseKey(t, 0xed, make([]byte, 32))
	_, err := parsePublicKeyMultibase(junk)
	if !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want ErrServiceAuthMalformedMultibase, got %v", err)
	}
}

func TestParsePublicKeyMultibase_RejectWrongLength(t *testing.T) {
	// Valid codec, but body is 32 bytes (missing SEC1 tag).
	s := encodeMultibaseKey(t, multicodecP256Pub, make([]byte, 32))
	_, err := parsePublicKeyMultibase(s)
	if !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want ErrServiceAuthMalformedMultibase, got %v", err)
	}
	if !strings.Contains(err.Error(), "33-byte") {
		t.Errorf("error should mention expected length, got %q", err.Error())
	}
}

func TestParsePublicKeyMultibase_RejectBadCompressedTag(t *testing.T) {
	// 0x04 = uncompressed tag at the right length (33). ATProto spec
	// requires compressed form.
	body := append([]byte{0x04}, make([]byte, 32)...)
	s := encodeMultibaseKey(t, multicodecP256Pub, body)
	_, err := parsePublicKeyMultibase(s)
	if !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want ErrServiceAuthMalformedMultibase, got %v", err)
	}
}

func TestParsePublicKeyMultibase_RejectOffCurveP256(t *testing.T) {
	// 0x02 tag + arbitrary 32 bytes — extremely unlikely to be on curve,
	// UnmarshalCompressed should return nil.
	body := append([]byte{0x02}, make([]byte, 32)...)
	body[32] = 0x01
	s := encodeMultibaseKey(t, multicodecP256Pub, body)
	_, err := parsePublicKeyMultibase(s)
	if !errors.Is(err, ErrServiceAuthKeyParseFailed) && !errors.Is(err, ErrServiceAuthMalformedMultibase) {
		t.Fatalf("want key parse error, got %v", err)
	}
}
