package extractors

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/notifications"
)

// ActivityContributorNotifier emits a notification to each contributor of an
// org.hypercerts.claim.activity record. Non-aggregated: each activity produces
// a distinct notification per contributor.
type ActivityContributorNotifier struct{}

// NewActivityContributorNotifier returns a new ActivityContributorNotifier.
func NewActivityContributorNotifier() *ActivityContributorNotifier {
	return &ActivityContributorNotifier{}
}

// Collection returns the NSID the notifier watches.
func (a *ActivityContributorNotifier) Collection() string {
	return "org.hypercerts.claim.activity"
}

type activityRecord struct {
	CreatedAt    string `json:"createdAt"`
	Contributors []struct {
		ContributorIdentity json.RawMessage `json:"contributorIdentity"`
	} `json:"contributors"`
}

// Extract parses the activity record, dedupes the contributor DID list,
// and emits one notification per distinct contributor (excluding the author).
// Returns at most MaxFanOutPerRecord notifications.
func (a *ActivityContributorNotifier) Extract(ctx context.Context, op ingestion.ProcessOp) ([]notifications.Notification, error) {
	// Early-reject grossly oversized records without full unmarshal.
	if countContributorsShallow(op.Record) > notifications.MaxContributorsBeforeReject {
		return nil, nil
	}

	var rec activityRecord
	if err := json.Unmarshal(op.Record, &rec); err != nil {
		return nil, fmt.Errorf("parse activity: %w", err)
	}

	sortAt := clampSortAt(rec.CreatedAt)
	seen := map[string]bool{op.DID: true} // skip self

	var notifs []notifications.Notification
	for _, c := range rec.Contributors {
		did := extractContributorDID(c.ContributorIdentity)
		if !isValidDID(did) || seen[did] {
			continue
		}
		seen[did] = true

		notifs = append(notifs, notifications.Notification{
			Recipient:     did,
			Author:        op.DID,
			RecordURI:     op.URI,
			RecordCID:     op.CID,
			Reason:        notifications.ReasonActivityContributor,
			ReasonSubject: op.URI,
			SortAt:        sortAt,
			GroupKey:      "", // each activity is a distinct notification per contributor
		})
		if len(notifs) >= notifications.MaxFanOutPerRecord {
			break
		}
	}

	return notifs, nil
}
