package tap

import (
	"testing"
)

func TestParseEvent_RecordCreate(t *testing.T) {
	data := []byte(`{
		"id": 42,
		"type": "record",
		"record": {
			"live": true,
			"rev": "abc123",
			"did": "did:plc:test",
			"collection": "app.bsky.feed.post",
			"rkey": "3k2la7fx2as2q",
			"action": "create",
			"cid": "bafyreia",
			"record": {"text": "hello world"}
		}
	}`)

	event, err := ParseEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != 42 {
		t.Errorf("expected ID 42, got %d", event.ID)
	}
	if event.Type != EventTypeRecord {
		t.Errorf("expected type record, got %s", event.Type)
	}
	if event.Record == nil {
		t.Fatal("expected record event, got nil")
	}
	if event.Record.DID != "did:plc:test" {
		t.Errorf("expected did:plc:test, got %s", event.Record.DID)
	}
	if event.Record.Action != "create" {
		t.Errorf("expected create, got %s", event.Record.Action)
	}
	if !event.Record.Live {
		t.Error("expected live=true")
	}
}

func TestParseEvent_Identity(t *testing.T) {
	data := []byte(`{
		"id": 99,
		"type": "identity",
		"identity": {
			"did": "did:plc:test",
			"handle": "user.bsky.social",
			"is_active": true,
			"status": "active"
		}
	}`)

	event, err := ParseEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != EventTypeIdentity {
		t.Errorf("expected identity, got %s", event.Type)
	}
	if event.Identity == nil {
		t.Fatal("expected identity event, got nil")
	}
	if event.Identity.Handle != "user.bsky.social" {
		t.Errorf("expected user.bsky.social, got %s", event.Identity.Handle)
	}
}

func TestParseEvent_UnknownType(t *testing.T) {
	data := []byte(`{"id": 1, "type": "unknown"}`)
	event, err := ParseEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "unknown" {
		t.Errorf("expected unknown, got %s", event.Type)
	}
}

func TestParseEvent_Malformed(t *testing.T) {
	_, err := ParseEvent([]byte(`{invalid json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
