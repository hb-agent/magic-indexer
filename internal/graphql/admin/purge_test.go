package admin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const testSecret = "a-test-secret-of-sufficient-entropy-for-hmac-sha256-keying-please-yes"

func newSigner(t *testing.T) (*PurgeTokenSigner, *time.Time) {
	t.Helper()
	s, err := NewPurgeTokenSigner([]byte(testSecret))
	if err != nil {
		t.Fatalf("NewPurgeTokenSigner: %v", err)
	}
	// Make `now` controllable so tests can step past the TTL
	// without sleeping.
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	return s, &now
}

func TestPurgeTokenSigner_RoundTrip(t *testing.T) {
	s, _ := newSigner(t)
	const adminDID = "did:plc:adminA"
	const targetDID = "did:plc:target1"
	const count = int64(42)

	token, exp, err := s.Sign(adminDID, targetDID, count)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Fatal("Sign returned empty token")
	}
	if !exp.After(time.Now().Add(-time.Second)) {
		t.Fatalf("Sign exp = %v, want > now", exp)
	}

	if err := s.Verify(token, adminDID, targetDID, count); err != nil {
		t.Fatalf("Verify(happy path): %v", err)
	}
}

func TestPurgeTokenSigner_RejectsTamperedSignature(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:a", "did:plc:t", 1)
	// Flip the last character of the signature half. If it's the
	// last char of the token, swap "A" / "B" / "C" so we end up
	// with a different but still-valid base64url byte.
	tampered := token[:len(token)-1] + "_"
	if err := s.Verify(tampered, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(tampered sig) = %v, want ErrPurgeTokenInvalid", err)
	}
}

func TestPurgeTokenSigner_RejectsTamperedPayload(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:a", "did:plc:t", 1)
	parts := strings.SplitN(token, ".", 2)
	// Modify the payload — signature no longer matches.
	tampered := parts[0] + "x." + parts[1]
	if err := s.Verify(tampered, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(tampered payload) = %v, want ErrPurgeTokenInvalid", err)
	}
}

func TestPurgeTokenSigner_RejectsWrongAdmin(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:adminA", "did:plc:target", 5)
	if err := s.Verify(token, "did:plc:adminB", "did:plc:target", 5); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(wrong admin) = %v, want ErrPurgeTokenInvalid — admin replay must be rejected", err)
	}
}

func TestPurgeTokenSigner_RejectsWrongTarget(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:admin", "did:plc:targetA", 5)
	if err := s.Verify(token, "did:plc:admin", "did:plc:targetB", 5); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(wrong target) = %v, want ErrPurgeTokenInvalid — target replay must be rejected", err)
	}
}

func TestPurgeTokenSigner_RejectsCountDrift(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:admin", "did:plc:target", 5)
	if err := s.Verify(token, "did:plc:admin", "did:plc:target", 6); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(count drift) = %v, want ErrPurgeTokenInvalid — count must match preview to prevent racing new ingest", err)
	}
}

func TestPurgeTokenSigner_RejectsExpired(t *testing.T) {
	s, nowPtr := newSigner(t)
	token, _, _ := s.Sign("did:plc:admin", "did:plc:target", 1)
	// Jump past TTL.
	*nowPtr = nowPtr.Add(purgeTokenTTL + time.Second)
	if err := s.Verify(token, "did:plc:admin", "did:plc:target", 1); !errors.Is(err, ErrPurgeTokenExpired) {
		t.Errorf("Verify(expired) = %v, want ErrPurgeTokenExpired", err)
	}
}

func TestPurgeTokenSigner_RejectsReplay(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign("did:plc:admin", "did:plc:target", 1)
	if err := s.Verify(token, "did:plc:admin", "did:plc:target", 1); err != nil {
		t.Fatalf("Verify(first use): %v", err)
	}
	if err := s.Verify(token, "did:plc:admin", "did:plc:target", 1); !errors.Is(err, ErrPurgeTokenAlreadyUsed) {
		t.Errorf("Verify(second use) = %v, want ErrPurgeTokenAlreadyUsed — single-use must be enforced", err)
	}
}

func TestPurgeTokenSigner_RejectsMalformedToken(t *testing.T) {
	s, _ := newSigner(t)
	cases := []string{
		"",
		"no-dot",
		".",
		"only-payload.",
		".only-sig",
		"not!base64.also!not",
	}
	for _, c := range cases {
		if err := s.Verify(c, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
			t.Errorf("Verify(%q) = %v, want ErrPurgeTokenInvalid", c, err)
		}
	}
}

func TestPurgeTokenSigner_PrunesExpiredSigs(t *testing.T) {
	s, nowPtr := newSigner(t)
	// Sign + verify a token (so it lands in usedSigs).
	t1, _, _ := s.Sign("did:plc:admin", "did:plc:target", 1)
	if err := s.Verify(t1, "did:plc:admin", "did:plc:target", 1); err != nil {
		t.Fatalf("Verify t1: %v", err)
	}
	// Step well past its expiry, then sign + verify a second
	// token. The lazy-prune branch in Verify should drop t1's
	// entry from usedSigs.
	*nowPtr = nowPtr.Add(purgeTokenTTL * 2)
	t2, _, _ := s.Sign("did:plc:admin", "did:plc:target", 2)
	if err := s.Verify(t2, "did:plc:admin", "did:plc:target", 2); err != nil {
		t.Fatalf("Verify t2: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stillThere := s.usedSigs[strings.SplitN(t1, ".", 2)[1]]; stillThere {
		t.Error("expired signature not pruned from usedSigs")
	}
}

func TestNewPurgeTokenSigner_RejectsEmptySecret(t *testing.T) {
	if _, err := NewPurgeTokenSigner(nil); err == nil {
		t.Error("NewPurgeTokenSigner(nil) succeeded; empty secret must be rejected")
	}
	if _, err := NewPurgeTokenSigner([]byte{}); err == nil {
		t.Error("NewPurgeTokenSigner(empty) succeeded; empty secret must be rejected")
	}
}
