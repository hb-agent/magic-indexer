// Package notifications implements a Bluesky-style notification system
// for the indexer: per-collection extractors emit notification rows
// inside the RecordProcessor post-insert hook.
package notifications

import (
	"context"
	"time"

	"github.com/GainForest/hypergoat/internal/ingestion"
)

// Canonical reason codes (Bluesky-style: bare strings, lowercase, hyphenated).
const (
	ReasonEndorsement         = "endorsement"
	ReasonActivityContributor = "activity-contributor"
)

// Limits.
const (
	MaxFanOutPerRecord             = 100
	UnreadCountCap                 = 50
	SortAtMaxPast                  = 7 * 24 * time.Hour
	MaxRecordBytesForNotifications = 1 << 20 // 1 MiB
	MaxReasonSubjectBytes          = 512
	MaxContributorsBeforeReject    = 2 * MaxFanOutPerRecord
)

// Notification describes a single notification to be emitted.
// GroupKey controls aggregation: "" means one row per event; non-empty means
// collapse all events with the same (recipient, group_key) into one row.
type Notification struct {
	Recipient     string
	Author        string
	RecordURI     string
	RecordCID     string
	Reason        string
	ReasonSubject string
	SortAt        time.Time
	GroupKey      string
}

// Notifier extracts notifications from a record.
type Notifier interface {
	Collection() string
	Extract(ctx context.Context, op ingestion.ProcessOp) ([]Notification, error)
}
