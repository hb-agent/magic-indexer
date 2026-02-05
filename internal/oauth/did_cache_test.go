package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNewDIDCache(t *testing.T) {
	cache := NewDIDCache()

	if cache.ttl != time.Hour {
		t.Errorf("Default TTL = %v, want %v", cache.ttl, time.Hour)
	}
	if cache.resolver == nil {
		t.Error("Resolver should not be nil")
	}
	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0", cache.Size())
	}
}

func TestDIDCache_PutAndGet(t *testing.T) {
	cache := NewDIDCache()

	doc := &DIDDocument{
		ID:          "did:plc:test123",
		AlsoKnownAs: []string{"at://test.bsky.social"},
	}

	cache.Put("did:plc:test123", doc)

	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
	if !cache.Contains("did:plc:test123") {
		t.Error("Contains() = false, want true")
	}
}

func TestDIDCache_Invalidate(t *testing.T) {
	cache := NewDIDCache()

	doc := &DIDDocument{ID: "did:plc:test123"}
	cache.Put("did:plc:test123", doc)

	if cache.Size() != 1 {
		t.Fatalf("Size() = %d, want 1", cache.Size())
	}

	cache.Invalidate("did:plc:test123")

	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0", cache.Size())
	}
	if cache.Contains("did:plc:test123") {
		t.Error("Contains() = true, want false after invalidate")
	}
}

func TestDIDCache_Clear(t *testing.T) {
	cache := NewDIDCache()

	cache.Put("did:plc:test1", &DIDDocument{ID: "did:plc:test1"})
	cache.Put("did:plc:test2", &DIDDocument{ID: "did:plc:test2"})
	cache.Put("did:plc:test3", &DIDDocument{ID: "did:plc:test3"})

	if cache.Size() != 3 {
		t.Fatalf("Size() = %d, want 3", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0 after Clear()", cache.Size())
	}
}

func TestDIDCache_Expiration(t *testing.T) {
	cache := NewDIDCache(WithCacheTTL(50 * time.Millisecond))

	doc := &DIDDocument{ID: "did:plc:test123"}
	cache.Put("did:plc:test123", doc)

	// Check it's cached
	if !cache.Contains("did:plc:test123") {
		t.Fatal("Document should be cached")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Check cached entry (note: Contains doesn't check expiration)
	cache.mu.RLock()
	cached, found := cache.cache["did:plc:test123"]
	cache.mu.RUnlock()

	if !found {
		t.Fatal("Entry should still exist")
	}
	if !cached.IsExpired() {
		t.Error("Entry should be expired")
	}
}

func TestDIDCache_Cleanup(t *testing.T) {
	cache := NewDIDCache()

	// Add entries with very short TTL
	cache.PutWithTTL("did:plc:expired1", &DIDDocument{ID: "did:plc:expired1"}, 1*time.Millisecond)
	cache.PutWithTTL("did:plc:expired2", &DIDDocument{ID: "did:plc:expired2"}, 1*time.Millisecond)
	cache.PutWithTTL("did:plc:valid", &DIDDocument{ID: "did:plc:valid"}, 1*time.Hour)

	if cache.Size() != 3 {
		t.Fatalf("Size() = %d, want 3", cache.Size())
	}

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	// Cleanup expired
	removed := cache.Cleanup()

	if removed != 2 {
		t.Errorf("Cleanup() removed = %d, want 2", removed)
	}
	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
	if !cache.Contains("did:plc:valid") {
		t.Error("Valid entry should still exist")
	}
}

func TestDIDCache_GetWithResolver(t *testing.T) {
	// Create mock PLC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/did:plc:resolve123" {
			doc := DIDDocument{
				ID:          "did:plc:resolve123",
				AlsoKnownAs: []string{"at://resolved.bsky.social"},
			}
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	resolver := NewDIDResolver(WithPLCDirectoryURL(server.URL))
	cache := NewDIDCache(WithResolver(resolver))

	// First get - should resolve
	doc, err := cache.Get("did:plc:resolve123")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if doc.ID != "did:plc:resolve123" {
		t.Errorf("ID = %v, want did:plc:resolve123", doc.ID)
	}

	// Should be cached now
	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1 after resolution", cache.Size())
	}
}

func TestDIDCache_GetWithInvalidate(t *testing.T) {
	// Create mock server that counts requests
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		doc := DIDDocument{ID: "did:plc:test"}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()

	resolver := NewDIDResolver(WithPLCDirectoryURL(server.URL))
	cache := NewDIDCache(WithResolver(resolver))

	// First get
	_, _ = cache.Get("did:plc:test")
	if requestCount != 1 {
		t.Errorf("Request count = %d, want 1", requestCount)
	}

	// Second get - should use cache
	_, _ = cache.Get("did:plc:test")
	if requestCount != 1 {
		t.Errorf("Request count = %d, want 1 (cached)", requestCount)
	}

	// Get with invalidate - should resolve again
	_, _ = cache.GetWithInvalidate("did:plc:test", true)
	if requestCount != 2 {
		t.Errorf("Request count = %d, want 2 (invalidated)", requestCount)
	}
}

func TestDIDCache_ConcurrentAccess(t *testing.T) {
	cache := NewDIDCache()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			did := "did:plc:concurrent"
			cache.Put(did, &DIDDocument{ID: did})
			cache.Contains(did)
			cache.Size()
		}(i)
	}
	wg.Wait()

	// Just verify no race/deadlock
	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
}

func TestCachedDIDDocument_IsExpired(t *testing.T) {
	// Not expired
	cached := &CachedDIDDocument{
		Document:  &DIDDocument{ID: "test"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if cached.IsExpired() {
		t.Error("Should not be expired")
	}

	// Expired
	cached2 := &CachedDIDDocument{
		Document:  &DIDDocument{ID: "test"},
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	if !cached2.IsExpired() {
		t.Error("Should be expired")
	}
}

func TestDIDCache_CustomTTL(t *testing.T) {
	customTTL := 30 * time.Minute
	cache := NewDIDCache(WithCacheTTL(customTTL))

	if cache.ttl != customTTL {
		t.Errorf("TTL = %v, want %v", cache.ttl, customTTL)
	}
}
