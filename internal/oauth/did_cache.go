// Package oauth provides AT Protocol OAuth implementation.
// DID document caching with TTL support.
package oauth

import (
	"sync"
	"time"
)

// CachedDIDDocument wraps a DID document with expiration.
type CachedDIDDocument struct {
	Document  *DIDDocument
	ExpiresAt time.Time
}

// IsExpired returns true if the cache entry has expired.
func (c *CachedDIDDocument) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// DIDCache provides caching for DID documents with TTL.
type DIDCache struct {
	mu       sync.RWMutex
	cache    map[string]*CachedDIDDocument
	ttl      time.Duration
	resolver *DIDResolver
}

// DIDCacheOption configures the DID cache.
type DIDCacheOption func(*DIDCache)

// WithCacheTTL sets the cache TTL duration.
func WithCacheTTL(ttl time.Duration) DIDCacheOption {
	return func(c *DIDCache) {
		c.ttl = ttl
	}
}

// WithResolver sets the DID resolver to use.
func WithResolver(resolver *DIDResolver) DIDCacheOption {
	return func(c *DIDCache) {
		c.resolver = resolver
	}
}

// NewDIDCache creates a new DID cache.
func NewDIDCache(opts ...DIDCacheOption) *DIDCache {
	c := &DIDCache{
		cache:    make(map[string]*CachedDIDDocument),
		ttl:      time.Hour, // Default 1 hour TTL
		resolver: NewDIDResolver(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get retrieves a DID document from cache, resolving if not cached or expired.
func (c *DIDCache) Get(did string) (*DIDDocument, error) {
	return c.GetWithInvalidate(did, false)
}

// GetWithInvalidate retrieves a DID document, optionally invalidating cache first.
func (c *DIDCache) GetWithInvalidate(did string, invalidateFirst bool) (*DIDDocument, error) {
	if invalidateFirst {
		c.Invalidate(did)
	}

	// Try cache first
	c.mu.RLock()
	cached, found := c.cache[did]
	c.mu.RUnlock()

	if found && !cached.IsExpired() {
		return cached.Document, nil
	}

	// Resolve fresh
	return c.resolveFresh(did)
}

// resolveFresh resolves a DID and caches the result.
func (c *DIDCache) resolveFresh(did string) (*DIDDocument, error) {
	doc, err := c.resolver.ResolveDID(did)
	if err != nil {
		return nil, err
	}

	// Cache the result
	c.Put(did, doc)

	return doc, nil
}

// Put stores a DID document in the cache.
func (c *DIDCache) Put(did string, doc *DIDDocument) {
	c.PutWithTTL(did, doc, c.ttl)
}

// PutWithTTL stores a DID document in the cache with a custom TTL.
func (c *DIDCache) PutWithTTL(did string, doc *DIDDocument, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[did] = &CachedDIDDocument{
		Document:  doc,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// Invalidate removes a DID document from the cache.
func (c *DIDCache) Invalidate(did string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, did)
}

// Delete is an alias for Invalidate.
func (c *DIDCache) Delete(did string) {
	c.Invalidate(did)
}

// Clear removes all entries from the cache.
func (c *DIDCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*CachedDIDDocument)
}

// Cleanup removes all expired entries from the cache.
func (c *DIDCache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	now := time.Now()
	for did, cached := range c.cache {
		if now.After(cached.ExpiresAt) {
			delete(c.cache, did)
			removed++
		}
	}
	return removed
}

// Size returns the number of entries in the cache.
func (c *DIDCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// Contains checks if a DID is in the cache (not checking expiration).
func (c *DIDCache) Contains(did string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, found := c.cache[did]
	return found
}

// StartCleanupRoutine starts a background goroutine that periodically cleans up expired entries.
// Returns a stop function to cancel the routine.
func (c *DIDCache) StartCleanupRoutine(interval time.Duration) func() {
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.Cleanup()
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
	}
}
