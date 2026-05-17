package tap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/ingestion"
)

// IndexHandler implements EventHandler by delegating record processing to the
// shared RecordProcessor and handling identity events directly.
type IndexHandler struct {
	processor *ingestion.RecordProcessor
	actors    *repositories.ActorsRepository
}

// NewIndexHandler creates a new IndexHandler.
func NewIndexHandler(processor *ingestion.RecordProcessor, actors *repositories.ActorsRepository) *IndexHandler {
	return &IndexHandler{
		processor: processor,
		actors:    actors,
	}
}

// HandleRecord processes a record event by delegating to RecordProcessor.
// sourceEventID is the Tap envelope's event.id (caller passes it in
// so the handler doesn't have to know about the envelope shape). It
// flows through to LogActivity as the dedup key against redelivered
// events on Tap-ack failure.
func (h *IndexHandler) HandleRecord(ctx context.Context, event *RecordEvent, sourceEventID int64) error {
	slog.Debug("Tap record event",
		"action", event.Action,
		"did", event.DID,
		"collection", event.Collection,
		"rkey", event.RKey,
		"rev", event.Rev,
	)

	return h.processor.ProcessRecord(ctx, ingestion.ProcessOp{
		DID:           event.DID,
		URI:           fmt.Sprintf("at://%s/%s/%s", event.DID, event.Collection, event.RKey),
		Collection:    event.Collection,
		RKey:          event.RKey,
		CID:           event.CID,
		Operation:     ingestion.Operation(event.Action),
		Record:        event.Record,
		SourceEventID: &sourceEventID,
	})
}

// HandleIdentity processes an identity event (handle changes, deactivation).
func (h *IndexHandler) HandleIdentity(ctx context.Context, event *IdentityEvent) error {
	slog.Info("Tap identity event",
		"did", event.DID,
		"handle", event.Handle,
		"is_active", event.IsActive,
		"status", event.Status,
	)

	// TODO: When ActorsRepository.Deactivate is implemented, set
	// is_active=false for deactivated/deleted/takendown actors.
	// Migration 014 adds the column; the logic needs a new repo method.
	return h.actors.Upsert(ctx, event.DID, event.Handle)
}
