package notifications

import (
	"context"
	"errors"
	"testing"

	"github.com/GainForest/hypergoat/internal/ingestion"
)

type countingNotifier struct {
	collection string
	extracted  int
	err        error
}

func (c *countingNotifier) Collection() string { return c.collection }
func (c *countingNotifier) Extract(ctx context.Context, op ingestion.ProcessOp) ([]Notification, error) {
	c.extracted++
	return nil, c.err
}

func TestService_HookSkipsUnknownCollection(t *testing.T) {
	s := NewService(nil) // nil repo OK since we never reach it
	s.Register(&countingNotifier{collection: "known"})

	err := s.process(context.Background(), ingestion.ProcessOp{
		Collection: "unknown",
		Operation:  ingestion.OpCreate,
	})
	if err != nil {
		t.Errorf("expected nil for unknown collection, got %v", err)
	}
}

func TestService_HookSkipsOversizedRecord(t *testing.T) {
	n := &countingNotifier{collection: "c"}
	s := NewService(nil)
	s.Register(n)

	big := make([]byte, MaxRecordBytesForNotifications+1)
	err := s.process(context.Background(), ingestion.ProcessOp{
		Collection: "c",
		Operation:  ingestion.OpCreate,
		Record:     big,
	})
	if err != nil {
		t.Errorf("expected nil for oversized record, got %v", err)
	}
	if n.extracted != 0 {
		t.Errorf("extractor should not have been called: called %d times", n.extracted)
	}
}

func TestService_HookPropagatesExtractError(t *testing.T) {
	want := errors.New("extraction failed")
	n := &countingNotifier{collection: "c", err: want}
	s := NewService(nil)
	s.Register(n)

	err := s.process(context.Background(), ingestion.ProcessOp{
		Collection: "c",
		Operation:  ingestion.OpCreate,
		Record:     []byte("{}"),
	})
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

func TestService_HookName(t *testing.T) {
	s := NewService(nil)
	if s.Hook().Name != "notifications" {
		t.Errorf("unexpected hook name: %s", s.Hook().Name)
	}
	if s.Hook().Policy != ingestion.HookLogContinue {
		t.Errorf("expected HookLogContinue policy")
	}
}
