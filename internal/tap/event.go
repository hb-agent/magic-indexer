// Package tap provides a consumer for the Bluesky Tap sidecar, which delivers
// cryptographically verified AT Protocol events with per-repo ordering
// guarantees and ack-based delivery.
package tap

import "encoding/json"

// EventType is the type of a Tap event.
type EventType string

const (
	EventTypeRecord   EventType = "record"
	EventTypeIdentity EventType = "identity"
)

// Event is the envelope for a Tap event.
type Event struct {
	ID       int64          `json:"id"`
	Type     EventType      `json:"type"`
	Record   *RecordEvent   `json:"record,omitempty"`
	Identity *IdentityEvent `json:"identity,omitempty"`
}

// RecordEvent describes a record change delivered by Tap.
type RecordEvent struct {
	Live       bool            `json:"live"`
	Rev        string          `json:"rev"` // commit revision (logged for audit)
	DID        string          `json:"did"`
	Collection string          `json:"collection"`
	RKey       string          `json:"rkey"`
	Action     string          `json:"action"` // "create", "update", "delete"
	CID        string          `json:"cid,omitempty"`
	Record     json.RawMessage `json:"record,omitempty"`
}

// IdentityEvent describes an identity change delivered by Tap.
type IdentityEvent struct {
	DID      string `json:"did"`
	Handle   string `json:"handle"`
	IsActive bool   `json:"is_active"`
	Status   string `json:"status"` // "active", "takendown", "suspended", "deactivated", "deleted"
}

// ParseEvent parses a Tap event from JSON.
func ParseEvent(data []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}
