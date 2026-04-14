package extractors

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/notifications"
)

func TestActivityContributorNotifier_Collection(t *testing.T) {
	n := NewActivityContributorNotifier()
	if got := n.Collection(); got != "org.hypercerts.claim.activity" {
		t.Errorf("unexpected collection: %s", got)
	}
}

func TestActivityContributorNotifier_ValidRecord(t *testing.T) {
	n := NewActivityContributorNotifier()
	rec := json.RawMessage(`{
		"createdAt": "2026-01-01T12:00:00Z",
		"contributors": [
			{"contributorIdentity": "did:plc:alice"},
			{"contributorIdentity": "did:plc:bob"},
			{"contributorIdentity": "did:plc:carol"}
		]
	}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:        "did:plc:author",
		URI:        "at://did:plc:author/org.hypercerts.claim.activity/1",
		CID:        "bafy",
		Collection: "org.hypercerts.claim.activity",
		Operation:  ingestion.OpCreate,
		Record:     rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(notifs))
	}
	for _, nf := range notifs {
		if nf.Reason != notifications.ReasonActivityContributor {
			t.Errorf("unexpected reason: %s", nf.Reason)
		}
		if nf.GroupKey != "" {
			t.Errorf("expected non-aggregated (empty GroupKey), got %q", nf.GroupKey)
		}
	}
}

func TestActivityContributorNotifier_SkipsAuthor(t *testing.T) {
	n := NewActivityContributorNotifier()
	rec := json.RawMessage(`{
		"createdAt": "2026-01-01T12:00:00Z",
		"contributors": [
			{"contributorIdentity": "did:plc:author"},
			{"contributorIdentity": "did:plc:alice"}
		]
	}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification (author skipped), got %d", len(notifs))
	}
	if notifs[0].Recipient != "did:plc:alice" {
		t.Errorf("unexpected recipient: %s", notifs[0].Recipient)
	}
}

func TestActivityContributorNotifier_Dedup(t *testing.T) {
	n := NewActivityContributorNotifier()
	rec := json.RawMessage(`{
		"createdAt": "2026-01-01T12:00:00Z",
		"contributors": [
			{"contributorIdentity": "did:plc:alice"},
			{"contributorIdentity": "did:plc:alice"},
			{"contributorIdentity": "did:plc:alice"}
		]
	}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 1 {
		t.Errorf("expected 1 deduped notification, got %d", len(notifs))
	}
}

func TestActivityContributorNotifier_StrongRefSkipped(t *testing.T) {
	n := NewActivityContributorNotifier()
	rec := json.RawMessage(`{
		"createdAt": "2026-01-01T12:00:00Z",
		"contributors": [
			{"contributorIdentity": {"uri": "at://some/ref", "cid": "bafy"}},
			{"contributorIdentity": "did:plc:alice"}
		]
	}`)
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 1 {
		t.Errorf("expected 1 notification (strongRef skipped), got %d", len(notifs))
	}
}

func TestActivityContributorNotifier_FanOutCap(t *testing.T) {
	n := NewActivityContributorNotifier()
	var contribs []map[string]interface{}
	for i := 0; i < notifications.MaxFanOutPerRecord+50; i++ {
		contribs = append(contribs, map[string]interface{}{
			"contributorIdentity": fmt.Sprintf("did:plc:contributor%d", i),
		})
	}
	rec, _ := json.Marshal(map[string]interface{}{
		"createdAt":    "2026-01-01T12:00:00Z",
		"contributors": contribs,
	})
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != notifications.MaxFanOutPerRecord {
		t.Errorf("expected %d notifications (capped), got %d", notifications.MaxFanOutPerRecord, len(notifs))
	}
}

func TestActivityContributorNotifier_PreRejectOversized(t *testing.T) {
	n := NewActivityContributorNotifier()
	var contribs []map[string]interface{}
	for i := 0; i < notifications.MaxContributorsBeforeReject+10; i++ {
		contribs = append(contribs, map[string]interface{}{
			"contributorIdentity": fmt.Sprintf("did:plc:contributor%d", i),
		})
	}
	rec, _ := json.Marshal(map[string]interface{}{
		"createdAt":    "2026-01-01T12:00:00Z",
		"contributors": contribs,
	})
	notifs, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    rec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifs) != 0 {
		t.Errorf("expected 0 notifications (oversized record rejected), got %d", len(notifs))
	}
}

func TestActivityContributorNotifier_MalformedJSON(t *testing.T) {
	n := NewActivityContributorNotifier()
	_, err := n.Extract(context.Background(), ingestion.ProcessOp{
		DID:       "did:plc:author",
		Operation: ingestion.OpCreate,
		Record:    []byte("{not valid"),
	})
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}
