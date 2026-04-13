package cursor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlusher_SkipsWhenNotAdvanced(t *testing.T) {
	var saveCount atomic.Int32
	f := NewFlusher(func(ctx context.Context, cursor int64) error {
		saveCount.Add(1)
		return nil
	}, 20*time.Millisecond, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	// Don't set any cursor — save should not be called.
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	// Only the final flush might fire (with cursor=0, which is skipped).
	if saveCount.Load() > 0 {
		t.Fatalf("expected 0 saves for idle cursor, got %d", saveCount.Load())
	}
}

func TestFlusher_FlushesOnAdvance(t *testing.T) {
	var lastSaved atomic.Int64
	f := NewFlusher(func(ctx context.Context, cursor int64) error {
		lastSaved.Store(cursor)
		return nil
	}, 20*time.Millisecond, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	f.SetCurrent(42)
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	if lastSaved.Load() != 42 {
		t.Fatalf("expected cursor 42, got %d", lastSaved.Load())
	}
}

func TestFlusher_AtomicReadWrite(t *testing.T) {
	f := NewFlusher(func(ctx context.Context, cursor int64) error {
		return nil
	}, time.Second, "test")

	f.SetCurrent(100)
	if got := f.GetCurrent(); got != 100 {
		t.Fatalf("expected 100, got %d", got)
	}

	f.SetCurrent(200)
	if got := f.GetCurrent(); got != 200 {
		t.Fatalf("expected 200, got %d", got)
	}
}

func TestFlusher_FinalFlushOnCancel(t *testing.T) {
	var lastSaved atomic.Int64
	f := NewFlusher(func(ctx context.Context, cursor int64) error {
		lastSaved.Store(cursor)
		return nil
	}, time.Hour, "test") // Long interval — won't fire during test.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	f.SetCurrent(999)
	time.Sleep(10 * time.Millisecond) // Let Run start.
	cancel()
	<-done

	// Final flush should have saved.
	if lastSaved.Load() != 999 {
		t.Fatalf("expected final flush to save 999, got %d", lastSaved.Load())
	}
}

func TestNewFlusher_DefaultInterval(t *testing.T) {
	f := NewFlusher(func(ctx context.Context, cursor int64) error {
		return nil
	}, 0, "test")

	if f.Interval != 5*time.Second {
		t.Fatalf("expected 5s default, got %v", f.Interval)
	}
}
