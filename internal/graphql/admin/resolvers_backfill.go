package admin

// Admin resolvers for backfill operations.
// Extracted from resolvers.go in 2026-05-17 Track 5; see
// docs/review-2026-05-17/plan.md for the partition rationale.

import (
	"context"
	"fmt"
	"log/slog"

	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
)

// TriggerBackfill starts a full backfill process.
// Uses atomic CompareAndSwap to prevent concurrent backfill launches (race-safe).
func (r *Resolver) TriggerBackfill(ctx context.Context) (bool, error) {
	if r.fullBackfillCallback == nil {
		return false, fmt.Errorf("full backfill not configured")
	}

	// Atomically check-and-set to prevent concurrent backfill launches
	if !r.backfillActive.CompareAndSwap(false, true) {
		return false, fmt.Errorf("backfill already in progress")
	}

	// Run backfill in background goroutine
	go func() {
		defer r.backfillActive.Store(false)

		// Use background context since HTTP request context will be cancelled
		if err := r.fullBackfillCallback(context.Background()); err != nil {
			slog.Error("[backfill] Full backfill failed in background", "error", err)
			return
		}
	}()

	return true, nil
}

// BackfillActor queues a single actor for backfill.
func (r *Resolver) BackfillActor(ctx context.Context, did string) (bool, error) {
	// Strict DID validation: prefix-only checks let newline / control-char
	// payloads into the actor table and log lines (commit c069afa).
	if !didpkg.IsValid(did) {
		return false, fmt.Errorf("invalid DID")
	}

	// Ensure actor exists (creates if not)
	if err := r.repos.Actors.Upsert(ctx, did, ""); err != nil {
		return false, fmt.Errorf("failed to register actor: %w", err)
	}

	// Trigger backfill callback if registered
	if r.backfillCallback != nil {
		if err := r.backfillCallback(ctx, did); err != nil {
			return false, fmt.Errorf("failed to trigger backfill: %w", err)
		}
	}

	return true, nil
}
