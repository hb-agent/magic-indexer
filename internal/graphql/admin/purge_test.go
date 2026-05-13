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

	token, exp, err := s.Sign(ScopeActorPurge, adminDID, targetDID, count)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Fatal("Sign returned empty token")
	}
	if !exp.After(time.Now().Add(-time.Second)) {
		t.Fatalf("Sign exp = %v, want > now", exp)
	}

	if err := s.Verify(token, ScopeActorPurge, adminDID, targetDID, count); err != nil {
		t.Fatalf("Verify(happy path): %v", err)
	}
}

func TestPurgeTokenSigner_RejectsTamperedSignature(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:a", "did:plc:t", 1)
	// Pick a different last byte than what's there. Previous version
	// just appended "_" which is a no-op ~1/64 of runs (when the
	// signature already ended in "_"), making the test flaky.
	last := token[len(token)-1]
	repl := byte('_')
	if last == repl {
		repl = 'A'
	}
	tampered := token[:len(token)-1] + string(repl)
	if err := s.Verify(tampered, ScopeActorPurge, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(tampered sig, last %q → %q) = %v, want ErrPurgeTokenInvalid", last, repl, err)
	}
}

func TestPurgeTokenSigner_RejectsTamperedPayload(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:a", "did:plc:t", 1)
	parts := strings.SplitN(token, ".", 2)
	// Modify the payload — signature no longer matches.
	tampered := parts[0] + "x." + parts[1]
	if err := s.Verify(tampered, ScopeActorPurge, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(tampered payload) = %v, want ErrPurgeTokenInvalid", err)
	}
}

func TestPurgeTokenSigner_RejectsWrongAdmin(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:adminA", "did:plc:target", 5)
	if err := s.Verify(token, ScopeActorPurge, "did:plc:adminB", "did:plc:target", 5); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(wrong admin) = %v, want ErrPurgeTokenInvalid — admin replay must be rejected", err)
	}
}

func TestPurgeTokenSigner_RejectsWrongTarget(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:targetA", 5)
	if err := s.Verify(token, ScopeActorPurge, "did:plc:admin", "did:plc:targetB", 5); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(wrong target) = %v, want ErrPurgeTokenInvalid — target replay must be rejected", err)
	}
}

func TestPurgeTokenSigner_RejectsCountDrift(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 5)
	// Distinct sentinel for count drift — split from
	// ErrPurgeTokenInvalid so forensics can tell apart "operator
	// raced an ingest" (benign) from "someone is forging tokens"
	// (active attack).
	if err := s.Verify(token, ScopeActorPurge, "did:plc:admin", "did:plc:target", 6); !errors.Is(err, ErrPurgeTokenCountDrift) {
		t.Errorf("Verify(count drift) = %v, want ErrPurgeTokenCountDrift", err)
	}
}

func TestPurgeTokenSigner_RejectsExpired(t *testing.T) {
	s, nowPtr := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 1)
	// Jump past TTL.
	*nowPtr = nowPtr.Add(purgeTokenTTL + time.Second)
	if err := s.Verify(token, ScopeActorPurge, "did:plc:admin", "did:plc:target", 1); !errors.Is(err, ErrPurgeTokenExpired) {
		t.Errorf("Verify(expired) = %v, want ErrPurgeTokenExpired", err)
	}
}

func TestPurgeTokenSigner_RejectsReplay(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 1)
	if err := s.Verify(token, ScopeActorPurge, "did:plc:admin", "did:plc:target", 1); err != nil {
		t.Fatalf("Verify(first use): %v", err)
	}
	if err := s.Verify(token, ScopeActorPurge, "did:plc:admin", "did:plc:target", 1); !errors.Is(err, ErrPurgeTokenAlreadyUsed) {
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
		if err := s.Verify(c, ScopeActorPurge, "did:plc:a", "did:plc:t", 1); !errors.Is(err, ErrPurgeTokenInvalid) {
			t.Errorf("Verify(%q) = %v, want ErrPurgeTokenInvalid", c, err)
		}
	}
}

