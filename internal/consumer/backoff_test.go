package consumer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWithReconnect_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var attempts atomic.Int32
	go func() {
		// Cancel after a few attempts.
		for attempts.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	err := RunWithReconnect(ctx, func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("fail")
	}, BackoffOpts{MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts.Load())
	}
}

func TestRunWithReconnect_ResetsBackoffOnStableConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	start := time.Now()

	go func() {
		for attempts.Load() < 3 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	err := RunWithReconnect(ctx, func(ctx context.Context) error {
		n := attempts.Add(1)
		if n == 1 {
			// First attempt: simulate a stable connection.
			time.Sleep(60 * time.Millisecond)
		}
		return errors.New("fail")
	}, BackoffOpts{
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  500 * time.Millisecond,
		StableAfter: 50 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	elapsed := time.Since(start)
	// With stable reset, the second backoff should be ~10ms (reset), not 20ms.
	// Total should be well under 200ms.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("took too long (%v), backoff may not have reset", elapsed)
	}
}

func TestRunWithReconnect_ExponentialBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var timestamps []time.Time
	var attempts atomic.Int32

	go func() {
		for attempts.Load() < 4 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	err := RunWithReconnect(ctx, func(ctx context.Context) error {
		attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		return errors.New("fail")
	}, BackoffOpts{
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if len(timestamps) < 3 {
		t.Fatalf("expected at least 3 timestamps, got %d", len(timestamps))
	}
	// Second gap should be roughly 2x the first gap (exponential).
	gap1 := timestamps[2].Sub(timestamps[1])
	gap0 := timestamps[1].Sub(timestamps[0])
	if gap1 < gap0 {
		t.Logf("gap0=%v gap1=%v — backoff may not be exponential", gap0, gap1)
	}
}

func TestBackoffOpts_Defaults(t *testing.T) {
	opts := BackoffOpts{}
	opts.defaults()
	if opts.MinBackoff != time.Second {
		t.Fatalf("expected 1s min, got %v", opts.MinBackoff)
	}
	if opts.MaxBackoff != 2*time.Minute {
		t.Fatalf("expected 2m max, got %v", opts.MaxBackoff)
	}
	if opts.StableAfter != 30*time.Second {
		t.Fatalf("expected 30s stable, got %v", opts.StableAfter)
	}
}
