package extractors

import (
	"encoding/json"
	"testing"

	"github.com/GainForest/hypergoat/internal/metrics"
)

// counterValue reads the current value of a single labelled counter
// out of the metrics package's registry. Used to assert that the
// extractor increments the right outcome bucket on each call shape.
func counterValue(t *testing.T, name, label string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("registry gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == label {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// counterDelta wraps a callback in a before/after read of a labelled
// counter, returning the increment that fired during the callback.
func counterDelta(t *testing.T, name, label string, fn func()) float64 {
	t.Helper()
	before := counterValue(t, name, label)
	fn()
	after := counterValue(t, name, label)
	return after - before
}

func TestExtractContributorDID_BareStringDID(t *testing.T) {
	raw := json.RawMessage(`"did:plc:alice"`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "did", func() {
		got = extractContributorDID(raw)
	})
	if got != "did:plc:alice" {
		t.Errorf("got %q, want %q", got, "did:plc:alice")
	}
	if delta != 1 {
		t.Errorf("did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_ObjectVariantDID(t *testing.T) {
	raw := json.RawMessage(`{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"did:plc:bob"}`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "did", func() {
		got = extractContributorDID(raw)
	})
	if got != "did:plc:bob" {
		t.Errorf("got %q, want %q", got, "did:plc:bob")
	}
	if delta != 1 {
		t.Errorf("did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_BareStringHandle(t *testing.T) {
	raw := json.RawMessage(`"alice.example.com"`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "non_did", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("non_did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_ObjectVariantHandle(t *testing.T) {
	raw := json.RawMessage(`{"$type":"org.hypercerts.claim.activity#contributorIdentity","identity":"alice.example.com"}`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "non_did", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("non_did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_EmptyBareString(t *testing.T) {
	// Empty bare string is non_did per the plan: it is a string, just
	// not a DID. unrecognized_shape is reserved for non-string shapes.
	raw := json.RawMessage(`""`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "non_did", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("non_did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_ObjectMissingIdentity(t *testing.T) {
	// Approximates the strong-ref variant of the union — an object
	// shape without an .identity field. Should signal
	// unrecognized_shape so operators see the trend if certified.app
	// starts shipping strong-refs.
	raw := json.RawMessage(`{"$type":"com.atproto.repo.strongRef","uri":"at://...","cid":"bafy"}`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "unrecognized_shape", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("unrecognized_shape outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_ObjectNonStringIdentity(t *testing.T) {
	// .identity is a number — json.Unmarshal into struct fails, falls
	// through to the unrecognized_shape branch.
	raw := json.RawMessage(`{"identity":42}`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "unrecognized_shape", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("unrecognized_shape outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_NullLiteral(t *testing.T) {
	// JSON null literal: json.Unmarshal into *string succeeds, leaving
	// the target unchanged at its zero value ("" — also valid empty
	// bare-string semantics). Classification: non_did (empty string is
	// a string, just not a DID).
	raw := json.RawMessage(`null`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "non_did", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("non_did outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_MalformedJSON(t *testing.T) {
	raw := json.RawMessage(`{not json`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "unrecognized_shape", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
	if delta != 1 {
		t.Errorf("unrecognized_shape outcome delta = %v, want 1", delta)
	}
}

func TestExtractContributorDID_WhitespaceWrappedDID(t *testing.T) {
	// Whitespace-padded stored DIDs are non_did. The extractor no
	// longer trims, keeping its decision symmetric with the SQL
	// filter (which also matches bytes exactly). Stored DIDs with
	// stray whitespace are a data-quality issue surfaced by the
	// non_did metric.
	raw := json.RawMessage(`"  did:plc:alice  "`)
	var got string
	delta := counterDelta(t, "hypergoat_contributor_identity_total", "non_did", func() {
		got = extractContributorDID(raw)
	})
	if got != "" {
		t.Errorf("got %q, want \"\" (whitespace-bearing input is non_did)", got)
	}
	if delta != 1 {
		t.Errorf("non_did outcome delta = %v, want 1", delta)
	}
}

// Defence against future regression: the COALESCE-equivalent in Go
// must consult the bare-string branch first, so a record that
// happens to encode both shapes does not silently flip which is
// read. This is a sanity check on the function structure, not a
// reachable input shape today.
func TestExtractContributorDID_PrefersBareStringWhenBothShapeParseable(t *testing.T) {
	// JSON cannot actually be both a string and an object at once;
	// this test instead verifies the function's contract by asserting
	// the bare-string-DID call path returns the bare value, not "".
	raw := json.RawMessage(`"did:plc:wins"`)
	if got := extractContributorDID(raw); got != "did:plc:wins" {
		t.Errorf("got %q, want %q", got, "did:plc:wins")
	}
}
