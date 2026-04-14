package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa_k1 "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	multibase "github.com/multiformats/go-multibase"
	varint "github.com/multiformats/go-varint"
)

// --- Test fixtures -----------------------------------------------------------

type fakeResolver struct {
	mu   sync.Mutex
	docs map[string]*DIDDocument
	err  error
}

func (f *fakeResolver) ResolveDID(did string) (*DIDDocument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	doc, ok := f.docs[did]
	if !ok {
		return nil, fmt.Errorf("DID not found: %s", did)
	}
	return doc, nil
}

type testSigner struct {
	did  string
	alg  string
	p256 *ecdsa.PrivateKey
	k1   *secp.PrivateKey
}

func newP256Signer(t *testing.T, did string) *testSigner {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{did: did, alg: "ES256", p256: priv}
}

func newSecp256k1Signer(t *testing.T, did string) *testSigner {
	t.Helper()
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{did: did, alg: es256kAlg, k1: priv}
}

func (s *testSigner) multibasePub(t *testing.T) string {
	t.Helper()
	var codec uint64
	var body []byte
	if s.p256 != nil {
		codec = multicodecP256Pub
		body = elliptic.MarshalCompressed(elliptic.P256(), s.p256.X, s.p256.Y)
	} else {
		codec = multicodecSecp256k1Pub
		body = s.k1.PubKey().SerializeCompressed()
	}
	raw := append(varint.ToUvarint(codec), body...)
	out, err := multibase.Encode(multibase.Base58BTC, raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (s *testSigner) didDoc(t *testing.T) *DIDDocument {
	return &DIDDocument{
		ID: s.did,
		VerificationMethod: []VerificationMethod{{
			ID:                 s.did + "#atproto",
			Type:               "Multikey",
			Controller:         s.did,
			PublicKeyMultibase: s.multibasePub(t),
		}},
	}
}

type jwtClaimsMap map[string]any

func (s *testSigner) mint(t *testing.T, claims jwtClaimsMap) string {
	t.Helper()
	hdr := map[string]string{"typ": "JWT", "alg": s.alg}
	hdrJSON, _ := json.Marshal(hdr)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hdrJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))

	var sig []byte
	if s.p256 != nil {
		r, sVal, err := ecdsa.Sign(rand.Reader, s.p256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		rBytes := make([]byte, 32)
		sBytes := make([]byte, 32)
		r.FillBytes(rBytes)
		sVal.FillBytes(sBytes)
		sig = append(rBytes, sBytes...)
	} else {
		// SignCompact returns [recid, r(32), s(32)]; drop recid for P1363.
		compact := ecdsa_k1.SignCompact(s.k1, digest[:], true)
		sig = compact[1:65]
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

const (
	testAud = "did:web:indexer.example"
	testLxm = "com.hypergoat.notification.query"
)

func newVerifier(resolver *fakeResolver) *ServiceAuthVerifier {
	return NewServiceAuthVerifier(ServiceAuthConfig{
		Audience: testAud,
		Resolver: resolver,
	})
}

func baseClaims(iss string, now time.Time) jwtClaimsMap {
	return jwtClaimsMap{
		"iss": iss,
		"aud": testAud,
		"exp": now.Add(30 * time.Second).Unix(),
		"iat": now.Unix(),
		"jti": fmt.Sprintf("%d-nonce", now.UnixNano()),
		"lxm": testLxm,
	}
}

// --- Tests -------------------------------------------------------------------

func TestServiceAuthVerify_P256_Valid(t *testing.T) {
	iss := "did:web:alice.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	tok := signer.mint(t, baseClaims(iss, time.Now()))
	did, err := v.Verify(context.Background(), tok, testLxm)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if did != iss {
		t.Errorf("want did=%s, got %s", iss, did)
	}
}

func TestServiceAuthVerify_Secp256k1_Valid(t *testing.T) {
	iss := "did:web:bob.example"
	signer := newSecp256k1Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	tok := signer.mint(t, baseClaims(iss, time.Now()))
	did, err := v.Verify(context.Background(), tok, testLxm)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if did != iss {
		t.Errorf("want did=%s, got %s", iss, did)
	}
}

func TestServiceAuthVerify_RejectTamperedPayload(t *testing.T) {
	iss := "did:web:carol.example"
	signer := newSecp256k1Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	tok := signer.mint(t, baseClaims(iss, time.Now()))
	// Flip a bit in the payload by swapping one base64 char.
	parts := splitDots(tok)
	if len(parts[1]) > 5 {
		if parts[1][5] == 'A' {
			parts[1] = parts[1][:5] + "B" + parts[1][6:]
		} else {
			parts[1] = parts[1][:5] + "A" + parts[1][6:]
		}
	}
	tampered := parts[0] + "." + parts[1] + "." + parts[2]

	_, err := v.Verify(context.Background(), tampered, testLxm)
	if err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestServiceAuthVerify_RejectBadAudience(t *testing.T) {
	iss := "did:web:dave.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	claims["aud"] = "did:web:someone-else.example"
	tok := signer.mint(t, claims)
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthBadAudience) {
		t.Fatalf("want ErrServiceAuthBadAudience, got %v", err)
	}
}

func TestServiceAuthVerify_RejectBadLxm(t *testing.T) {
	iss := "did:web:eve.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	claims["lxm"] = "com.atproto.other"
	tok := signer.mint(t, claims)
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthBadLxm) {
		t.Fatalf("want ErrServiceAuthBadLxm, got %v", err)
	}
}

func TestServiceAuthVerify_RejectExpired(t *testing.T) {
	iss := "did:web:frank.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	claims["exp"] = time.Now().Add(-120 * time.Second).Unix()
	claims["iat"] = time.Now().Add(-180 * time.Second).Unix()
	tok := signer.mint(t, claims)
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthExpired) {
		t.Fatalf("want ErrServiceAuthExpired, got %v", err)
	}
}

func TestServiceAuthVerify_RejectFutureIat(t *testing.T) {
	iss := "did:web:gina.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	claims["iat"] = time.Now().Add(120 * time.Second).Unix()
	tok := signer.mint(t, claims)
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthFutureIAT) {
		t.Fatalf("want ErrServiceAuthFutureIAT, got %v", err)
	}
}