func TestPurgeTokenSigner_PrunesExpiredSigs(t *testing.T) {
	s, nowPtr := newSigner(t)
	// Sign + verify a token (so it lands in usedSigs).
	t1, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 1)
	if err := s.Verify(t1, ScopeActorPurge, "did:plc:admin", "did:plc:target", 1); err != nil {
		t.Fatalf("Verify t1: %v", err)
	}
	// Step well past its expiry, then sign + verify a second
	// token. The lazy-prune branch in Verify should drop t1's
	// entry from usedSigs.
	*nowPtr = nowPtr.Add(purgeTokenTTL * 2)
	t2, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 2)
	if err := s.Verify(t2, ScopeActorPurge, "did:plc:admin", "did:plc:target", 2); err != nil {
		t.Fatalf("Verify t2: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stillThere := s.usedSigs[strings.SplitN(t1, ".", 2)[1]]; stillThere {
		t.Error("expired signature not pruned from usedSigs")
	}
}

// TestPurgeTokenSigner_RejectsScopeMismatch verifies that a token
// minted for the actor_purge scope cannot be redeemed against the
// reset_all scope (or vice versa). Without this guard, an admin
// previewing an actor purge could see their token redeemed against
// the strictly-more-destructive ResetAll mutation.
func TestPurgeTokenSigner_RejectsScopeMismatch(t *testing.T) {
	s, _ := newSigner(t)

	// Token minted for actor_purge, verified as reset_all → reject.
	t1, _, _ := s.Sign(ScopeActorPurge, "did:plc:admin", "did:plc:target", 5)
	if err := s.Verify(t1, ScopeResetAll, "did:plc:admin", "did:plc:target", 5); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(actor_purge token as reset_all) = %v, want ErrPurgeTokenInvalid", err)
	}

	// Token minted for reset_all, verified as actor_purge → reject.
	// reset_all binds an empty target DID.
	t2, _, _ := s.Sign(ScopeResetAll, "did:plc:admin", "", 100)
	if err := s.Verify(t2, ScopeActorPurge, "did:plc:admin", "", 100); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(reset_all token as actor_purge) = %v, want ErrPurgeTokenInvalid", err)
	}
}

// TestPurgeTokenSigner_ResetAllScopeRoundTrip exercises the full
// sign-verify cycle under the new reset_all scope. TargetDID is
// empty for this scope; the binding is admin + count + scope.
func TestPurgeTokenSigner_ResetAllScopeRoundTrip(t *testing.T) {
	s, _ := newSigner(t)
	const adminDID = "did:plc:admin"
	const totalRows = int64(123456)

	token, _, err := s.Sign(ScopeResetAll, adminDID, "", totalRows)
	if err != nil {
		t.Fatalf("Sign(reset_all): %v", err)
	}
	if err := s.Verify(token, ScopeResetAll, adminDID, "", totalRows); err != nil {
		t.Errorf("Verify(reset_all happy path): %v", err)
	}
}

// TestPurgeTokenSigner_RejectsResetAllAdminMismatch verifies the
// admin-binding check holds under the reset_all scope. Without it,
// admin A's preview token could be redeemed by admin B.
func TestPurgeTokenSigner_RejectsResetAllAdminMismatch(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeResetAll, "did:plc:adminA", "", 10)
	if err := s.Verify(token, ScopeResetAll, "did:plc:adminB", "", 10); !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("Verify(reset_all wrong admin) = %v, want ErrPurgeTokenInvalid", err)
	}
}

// TestPurgeTokenSigner_RejectsResetAllCountDrift verifies count
// binding under reset_all. If the operator previewed at 100 rows
// and a Jetstream message lands a 101st before they confirm, the
// token rejects with the distinct count-drift sentinel and the
// operator re-previews.
func TestPurgeTokenSigner_RejectsResetAllCountDrift(t *testing.T) {
	s, _ := newSigner(t)
	token, _, _ := s.Sign(ScopeResetAll, "did:plc:admin", "", 100)
	if err := s.Verify(token, ScopeResetAll, "did:plc:admin", "", 101); !errors.Is(err, ErrPurgeTokenCountDrift) {
		t.Errorf("Verify(reset_all count drift) = %v, want ErrPurgeTokenCountDrift", err)
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
