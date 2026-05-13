package extractors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/notifications"
)

// EndorsementNotifier emits a notification to the endorsement's subject DID
// whenever a new endorsement record is indexed.
type EndorsementNotifier struct{}

// NewEndorsementNotifier returns a new EndorsementNotifier.
func NewEndorsementNotifier() *EndorsementNotifier {
	return &EndorsementNotifier{}
}

// Collection returns the NSID the notifier watches.
func (e *EndorsementNotifier) Collection() string {
	return "app.certified.temp.graph.endorsement"
}

type endorsementRecord struct {
	Subject struct {
		DID string `json:"did"`
		URI string `json:"uri"`
	} `json:"subject"`
	CreatedAt string `json:"createdAt"`
}

// Extract parses the endorsement record and emits a single notification to
// the subject DID unless it would be a self-endorsement or the DID is invalid.
// Endorsements aggregate: all endorsements of the same subject for the same
// recipient collapse into one notification row.
func (e *EndorsementNotifier) Extract(ctx context.Context, op ingestion.ProcessOp) ([]notifications.Notification, error) {
	var rec endorsementRecord
	if err := json.Unmarshal(op.Record, &rec); err != nil {
		return nil, fmt.Errorf("parse endorsement: %w", err)
	}

	subjectDID := strings.TrimSpace(rec.Subject.DID)
	subjectURI := strings.TrimSpace(rec.Subject.URI)

	if !did.IsValid(subjectDID) || subjectDID == op.DID {
		return nil, nil
	}
	if len(subjectURI) > notifications.MaxReasonSubjectBytes {
		return nil, nil
	}

	return []notifications.Notification{{
		Recipient:     subjectDID,
		Author:        op.DID,
		RecordURI:     op.URI,
		RecordCID:     op.CID,
		Reason:        notifications.ReasonEndorsement,
		ReasonSubject: subjectURI,
		SortAt:        clampSortAt(rec.CreatedAt),
		GroupKey:      "endorsement:" + subjectURI,
	}}, nil
}
