package migrations_test

import (
	"context"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/migrations"
	"github.com/GainForest/hypergoat/internal/database/sqlite"
)

// newTestExecutor creates an in-memory SQLite executor for testing.
func newTestExecutor(t *testing.T) *sqlite.Executor {
	t.Helper()

	exec, err := sqlite.NewExecutor("sqlite::memory:")
	if err != nil {
		t.Fatalf("failed to create SQLite executor: %v", err)
	}
	t.Cleanup(func() { exec.Close() })

	return exec
}

func TestMigrations_Run(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify key tables exist by querying sqlite_master.
	expectedTables := []string{
		"record",
		"actor",
		"config",
		"lexicon",
		"jetstream_activity",
		"label",
		"report",
		"label_definition",
		"actor_label_preference",
	}

	for _, table := range expectedTables {
		var name string
		err := exec.DB().QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist, but got error: %v", table, err)
		}
	}
}

func TestMigrations_RunIdempotent(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("first Run() returned error: %v", err)
	}

	// Running a second time should be a no-op (all migrations already applied).
	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("second Run() returned error: %v", err)
	}

	// Verify tables still present after second run.
	var count int
	err := exec.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='record'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query sqlite_master: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record table, got %d", count)
	}
}

// TestMigrations_UpDownUpRoundTrip asserts that applying every
// migration, rolling back the last one, and re-applying it leaves
// the sqlite_master snapshot byte-identical to the original
// all-applied state. Catches migrations whose DownSQL is lossy or
// whose UpSQL is subtly non-idempotent.
func TestMigrations_UpDownUpRoundTrip(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	snapshot := func() map[string]string {
		t.Helper()
		rows, err := exec.DB().QueryContext(ctx,
			"SELECT type, name, COALESCE(sql, '') FROM sqlite_master "+
				"WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
		if err != nil {
			t.Fatalf("snapshot query: %v", err)
		}
		defer rows.Close()
		out := make(map[string]string)
		for rows.Next() {
			var kind, name, sqlStr string
			if err := rows.Scan(&kind, &name, &sqlStr); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			out[kind+":"+name] = sqlStr
		}
		return out
	}

	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	before := snapshot()

	if err := migrations.Rollback(ctx, exec); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	after := snapshot()

	if len(before) != len(after) {
		t.Errorf("object count changed: before=%d after=%d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("object %q diverged after round trip\n  before: %s\n  after:  %s", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("object %q appeared only after round trip: %s", k, after[k])
		}
	}
}

func TestMigrations_Rollback(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	// Apply all migrations first.
	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Count applied migrations before rollback.
	var countBefore int
	err := exec.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations",
	).Scan(&countBefore)
	if err != nil {
		t.Fatalf("failed to count migrations: %v", err)
	}

	if countBefore == 0 {
		t.Fatal("expected at least one applied migration before rollback")
	}

	// Rollback the last migration.
	if err := migrations.Rollback(ctx, exec); err != nil {
		t.Fatalf("Rollback() returned error: %v", err)
	}

	// Verify one fewer migration is recorded.
	var countAfter int
	err = exec.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations",
	).Scan(&countAfter)
	if err != nil {
		t.Fatalf("failed to count migrations after rollback: %v", err)
	}

	if countAfter != countBefore-1 {
		t.Errorf("expected %d migrations after rollback, got %d", countBefore-1, countAfter)
	}
}
