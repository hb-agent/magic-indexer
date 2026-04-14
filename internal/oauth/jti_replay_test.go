package oauth

import (
	"sync"
	"testing"
	"time"
)

func TestJTIReplayCache_CheckAndSet(t *testing.T) {
	cache := newJTIReplayCache(10)
	now := time.Unix(1_700_000_000, 0)

	if !cache.checkAndSet("a", now.Add(60*time.Second), now) {
		t.Fatal("first insert should accept")
	}
	if cache.checkAndSet("a", now.Add(60*time.Second), now) {
		t.Fatal("second insert of same key should reject (replay)")
	}
	if !cache.checkAndSet("b", now.Add(60*time.Second), now) {
		t.Fatal("different key should accept")
	}
}

func TestJTIReplayCache_ExpiryPurge(t *testing.T) {
	cache := newJTIReplayCache(10)
	now := time.Unix(1_700_000_000, 0)

	cache.checkAndSet("a", now.Add(60*time.Second), now)
	// Advance past expiry — next op should lazily evict "a" so re-insert works.
	later := now.Add(120 * time.Second)
	if !cache.checkAndSet("a", later.Add(60*time.Second), later) {
		t.Fatal("after expiry, re-insert should accept")
	}
}

func TestJTIReplayCache_CapacityEviction(t *testing.T) {
	cache := newJTIReplayCache(3)
	now := time.Unix(1_700_000_000, 0)

	// Insert three entries with increasing expiry.
	cache.checkAndSet("oldest", now.Add(1*time.Second), now)
	cache.checkAndSet("mid", now.Add(30*time.Second), now)
	cache.checkAndSet("newest", now.Add(60*time.Second), now)
	if cache.size() != 3 {
		t.Fatalf("size = %d, want 3", cache.size())
	}

	// Fourth insert should evict "oldest" (smallest expiry) but NOT by
	// reaching its expiry — this exercises the at-capacity eviction path.
	cache.checkAndSet("fourth", now.Add(120*time.Second), now)
	if cache.size() != 3 {
		t.Fatalf("size after over-capacity insert = %d, want 3", cache.size())
	}

	// "oldest" has been evicted; re-inserting it should succeed (the cache
	// no longer knows it). This is the documented replay-under-pressure
	// trade-off.
	if !cache.checkAndSet("oldest", now.Add(10*time.Second), now) {
		t.Fatal("evicted entry should be re-insertable")
	}
}

func TestJTIReplayCache_Concurrent(t *testing.T) {
	cache := newJTIReplayCache(1000)
	now := time.Unix(1_700_000_000, 0)

	// 50 goroutines race on the same key. Exactly one must win.
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	accepted := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			accepted <- cache.checkAndSet("contested", now.Add(60*time.Second), now)
		}()
	}
	wg.Wait()
	close(accepted)

	wins := 0
	for ok := range accepted {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("contested checkAndSet: got %d winners, want exactly 1", wins)
	}
}