func TestServiceAuthVerify_RejectMissingJtiAndIat(t *testing.T) {
	iss := "did:web:hank.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	delete(claims, "jti")
	delete(claims, "iat")
	tok := signer.mint(t, claims)
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthMissingJTI) {
		t.Fatalf("want ErrServiceAuthMissingJTI, got %v", err)
	}
}

func TestServiceAuthVerify_AcceptJtiOnly(t *testing.T) {
	iss := "did:web:iris.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	delete(claims, "iat")
	tok := signer.mint(t, claims)
	if _, err := v.Verify(context.Background(), tok, testLxm); err != nil {
		t.Fatalf("want accept, got %v", err)
	}
}

func TestServiceAuthVerify_AcceptIatOnly(t *testing.T) {
	iss := "did:web:jane.example"
	signer := newSecp256k1Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	delete(claims, "jti")
	tok := signer.mint(t, claims)
	if _, err := v.Verify(context.Background(), tok, testLxm); err != nil {
		t.Fatalf("want accept, got %v", err)
	}
}

func TestServiceAuthVerify_Replay(t *testing.T) {
	iss := "did:web:kate.example"
	signer := newP256Signer(t, iss)
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	tok := signer.mint(t, baseClaims(iss, time.Now()))
	if _, err := v.Verify(context.Background(), tok, testLxm); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok, testLxm); !errors.Is(err, ErrServiceAuthReplay) {
		t.Fatalf("replay should be rejected, got %v", err)
	}
}

