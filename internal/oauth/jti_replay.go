package oauth

import (
	"container/heap"
	"sync"
	"time"
)

// jtiReplayCache tracks recently-seen service-auth tokens to prevent
// replay inside the acceptance window. Keys are composite strings so the
// same random `jti` from two different issuers doesn't collide. When the
// caller omits `jti`, the verifier synthesises a key from the token's
// signature bytes plus claims (see synthReplayKey) so ECDSA's
// non-deterministic signing still yields a unique key per mint.
//
// The cache is bounded: on insertion beyond capacity, the entry with the
// earliest expiry is evicted. This is the correct eviction policy — the
// oldest entry is closest to falling out of the acceptance window
// naturally anyway, and biasing toward eviction-by-expiry prevents an
// attacker from churning fresh tokens to push legitimate ones out.
//
// Single-process only. A multi-replica deploy would need to move this
// to Postgres (reusing the existing oauth_dpop_jti table's pattern is
// the planned follow-up).
type jtiReplayCache struct {
	mu       sync.Mutex
	entries  map[string]*jtiEntry
	heap     jtiExpiryHeap
	capacity int
}

type jtiEntry struct {
	key       string
	expiresAt time.Time
	heapIdx   int
}

// newJTIReplayCache constructs a cache with the given capacity. Capacity
// must be positive; zero or negative is a programmer error and panics.
func newJTIReplayCache(capacity int) *jtiReplayCache {
	if capacity <= 0 {
		panic("jtiReplayCache: capacity must be positive")
	}
	return &jtiReplayCache{
		entries:  make(map[string]*jtiEntry, capacity),
		capacity: capacity,
	}
}

// checkAndSet atomically records the (key, expiresAt) pair. Returns true
// if the key was not present (caller should accept the token), false if
// the key already exists (replay — caller must reject).
//
// Eviction runs inline: expired entries at the top of the heap are purged
// first, then if the cache is still at capacity the oldest-by-expiry
// entry is dropped to make room.
func (c *jtiReplayCache) checkAndSet(key string, expiresAt, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Lazy purge of already-expired entries. Bounded by how many entries
	// happen to be past expiry right now — amortises to O(1) per op.
	for c.heap.Len() > 0 {
		top := c.heap[0]
		if top.expiresAt.After(now) {
			break
		}
		heap.Pop(&c.heap)
		delete(c.entries, top.key)
	}

	if _, exists := c.entries[key]; exists {
		return false
	}

	if len(c.entries) >= c.capacity {
		// Evict the oldest-by-expiry to make room. Documented trade-off:
		// under sustained burst this can evict a still-valid jti and
		// re-open a replay window — the cache size must be tuned to
		// peak RPS × MaxLifetime. See §Replay cache in the plan.
		if c.heap.Len() > 0 {
			victim := heap.Pop(&c.heap).(*jtiEntry)
			delete(c.entries, victim.key)
		}
	}

	entry := &jtiEntry{key: key, expiresAt: expiresAt}
	c.entries[key] = entry
	heap.Push(&c.heap, entry)
	return true
}

// size returns the current entry count. Lock-held read — only useful for
// tests or metric sampling.
func (c *jtiReplayCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// --- min-heap on expiresAt ---

type jtiExpiryHeap []*jtiEntry

func (h jtiExpiryHeap) Len() int           { return len(h) }
func (h jtiExpiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h jtiExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIdx = i
	h[j].heapIdx = j
}

func (h *jtiExpiryHeap) Push(x any) {
	e := x.(*jtiEntry)
	e.heapIdx = len(*h)
	*h = append(*h, e)
}

func (h *jtiExpiryHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.heapIdx = -1
	*h = old[:n-1]
	return e
}
