package ingestion

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/oauth"
)

func TestProcessOp_OperationValidation(t *testing.T) {
	p := &RecordProcessor{} // will panic on nil repos if we reach them

	err := p.ProcessRecord(context.Background(), ProcessOp{
		Operation: "invalid",
		URI:       "at://did:plc:test/test/1",
	})
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestProcessOp_CollectionAllowlist(t *testing.T) {
	p := &RecordProcessor{
		AllowedCollections: map[string]bool{
			"app.bsky.feed.post": true,
		},
	}

	// Disallowed collection should be silently skipped.
	err := p.ProcessRecord(context.Background(), ProcessOp{
		Operation:  OpCreate,
		Collection: "com.evil.collection",
		URI:        "at://did:plc:test/com.evil.collection/1",
		Record:     json.RawMessage(`{"text": "hello"}`),
	})
	if err != nil {
		t.Fatalf("expected nil error for disallowed collection, got %v", err)
	}
}

func TestProcessOp_NilAllowlistAllowsAll(t *testing.T) {
	// With nil AllowedCollections, the allowlist check is skipped.
	// We verify this by using an AllowedCollections=nil processor with a
	// collection that would be blocked if the allowlist were non-nil.
	p := &RecordProcessor{
		AllowedCollections: map[string]bool{"only.this": true},
	}

	// Should be blocked by allowlist.
	err := p.ProcessRecord(context.Background(), ProcessOp{
		Operation:  OpCreate,
		Collection: "anything.goes",
		URI:        "at://did:plc:test/anything.goes/1",
		Record:     json.RawMessage(`{"text": "hello"}`),
	})
	if err != nil {
		t.Fatalf("expected nil (skipped), got %v", err)
	}

	// Now with nil allowlist — same collection should pass the allowlist check.
	// It will fail later at Actors.Upsert (nil), but we use recover to verify
	// the allowlist check itself passed.
	p2 := &RecordProcessor{
		AllowedCollections: nil,
	}
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic from nil repos, meaning allowlist check passed")
			}
			// Got the expected panic — allowlist check was skipped.
		}()
		_ = p2.ProcessRecord(context.Background(), ProcessOp{
			Operation:  OpCreate,
			Collection: "anything.goes",
			URI:        "at://did:plc:test/anything.goes/1",
			Record:     json.RawMessage(`{"text": "hello"}`),
		})
	}()
}

func TestProcessOp_RejectsNonObjectJSON(t *testing.T) {
	p := &RecordProcessor{
		AllowedCollections: nil,
	}

	tests := []struct {
		name   string
		record string
	}{
		{"array", `[1,2,3]`},
		{"string", `"hello"`},
		{"number", `42`},
		{"boolean", `true`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.ProcessRecord(context.Background(), ProcessOp{
				Operation:  OpCreate,
				Collection: "test.collection",
				URI:        "at://did:plc:test/test.collection/1",
				Record:     json.RawMessage(tt.record),
			})
			if err != nil {
				t.Fatalf("expected nil (skip), got error: %v", err)
			}
		})
	}
}

func TestResolvePDS_NilCacheReturnsEmpty(t *testing.T) {
	// When DIDCache is unset, the processor must fall through with
	// empty pds rather than panicking — covers test setups and
	// deliberate disabling.
	p := &RecordProcessor{}
	if got := p.resolvePDS("did:plc:any"); got != "" {
		t.Errorf("expected empty pds with nil cache, got %q", got)
	}
}

func TestResolvePDS_CacheHitReturnsEndpoint(t *testing.T) {
	// Pre-populate the cache via Put, then resolve. No HTTP call —
	// the test exercises the cache-hit path that dominates steady
	// state ingestion.
	cache := oauth.NewDIDCache(oauth.WithCacheTTL(time.Hour))
	cache.Put("did:plc:happy", &oauth.DIDDocument{
		ID: "did:plc:happy",
		Service: []oauth.Service{
			{Type: "AtprotoPersonalDataServer", ServiceEndpoint: "https://prod.pds.example.com"},
		},
	})

	p := &RecordProcessor{DIDCache: cache}
	got := p.resolvePDS("did:plc:happy")
	if got != "https://prod.pds.example.com" {
		t.Errorf("expected resolved endpoint, got %q", got)
	}
}

func TestResolvePDS_CacheHitWithoutPDSEndpoint(t *testing.T) {
	// A DID document that lacks AtprotoPersonalDataServer is a real
	// (rare) case: e.g. a labeler-only DID with only an
	// AtprotoLabeler service entry. The processor should treat this
	// as "no pds" rather than crashing.
	cache := oauth.NewDIDCache(oauth.WithCacheTTL(time.Hour))
	cache.Put("did:plc:labeler", &oauth.DIDDocument{
		ID: "did:plc:labeler",
		Service: []oauth.Service{
			{Type: "AtprotoLabeler", ServiceEndpoint: "https://labeler.example.com"},
		},
	})

	p := &RecordProcessor{DIDCache: cache}
	got := p.resolvePDS("did:plc:labeler")
	if got != "" {
		t.Errorf("expected empty pds for labeler-only DID, got %q", got)
	}
}

func TestProcessOp_DeleteSkipsJSONValidation(t *testing.T) {
	// Delete with nil record should not fail the non-object check.
	// It will panic at Records.Delete (nil), confirming the JSON check was skipped.
	p := &RecordProcessor{}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic from nil repos, meaning JSON check was skipped for delete")
			}
		}()
		_ = p.ProcessRecord(context.Background(), ProcessOp{
			Operation:  OpDelete,
			Collection: "test.collection",
			URI:        "at://did:plc:test/test.collection/1",
			Record:     nil,
		})
	}()
}
