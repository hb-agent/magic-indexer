package notifications

import (
	"context"
	"testing"

	"github.com/GainForest/hypergoat/internal/ingestion"
)

type mockNotifier struct {
	collection string
}

func (m *mockNotifier) Collection() string { return m.collection }
func (m *mockNotifier) Extract(ctx context.Context, op ingestion.ProcessOp) ([]Notification, error) {
	return nil, nil
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("expected no notifier for unknown collection")
	}
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	n1 := &mockNotifier{collection: "a.b.c"}
	r.Register(n1)
	got, ok := r.Get("a.b.c")
	if !ok {
		t.Fatal("expected notifier to be registered")
	}
	if got.Collection() != "a.b.c" {
		t.Errorf("got wrong notifier: %s", got.Collection())
	}
}

func TestRegistry_OverwriteSameCollection(t *testing.T) {
	r := NewRegistry()
	n1 := &mockNotifier{collection: "x"}
	n2 := &mockNotifier{collection: "x"}
	r.Register(n1)
	r.Register(n2)
	got, _ := r.Get("x")
	if got != n2 {
		t.Errorf("expected second registration to overwrite first")
	}
}
