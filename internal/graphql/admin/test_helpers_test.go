package admin

// Test-only helpers for the admin package. Lives in a *_test.go
// file so it is not compiled into the production binary and
// cannot be imported from non-test code. Keep the production
// surface (purge.go, resolvers.go) free of test scaffolding.

import (
	"context"
	"sync"
)

// stubTapRemover is a configurable TapRemover for resolver tests.
// Records every invocation so tests can assert the resolver hit
// the Tap leg exactly once with the expected DIDs; and can be
// pre-loaded with an error so the "Tap failure, SQL still
// succeeds" path is testable without spinning up a real Tap
// sidecar.
//
// Thread-safe so a future race test that fires purges in parallel
// doesn't fight the stub's bookkeeping.
type stubTapRemover struct {
	mu      sync.Mutex
	err     error
	callLog [][]string
}

// setErr configures the next (and subsequent) RemoveRepos call to
// return the given error. Pass nil to clear.
func (s *stubTapRemover) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// calls returns a copy of the call log. Each entry is the slice of
// DIDs passed to RemoveRepos in the order received. Returning a
// copy means tests can append / mutate without poisoning future
// observations.
func (s *stubTapRemover) calls() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.callLog))
	for i, c := range s.callLog {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// RemoveRepos implements the TapRemover interface. Stores the
// argument slice and returns the configured error.
func (s *stubTapRemover) RemoveRepos(ctx context.Context, dids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callLog = append(s.callLog, append([]string(nil), dids...))
	return s.err
}
