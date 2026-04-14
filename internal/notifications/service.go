package notifications

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GainForest/hypergoat/internal/ingestion"
)

// Service wires together the notifier registry and repository.
// It exposes a RecordHook for attachment to the shared RecordProcessor.
type Service struct {
	registry *Registry
	repo     *Repository
}

// NewService creates a new Service.
func NewService(repo *Repository) *Service {
	return &Service{registry: NewRegistry(), repo: repo}
}

// Register adds a notifier to the service.
func (s *Service) Register(n Notifier) {
	s.registry.Register(n)
}

// Repo returns the underlying repository (for resolver wiring).
func (s *Service) Repo() *Repository {
	return s.repo
}

// Hook returns the RecordHook that the notifier service uses to process
// record inserts/updates/deletes. Registered with HookLogContinue so a
// malformed record can't stall firehose ingestion.
func (s *Service) Hook() ingestion.RecordHook {
	return ingestion.RecordHook{
		Name:   "notifications",
		Policy: ingestion.HookLogContinue,
		Fn:     s.process,
	}
}

func (s *Service) process(ctx context.Context, op ingestion.ProcessOp) error {
	notifier, ok := s.registry.Get(op.Collection)
	if !ok {
		return nil
	}

	if op.Operation == ingestion.OpDelete {
		return s.repo.DeleteByRecordURI(ctx, op.URI)
	}

	if op.Operation == ingestion.OpUpdate {
		// Clean up prior participants for this record so the update path
		// correctly reflects the new contributor set.
		if err := s.repo.DeleteByRecordURI(ctx, op.URI); err != nil {
			return fmt.Errorf("delete prior on update: %w", err)
		}
	}

	if len(op.Record) > MaxRecordBytesForNotifications {
		slog.Debug("record too large for notifications",
			"uri", op.URI, "size", len(op.Record))
		return nil
	}

	notifs, err := notifier.Extract(ctx, op)
	if err != nil {
		return fmt.Errorf("extract %s: %w", op.Collection, err)
	}
	if len(notifs) == 0 {
		return nil
	}
	if len(notifs) > MaxFanOutPerRecord {
		slog.Warn("notifications fan-out capped",
			"collection", op.Collection, "uri", op.URI,
			"emitted", len(notifs), "cap", MaxFanOutPerRecord)
		notifs = notifs[:MaxFanOutPerRecord]
	}

	if err := s.repo.Apply(ctx, notifs); err != nil {
		return fmt.Errorf("apply notifications: %w", err)
	}
	return nil
}
