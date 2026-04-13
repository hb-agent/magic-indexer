// Package cursor provides shared cursor persistence for event consumers.
package cursor

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Flusher periodically writes a cursor position to persistent storage.
// The cursor value is updated atomically by the consumer goroutine and
// flushed to storage by the Flusher's Run goroutine.
type Flusher struct {
	cursor   atomic.Int64
	Save     func(ctx context.Context, cursor int64) error
	Interval time.Duration
	// Label is used in log messages to identify which consumer this flusher
	// belongs to (e.g. "jetstream", "labeler:did:plc:xyz").
	Label string
}

// NewFlusher creates a Flusher with the given save function and interval.
func NewFlusher(save func(ctx context.Context, cursor int64) error, interval time.Duration, label string) *Flusher {
	if interval == 0 {
		interval = 5 * time.Second
	}
	return &Flusher{
		Save:     save,
		Interval: interval,
		Label:    label,
	}
}

// SetCurrent atomically stores the current cursor value.
func (f *Flusher) SetCurrent(v int64) {
	f.cursor.Store(v)
}

// GetCurrent atomically loads the current cursor value.
func (f *Flusher) GetCurrent() int64 {
	return f.cursor.Load()
}

// Run flushes the cursor periodically until ctx is cancelled.
// On cancellation, it performs a final flush with a bounded timeout.
func (f *Flusher) Run(ctx context.Context) {
	ticker := time.NewTicker(f.Interval)
	defer ticker.Stop()

	var lastFlushed int64

	for {
		select {
		case <-ctx.Done():
			f.finalFlush()
			return
		case <-ticker.C:
			current := f.cursor.Load()
			if current > lastFlushed {
				// Use a short-lived context for the save so a concurrent
				// parent cancellation doesn't spuriously fail the write.
				saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := f.Save(saveCtx, current); err != nil {
					slog.Warn("Failed to flush cursor",
						"label", f.Label, "error", err)
				} else {
					lastFlushed = current
				}
				cancel()
			}
		}
	}
}

// finalFlush writes the cursor one last time with a bounded timeout.
func (f *Flusher) finalFlush() {
	current := f.cursor.Load()
	if current <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Save(ctx, current); err != nil {
		slog.Warn("Failed to flush cursor on shutdown",
			"label", f.Label, "error", err)
	}
}