func TestServiceAuthVerify_ForgedIssAcceptedKeyRejected(t *testing.T) {
	// Attacker signs with their own key but claims victim's iss.
	attacker := newP256Signer(t, "did:web:mallory.example")
	victimDID := "did:web:victim.example"
	victim := newP256Signer(t, victimDID)

	// Resolver returns victim's key for victim's iss.
	resolver := &fakeResolver{docs: map[string]*DIDDocument{victimDID: victim.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(victimDID, time.Now())
	tok := attacker.mint(t, claims)
	tok = rewriteISS(t, tok, attacker, victimDID)

	_, err := v.Verify(context.Background(), tok, testLxm)
	if err == nil {
		t.Fatal("forged iss was accepted")
	}
}

func TestServiceAuthVerify_AlgSwap(t *testing.T) {
	iss := "did:web:leo.example"
	signer := newSecp256k1Signer(t, iss) // real key is secp256k1
	resolver := &fakeResolver{docs: map[string]*DIDDocument{iss: signer.didDoc(t)}}
	v := newVerifier(resolver)

	claims := baseClaims(iss, time.Now())
	// Sign with secp256k1 but claim ES256 in the header — keyFunc returns
	// the P-256 key (absent), so verify fails.
	hdr := map[string]string{"typ": "JWT", "alg": "ES256"}
	hdrJSON, _ := json.Marshal(hdr)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hdrJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	compact := ecdsa_k1.SignCompact(signer.k1, digest[:], true)
	tok := signingInput + "." + base64.RawURLEncoding.EncodeToString(compact[1:65])

	_, err := v.Verify(context.Background(), tok, testLxm)
	if err == nil {
		t.Fatal("alg-swap was accepted")
	}
}

func TestServiceAuthVerify_RejectAlgNone(t *testing.T) {
	iss := "did:web:none.example"
	claims := baseClaims(iss, time.Now())
	hdr := map[string]string{"typ": "JWT", "alg": "none"}
	hdrJSON, _ := json.Marshal(hdr)
	claimsJSON, _ := json.Marshal(claims)
	tok := base64.RawURLEncoding.EncodeToString(hdrJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + "."
	v := newVerifier(&fakeResolver{docs: map[string]*DIDDocument{}})
	_, err := v.Verify(context.Background(), tok, testLxm)
	if err == nil {
		t.Fatal("alg=none was accepted")
	}
}

func TestServiceAuthVerify_RejectTooLarge(t *testing.T) {
	huge := make([]byte, serviceAuthMaxJWTBytes+1)
	for i := range huge {
		huge[i] = 'x'
	}
	v := newVerifier(&fakeResolver{docs: map[string]*DIDDocument{}})
	_, err := v.Verify(context.Background(), string(huge), testLxm)
	if !errors.Is(err, ErrServiceAuthJWTTooLarge) {
		t.Fatalf("want ErrServiceAuthJWTTooLarge, got %v", err)
	}
}

func TestServiceAuthVerify_ResolverNotFound(t *testing.T) {
	iss := "did:web:ghost.example"
	signer := newP256Signer(t, iss)
	// Resolver has no entry for this DID.
	resolver := &fakeResolver{docs: map[string]*DIDDocument{}}
	v := newVerifier(resolver)
	tok := signer.mint(t, baseClaims(iss, time.Now()))
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthDIDResolveNotFound) {
		t.Fatalf("want ErrServiceAuthDIDResolveNotFound, got %v", err)
	}
}

func TestServiceAuthVerify_RejectWrongKeyType(t *testing.T) {
	// DID doc has a key, but type != Multikey.
	iss := "did:web:legacy.example"
	signer := newP256Signer(t, iss)
	doc := &DIDDocument{
		ID: iss,
		VerificationMethod: []VerificationMethod{{
			ID:                 iss + "#atproto",
			Type:               "EcdsaSecp256r1VerificationKey2019",
			Controller:         iss,
			PublicKeyMultibase: signer.multibasePub(t),
		}},
	}
	v := newVerifier(&fakeResolver{docs: map[string]*DIDDocument{iss: doc}})
	tok := signer.mint(t, baseClaims(iss, time.Now()))
	_, err := v.Verify(context.Background(), tok, testLxm)
	if !errors.Is(err, ErrServiceAuthVerificationMethodMissing) {
		t.Fatalf("want ErrServiceAuthVerificationMethodMissing, got %v", err)
	}
}

func TestReasonFor_Exhaustive(t *testing.T) {
	sentinels := []error{
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
	seen := make(map[string]bool)
	for _, s := range sentinels {
		r := ReasonFor(s)
		if r == "" || r == "other" {
			t.Errorf("%v mapped to %q", s, r)
		}
		if seen[r] {
			t.Errorf("duplicate reason label %q", r)
		}
		seen[r] = true
	}
	if ReasonFor(errors.New("random")) != "other" {
		t.Error("unknown errors should map to 'other'")
	}
	if ReasonFor(nil) != "" {
		t.Error("nil should map to empty string")
	}
}

// --- helpers -----------------------------------------------------------------

func splitDots(s string) [3]string {
	var out [3]string
	pos := 0
	for i := 0; i < 3; i++ {
		dot := indexOf(s, '.', pos)
		if dot < 0 || i == 2 {
			out[i] = s[pos:]
			return out
		}
		out[i] = s[pos:dot]
		pos = dot + 1
	}
	return out
}

func indexOf(s string, ch byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == ch {
			return i
		}
	}
	return -1
}

// rewriteISS re-signs the token with the attacker's key but with the
// victim's iss claim — simulating an attacker who learned a victim's
// DID and tries to impersonate them.
func rewriteISS(t *testing.T, _ string, attacker *testSigner, victimDID string) string {
	t.Helper()
	claims := baseClaims(victimDID, time.Now())
	return attacker.mint(t, claims)
}
