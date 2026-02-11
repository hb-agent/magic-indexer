package jetstream

import (
	"encoding/json"
	"testing"
)

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantErr    bool
		wantDID    string
		wantKind   EventType
		wantTimeUS int64
		wantCommit bool
	}{
		{
			name: "valid commit create event",
			input: `{
				"did": "did:plc:test123",
				"time_us": 1706000000000000,
				"kind": "commit",
				"commit": {
					"rev": "abc",
					"operation": "create",
					"collection": "app.bsky.feed.post",
					"rkey": "3k2la7fx2as2s",
					"record": {"$type": "app.bsky.feed.post", "text": "hello"},
					"cid": "bafyreib"
				}
			}`,
			wantDID:    "did:plc:test123",
			wantKind:   EventTypeCommit,
			wantTimeUS: 1706000000000000,
			wantCommit: true,
		},
		{
			name: "valid commit delete event",
			input: `{
				"did": "did:plc:user456",
				"time_us": 1706000001000000,
				"kind": "commit",
				"commit": {
					"rev": "def",
					"operation": "delete",
					"collection": "app.bsky.feed.post",
					"rkey": "3k2la7fx2as2s"
				}
			}`,
			wantDID:    "did:plc:user456",
			wantKind:   EventTypeCommit,
			wantTimeUS: 1706000001000000,
			wantCommit: true,
		},
		{
			name: "valid identity event",
			input: `{
				"did": "did:plc:handle789",
				"time_us": 1706000002000000,
				"kind": "identity"
			}`,
			wantDID:    "did:plc:handle789",
			wantKind:   EventTypeIdentity,
			wantTimeUS: 1706000002000000,
			wantCommit: false,
		},
		{
			name:    "invalid JSON",
			input:   `{not valid json`,
			wantErr: true,
		},
		{
			name:       "empty JSON object",
			input:      `{}`,
			wantDID:    "",
			wantKind:   "",
			wantTimeUS: 0,
			wantCommit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseEvent([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if event.DID != tt.wantDID {
				t.Errorf("DID = %q, want %q", event.DID, tt.wantDID)
			}
			if event.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", event.Kind, tt.wantKind)
			}
			if event.TimeUS != tt.wantTimeUS {
				t.Errorf("TimeUS = %d, want %d", event.TimeUS, tt.wantTimeUS)
			}
			if (event.Commit != nil) != tt.wantCommit {
				t.Errorf("Commit present = %v, want %v", event.Commit != nil, tt.wantCommit)
			}
			if event.Raw == nil {
				t.Error("Raw should be set after parsing")
			}
		})
	}
}

func TestParseEvent_CommitFields(t *testing.T) {
	input := `{
		"did": "did:plc:test123",
		"time_us": 1706000000000000,
		"kind": "commit",
		"commit": {
			"rev": "abc123rev",
			"operation": "create",
			"collection": "app.bsky.feed.post",
			"rkey": "3k2la7fx2as2s",
			"record": {"$type": "app.bsky.feed.post", "text": "hello world", "createdAt": "2024-01-23T12:00:00Z"},
			"cid": "bafyreiblahblah"
		}
	}`

	event, err := ParseEvent([]byte(input))
	if err != nil {
		t.Fatalf("ParseEvent() unexpected error: %v", err)
	}

	c := event.Commit
	if c.Rev != "abc123rev" {
		t.Errorf("Rev = %q, want %q", c.Rev, "abc123rev")
	}
	if c.Operation != OpCreate {
		t.Errorf("Operation = %q, want %q", c.Operation, OpCreate)
	}
	if c.Collection != "app.bsky.feed.post" {
		t.Errorf("Collection = %q, want %q", c.Collection, "app.bsky.feed.post")
	}
	if c.RKey != "3k2la7fx2as2s" {
		t.Errorf("RKey = %q, want %q", c.RKey, "3k2la7fx2as2s")
	}
	if c.CID != "bafyreiblahblah" {
		t.Errorf("CID = %q, want %q", c.CID, "bafyreiblahblah")
	}
	if c.Record == nil {
		t.Fatal("Record should not be nil for create event")
	}

	// Verify Record is valid JSON containing the expected data.
	var rec map[string]interface{}
	if err := json.Unmarshal(c.Record, &rec); err != nil {
		t.Fatalf("Record is not valid JSON: %v", err)
	}
	if rec["text"] != "hello world" {
		t.Errorf("Record text = %v, want %q", rec["text"], "hello world")
	}
}

