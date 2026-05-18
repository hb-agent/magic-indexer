// Package workers provides background worker implementations.
package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
)

// activityCleaner is the minimal surface ActivityCleanupWorker needs
// from the activity repository. Extracted so the worker can be tested
// against a fake without depending on a real Postgres instance — the
// previous test was a tautology that re-implemented the production
// goroutine inline instead of exercising it, which is how the bug
// where the ticker was Stop'd outside the goroutine reached
// production (overnight finding C-1 + T-1).
type activityCleaner interface {
	CleanupOldActivity(ctx context.Context, retentionHrs int) error
	OrphanPendingActivity(ctx context.Context, olderThanMinutes int) (int64, error)
}

// ActivityCleanupWorker periodically cleans up old activity entries.
type ActivityCleanupWorker struct {
	activity     activityCleaner
	interval     time.Duration
	retentionHrs int
	stop         chan struct{}
	done         chan struct{}
}

// NewActivityCleanupWorker creates a new activity cleanup worker.
func NewActivityCleanupWorker(activity *repositories.JetstreamActivityRepository) *ActivityCleanupWorker {
	return &ActivityCleanupWorker{
		activity:     activity,
		interval:     time.Hour, // Run every hour
		retentionHrs: 24 * 7,    // Keep 7 days of activity
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start begins the cleanup worker.
//
// The ticker is created INSIDE the goroutine — the previous shape
// created it in Start() and registered `defer ticker.Stop()` in
// Start()'s scope. That defer fired the moment Start() returned,
// stopping the ticker before the goroutine ever read from its
// channel. The cleanup() call only ever ran once, at boot, and the
// 7-day retention + 10-minute orphan janitor were silently dead in
// production for the lifetime of this worker. See overnight finding
// C-1 + T-1.
func (w *ActivityCleanupWorker) Start(ctx context.Context) {
	slog.Info("Starting activity cleanup worker",
		"interval", w.interval,
		"retention_hours", w.retentionHrs)

	// Run immediately on start, before the periodic loop, so the first
	// cleanup happens at boot rather than waiting for the first tick.
	w.cleanup(ctx)

	go func() {
		defer close(w.done)

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("Activity cleanup worker stopping (context cancelled)")
				return
			case <-w.stop:
				slog.Info("Activity cleanup worker stopping (stop signal)")
				return
			case <-ticker.C:
				w.cleanup(ctx)
			}
		}
	}()
}

// Stop gracefully stops the worker.
func (w *ActivityCleanupWorker) Stop() {
	close(w.stop)
	<-w.done
}

// orphanAfterMinutes bounds how long an activity row may sit in the
// 'pending' state before the janitor flips it to 'orphaned'. 10 min
// is long enough that a normal write + status update (sub-second)
// can't race the sweep, short enough that the admin UI doesn't
// accumulate zombie rows for hours after a crash.
const orphanAfterMinutes = 10

func (w *ActivityCleanupWorker) cleanup(ctx context.Context) {
	slog.Debug("Running activity cleanup", "retention_hours", w.retentionHrs)

	if err := w.activity.CleanupOldActivity(ctx, w.retentionHrs); err != nil {
		slog.Error("Failed to cleanup old activity", "error", err)
		// Fall through: orphan sweep is independent.
	}

	// Mark any pending activity rows older than orphanAfterMinutes as
	// orphaned. LogActivity + UpdateStatus are two separate writes
	// so a crash between them leaves the row in pending forever;
	// this janitor closes that out.
	n, err := w.activity.OrphanPendingActivity(ctx, orphanAfterMinutes)
	if err != nil {
		slog.Error("Failed to orphan pending activity", "error", err)
	} else if n > 0 {
		slog.Info("Marked stale pending activity as orphaned",
			"count", n, "age_minutes", orphanAfterMinutes)
	}

	slog.Debug("Activity cleanup completed")
}
