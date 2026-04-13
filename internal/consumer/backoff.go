// Package consumer provides shared infrastructure for event consumers
// (Jetstream, Labeler, Tap).
package consumer

import (
	"context"
	"log/slog"
	"time"
)

// BackoffOpts configures reconnection backoff behavior.
type BackoffOpts struct {
	// MinBackoff is the initial delay after a disconnect (default 1s).
	MinBackoff time.Duration
	// MaxBackoff is the maximum delay between reconnection attempts (default 2min).
	MaxBackoff time.Duration
	// StableAfter is how long a connection must be up before backoff resets (default 30s).
	StableAfter time.Duration
}

func (o *BackoffOpts) defaults() {
	if o.MinBackoff == 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 2 * time.Minute
	}
	if o.StableAfter == 0 {
		o.StableAfter = 30 * time.Second
	}
}

// RunWithReconnect calls connectFn in a loop with exponential backoff on
// failure. If the connection stays up longer than opts.StableAfter, the
// backoff resets to opts.MinBackoff. Returns only when ctx is cancelled.
func RunWithReconnect(ctx context.Context, connectFn func(ctx context.Context) error, opts BackoffOpts) error {
	opts.defaults()
	backoff := opts.MinBackoff

	for {
		connStart := time.Now()
		err := connectFn(ctx)

		// Check if we should stop.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Reset backoff if the connection was stable.
		if time.Since(connStart) > opts.StableAfter {
			backoff = opts.MinBackoff
		}

		if err != nil {
			slog.Error("Connection lost, will reconnect",
				"error", err,
				"backoff", backoff,
			)
		} else {
			slog.Warn("Connection closed unexpectedly, will reconnect",
				"backoff", backoff,
			)
		}

		// Wait before reconnecting.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff with cap.
		backoff *= 2
		if backoff > opts.MaxBackoff {
			backoff = opts.MaxBackoff
		}

		slog.Info("Attempting to reconnect...")
	}
}
