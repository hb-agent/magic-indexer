// Package ingestion provides shared record processing logic for all event
// consumers (Jetstream, Labeler, Tap). It centralizes the
// ensure-actor → insert/delete-record → log-activity → publish-to-pubsub
// pipeline so that consumers are thin adapters over their wire protocol.
package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/graphql/subscription"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/metrics"
)

// Operation represents a record change operation.
type Operation string

const (
	OpCreate Operation = "create"
	OpUpdate Operation = "update"
	OpDelete Operation = "delete"
)

// ProcessOp describes a single record operation to process.
type ProcessOp struct {
	DID        string
	URI        string
	Collection string
	RKey       string
	CID        string
	Operation  Operation
	Record     json.RawMessage // nil for delete
}

// RecordProcessor handles the core indexing pipeline shared by all consumers.
type RecordProcessor struct {
	Records  *repositories.RecordsRepository
	Actors   *repositories.ActorsRepository
	Activity *repositories.JetstreamActivityRepository
	PubSub   *subscription.PubSub

	Validator *lexicon.Validator
	ValMode   string // "disabled", "warn", "enforce"

	// AllowedCollections restricts which collections can be indexed.
	// nil means all collections are allowed.
	AllowedCollections map[string]bool
}

// ProcessRecord executes the core indexing pipeline for a single record event.
func (p *RecordProcessor) ProcessRecord(ctx context.Context, op ProcessOp) error {
	// Validate operation type.
	switch op.Operation {
	case OpCreate, OpUpdate, OpDelete:
	default:
		return fmt.Errorf("unknown operation: %q", op.Operation)
	}

	// Enforce collection allowlist.
	if p.AllowedCollections != nil && !p.AllowedCollections[op.Collection] {
		slog.Debug("Skipping record for unlisted collection",
			"collection", op.Collection, "uri", op.URI)
		return nil
	}

	// Reject non-object JSON for create/update.
	if op.Operation != OpDelete && len(op.Record) > 0 {
		trimmed := strings.TrimSpace(string(op.Record))
		if trimmed == "" || trimmed[0] != '{' {
			slog.Warn("Skipping non-object JSON record",
				"uri", op.URI, "json_prefix", fmt.Sprintf("%q", truncate(trimmed, 20)))
			return nil
		}
	}

	processedAt := time.Now()

	// Log activity.
	var activityID int64
	if p.Activity != nil {
		var err error
		activityID, err = p.Activity.LogActivity(
			ctx,
			processedAt,
			string(op.Operation),
			op.Collection,
			op.DID,
			op.RKey,
			string(op.Record),
		)
		if err != nil {
			slog.Warn("Failed to log activity", "error", err)
		}
	}

	updateStatus := func(status string, errMsg *string, isValid *bool) {
		if p.Activity != nil && activityID > 0 {
			if err := p.Activity.UpdateStatus(ctx, activityID, status, errMsg, isValid); err != nil {
				slog.Warn("Failed to update activity status", "error", err)
			}
		}
	}

	// Validate record against lexicon schema.
	var validationMsg *string
	var isValidPtr *bool
	if p.Validator != nil && p.ValMode != "disabled" && (op.Operation == OpCreate || op.Operation == OpUpdate) {
		result := p.Validator.Validate(op.Collection, op.Record)
		if !result.Valid {
			parts := make([]string, 0, len(result.Violations))
			for _, v := range result.Violations {
				if v.Field != "" {
					parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Message))
				} else {
					parts = append(parts, v.Message)
				}
			}
			summary := fmt.Sprintf("%d violation(s): %s", len(result.Violations), strings.Join(parts, "; "))

			slog.Warn("Record failed validation",
				"uri", op.URI,
				"collection", op.Collection,
				"violations", summary,
			)
			metrics.RecordValidationFailed(op.Collection)
			isValid := false
			isValidPtr = &isValid
			validationMsg = &summary

			if p.ValMode == "enforce" {
				updateStatus("rejected", &summary, &isValid)
				return nil
			}
		}
	}

	switch op.Operation {
	case OpCreate, OpUpdate:
		// Ensure actor exists.
		if err := p.Actors.Upsert(ctx, op.DID, ""); err != nil {
			slog.Warn("Failed to ensure actor", "did", op.DID, "error", err)
		}

		// Store record.
		result, err := p.Records.Insert(ctx, op.URI, op.CID, op.DID, op.Collection, string(op.Record))
		if err != nil {
			errMsg := err.Error()
			updateStatus("error", &errMsg, isValidPtr)
			return fmt.Errorf("failed to insert record: %w", err)
		}

		if result == repositories.Inserted {
			metrics.RecordInserted(op.Collection)
		}

		// Publish to GraphQL subscriptions.
		eventType := subscription.EventCreate
		if op.Operation == OpUpdate {
			eventType = subscription.EventUpdate
		}
		p.PubSub.PublishRecord(eventType, op.URI, op.CID, op.DID, op.Collection, op.Record)

		updateStatus("success", validationMsg, isValidPtr)

		slog.Debug("Stored record",
			"uri", op.URI,
			"collection", op.Collection,
			"operation", op.Operation,
		)

	case OpDelete:
		if err := p.Records.Delete(ctx, op.URI); err != nil {
			errMsg := err.Error()
			updateStatus("error", &errMsg, nil)
			return fmt.Errorf("failed to delete record: %w", err)
		}

		// Publish delete to GraphQL subscriptions.
		p.PubSub.PublishRecord(subscription.EventDelete, op.URI, op.CID, op.DID, op.Collection, nil)

		updateStatus("success", nil, nil)

		slog.Debug("Deleted record", "uri", op.URI)
	}

	return nil
}

// truncate returns at most n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
