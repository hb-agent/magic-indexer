package extractors

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/notifications"
)

func TestEndorsementNotifier_Collection(t *testing.T) {
	n := NewEndorsementNotifier()
	if got := n.Collection(); got != "app.certified.temp.graph.endorsement" {
		t.Errorf("unexpected collection: %s", got)
	}
}

func TestEndorsementNotifier_ValidRecord(t *testing.T) {
	n := NewEndorsementNotifier()
	rec := json.RawMessage(`{
		"subject": {"did": "did:plc:alice", "uri": "at://did:plc:alice/app.certified.temp.graph.endorsement/1"},
		"createdAt": "2026-01-01T12:00:00Z"
	}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:        "did:plc:bob",
		URI:        "at://did:plc:bob/app.certified.temp.graph.endorsement/1",
		CID:        "bafyreia",
		Collection: "app.certified.temp.graph.endorsement",
		Operation:  ingestion.OpCreate,
		Record:     rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}
	got := notifs[0]
	if got.Recipient != "did:plc:alice" {
		t.Errorf("unexpected recipient: %s", got.Recipient)
	}
	if got.Author != "did:plc:bob" {
		t.Errorf("unexpected author: %s", got.Author)
	}
	if got.Reason != notifications.ReasonEndorsement {
		t.Errorf("unexpected reason: %s", got.Reason)
	}
	if got.ReasonSubject != "at://did:plc:alice/app.certified.temp.graph.endorsement/1" {
		t.Errorf("unexpected reasonSubject: %s", got.ReasonSubject)
	}
	if got.GroupKey == "" {
		t.Error("expected non-empty group key for aggregation")
	}
}

func TestEndorsementNotifier_SelfEndorsement(t *testing.T) {
	n := NewEndorsementNotifier()
	rec := json.RawMessage(`{"subject": {"did": "did:plc:bob", "uri": "x"}, "createdAt": "2026-01-01T12:00:00Z"}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:bob",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 0 {
		t.Errorf("expected 0 notifications for self-endorsement, got %d", len(notifs))
	}
}

func TestEndorsementNotifier_InvalidDID(t *testing.T) {
	tests := []struct {
		name string
		did  string
	}{
		{"empty", ""},
		{"no prefix", "alice"},
		{"short", "did:a"},
		{"illegal char", "did:plc:alice\x00bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewEndorsementNotifier()
			rec := map[string]interface{}{
				"subject":   map[string]interface{}{"did": tt.did, "uri": "x"},
				"createdAt": "2026-01-01T12:00:00Z",
			}
			raw, _ := json.Marshal(rec)
			notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
				DID:       "did:plc:bob",
				Operation: ingestion.OpCreate,
				Record:    raw,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(notifs) != 0 {
				t.Errorf("expected 0 notifications for invalid DID %q", tt.did)
			}
		})
	}
}

func TestEndorsementNotifier_MalformedJSON(t *testing.T) {
	n := NewEndorsementNotifier()
	_, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:bob",
		Operation: ingestion.OpCreate,
		Record:    []byte("{not valid json"),
	})
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestClampSortAt(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	past := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	ok := now.Add(-time.Hour).Format(time.RFC3339Nano)

	if !clampSortAt(future).Before(now.Add(time.Second)) {
		t.Error("future date should clamp to now")
	}
	if !clampSortAt(past).After(now.Add(-time.Second)) {
		t.Error("far-past date should clamp to now")
	}
	if clampSortAt(ok).After(now) {
		t.Error("recent past should not clamp")
	}
	if clampSortAt("bogus").IsZero() {
		t.Error("bogus input should return a valid time")
	}
}

func TestIsValidDID(t *testing.T) {
	valid := []string{
		"did:plc:alice123",
		"did:web:example.com",
		"did:plc:z7mstadkybqmyvmsspztwnoy",
	}
	invalid := []string{
		"",
		"plc:alice",
		"did:",
		"did:plc:alice bob",
		"did:plc:alice\nbob",
	}
	for _, v := range valid {
		if !isValidDID(v) {
			t.Errorf("expected valid: %q", v)
		}
	}
	for _, v := range invalid {
		if isValidDID(v) {
			t.Errorf("expected invalid: %q", v)
		}
	}
}
