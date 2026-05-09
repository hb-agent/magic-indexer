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
	"github.com/GainForest/hypergoat/internal/oauth"
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

// HookErrorPolicy controls what happens when a RecordHook returns an error.
type HookErrorPolicy int

const (
	// HookLogContinue logs the hook error and continues processing. The record
	// is already persisted; the hook's work is best-effort.
	HookLogContinue HookErrorPolicy = iota
	// HookAbortTx returns the hook error from ProcessRecord to the caller.
	// The record has already been committed (hooks run after insert), but the
	// consumer sees the error and may choose to retry or halt.
	HookAbortTx
)

// RecordHook is called after a record insert/update/delete. Hooks run
// sequentially; ordering is defined by the slice position in RecordProcessor.RecordHooks.
type RecordHook struct {
	Name   string
	Policy HookErrorPolicy
	Fn     func(ctx context.Context, op ProcessOp) error
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

	// RecordHooks run after each record insert/update/delete. See RecordHook.
	RecordHooks []RecordHook

	// DIDCache, when non-nil, is consulted on each actor upsert to
	// resolve the author's PDS service endpoint. The resolved PDS is
	// persisted on the actor row so GraphQL queries can join on it
	// (record.pds is a join attribute, not a per-record column).
	//
	// Resolution is best-effort: cache miss + transient failure logs
	// a warning and persists the actor with no pds. The actor's pds
	// is updated on the next ingestion event for that DID once the
	// upstream is healthy again, or via the standalone backfill CLI.
	// Singleflight inside DIDCache collapses concurrent resolves for
	// the same DID, so a burst of records from a popular new DID
	// makes one upstream call.
	DIDCache *oauth.DIDCache
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
		// Ensure actor exists. Resolve PDS via the DID cache when one is
		// configured; on miss/error we fall through with an empty pds and
		// UpsertWithPDS preserves any previously-resolved value via
		// COALESCE(EXCLUDED.pds, actor.pds).
		pds := p.resolvePDS(op.DID)
		if err := p.Actors.UpsertWithPDS(ctx, op.DID, "", pds); err != nil {
			slog.Warn("Failed to ensure actor", "did", op.DID, "error", err)
		}

		// Compute sort_at (issue #26) from the record's self-reported
		// createdAt, clamped against clock skew. A nil/zero result from
		// ExtractCreatedAt falls back to processedAt inside ComputeSortAt.
		sortAt := ComputeSortAt(ExtractCreatedAt(op.Record), processedAt)

		// Store record.
		result, err := p.Records.InsertWithParams(ctx, repositories.InsertParams{
			URI:        op.URI,
			CID:        op.CID,
			DID:        op.DID,
			Collection: op.Collection,
			JSONData:   string(op.Record),
			SortAt:     &sortAt,
		})
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

	// Run post-processing hooks (notifications, etc.). Hooks run sequentially;
	// each hook is independently isolated with panic recovery.
	for _, hook := range p.RecordHooks {
		if err := p.runHook(ctx, hook, op); err != nil {
			if hook.Policy == HookAbortTx {
				return fmt.Errorf("hook %q: %w", hook.Name, err)
			}
			slog.Warn("record hook failed",
				"hook", hook.Name,
				"collection", op.Collection,
				"uri", op.URI,
				"error", err)
		}
	}

	return nil
}

// resolvePDS returns the author's PDS service endpoint via the DID cache,
// or "" on cache miss / resolution failure. Logging is at warn level so
// transient PLC outages surface in operator dashboards without spamming
// at error severity. When the cache is nil (test setups, deliberate
// disabling) the function is a no-op.
func (p *RecordProcessor) resolvePDS(did string) string {
	if p.DIDCache == nil {
		return ""
	}
	doc, err := p.DIDCache.Get(did)
	if err != nil {
		slog.Warn("PDS resolution failed; actor will have empty pds",
			"did", did, "error", err)
		metrics.PDSResolveFailed()
		return ""
	}
	pds := doc.GetPDSEndpoint()
	if pds == "" {
		// DID document had no AtprotoPersonalDataServer service entry —
		// rare but valid. Surface as a debug log so it's visible without
		// noise, and tag a metric so the rate is observable.
		slog.Debug("DID document had no PDS endpoint", "did", did)
		metrics.PDSResolveNoEndpoint()
		return ""
	}
	metrics.PDSResolveOK()
	return pds
}

// runHook invokes a RecordHook with panic recovery.
func (p *RecordProcessor) runHook(ctx context.Context, hook RecordHook, op ProcessOp) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("hook panic: %v", r)
		}
	}()
	return hook.Fn(ctx, op)
}

// truncate returns at most n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