func TestParseEvent_DeleteHasNoRecordOrCID(t *testing.T) {
	input := `{
		"did": "did:plc:user456",
		"time_us": 1706000001000000,
		"kind": "commit",
		"commit": {
			"rev": "xyz",
			"operation": "delete",
			"collection": "app.bsky.feed.like",
			"rkey": "3k9zzz"
		}
	}`

	event, err := ParseEvent([]byte(input))
	if err != nil {
		t.Fatalf("ParseEvent() unexpected error: %v", err)
	}

	c := event.Commit
	if c.Record != nil {
		t.Errorf("Record should be nil for delete, got %s", string(c.Record))
	}
	if c.CID != "" {
		t.Errorf("CID should be empty for delete, got %q", c.CID)
	}
}

func TestEvent_IsCommit(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{
			name: "commit event with commit data",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpCreate},
			},
			want: true,
		},
		{
			name: "commit kind but nil commit",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: nil,
			},
			want: false,
		},
		{
			name: "identity event",
			event: Event{
				Kind: EventTypeIdentity,
			},
			want: false,
		},
		{
			name: "account event",
			event: Event{
				Kind: EventTypeAccount,
			},
			want: false,
		},
		{
			name:  "zero-value event",
			event: Event{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsCommit(); got != tt.want {
				t.Errorf("IsCommit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvent_IsCreate(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{
			name: "create operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpCreate},
			},
			want: true,
		},
		{
			name: "update operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpUpdate},
			},
			want: false,
		},
		{
			name: "delete operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpDelete},
			},
			want: false,
		},
		{
			name: "not a commit event",
			event: Event{
				Kind: EventTypeIdentity,
			},
			want: false,
		},
		{
			name: "commit kind but nil commit",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: nil,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsCreate(); got != tt.want {
				t.Errorf("IsCreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvent_IsUpdate(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{
			name: "update operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpUpdate},
			},
			want: true,
		},
		{
			name: "create operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpCreate},
			},
			want: false,
		},
		{
			name: "delete operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpDelete},
			},
			want: false,
		},
		{
			name: "not a commit event",
			event: Event{
				Kind: EventTypeIdentity,
			},
			want: false,
		},
		{
			name: "commit kind but nil commit",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: nil,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsUpdate(); got != tt.want {
				t.Errorf("IsUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvent_IsDelete(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{
			name: "delete operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpDelete},
			},
			want: true,
		},
		{
			name: "create operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpCreate},
			},
			want: false,
		},
		{
			name: "update operation",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: &CommitEvent{Operation: OpUpdate},
			},
			want: false,
		},
		{
			name: "not a commit event",
			event: Event{
				Kind: EventTypeAccount,
			},
			want: false,
		},
		{
			name: "commit kind but nil commit",
			event: Event{
				Kind:   EventTypeCommit,
				Commit: nil,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsDelete(); got != tt.want {
				t.Errorf("IsDelete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommitEvent_URI(t *testing.T) {
	tests := []struct {
		name       string
		did        string
		collection string
		rkey       string
		want       string
	}{
		{
			name:       "bsky post",
			did:        "did:plc:test123",
			collection: "app.bsky.feed.post",
			rkey:       "3k2la7fx2as2s",
			want:       "at://did:plc:test123/app.bsky.feed.post/3k2la7fx2as2s",
		},
		{
			name:       "bsky like",
			did:        "did:plc:user456",
			collection: "app.bsky.feed.like",
			rkey:       "3k9abc",
			want:       "at://did:plc:user456/app.bsky.feed.like/3k9abc",
		},
		{
			name:       "bsky follow",
			did:        "did:web:example.com",
			collection: "app.bsky.graph.follow",
			rkey:       "3kfollow1",
			want:       "at://did:web:example.com/app.bsky.graph.follow/3kfollow1",
		},
		{
			name:       "custom collection",
			did:        "did:plc:abc",
			collection: "com.example.custom.record",
			rkey:       "self",
			want:       "at://did:plc:abc/com.example.custom.record/self",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &CommitEvent{
				Collection: tt.collection,
				RKey:       tt.rkey,
			}
			got := c.URI(tt.did)
			if got != tt.want {
				t.Errorf("URI() = %q, want %q", got, tt.want)
			}
		})
	}
}
