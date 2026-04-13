package migrations_test

import (
	"context"
	"os"
	"testing"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/migrations"
	"github.com/GainForest/hypergoat/internal/database/postgres"
)

// newTestExecutor creates a Postgres executor for testing.
// Uses TEST_DATABASE_URL if set, otherwise a local default.
func newTestExecutor(t *testing.T) database.Executor {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://hypergoat:hypergoat@localhost:5432/hypergoat_test?sslmode=disable"
		t.Log("TEST_DATABASE_URL not set, using default:", url)
	}

	exec, err := postgres.NewExecutor(url)
	if err != nil {
		t.Fatalf("failed to create Postgres executor: %v", err)
	}
	t.Cleanup(func() { exec.Close() })

	// Drop ALL tables so each test starts completely fresh.
	// CASCADE handles foreign key dependencies.
	_, _ = exec.DB().ExecContext(context.Background(), `
		DO $$ DECLARE r RECORD;
		BEGIN
			FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
				EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(r.tablename) || ' CASCADE';
			END LOOP;
		END $$;
	`)

	return exec
}

func TestMigrations_Run(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	if err := migrations.Run(ctx, exec); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify key tables exist by querying information_schema.
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
			"SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name=$1", table,
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
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='record'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query information_schema: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record table, got %d", count)
	}
}

// TestMigrations_UpDownUpRoundTrip asserts that applying every
// migration, rolling back the last one, and re-applying it leaves
// the information_schema snapshot identical to the original
// all-applied state. Catches migrations whose DownSQL is lossy or
// whose UpSQL is subtly non-idempotent.
func TestMigrations_UpDownUpRoundTrip(t *testing.T) {
	exec := newTestExecutor(t)
	ctx := context.Background()

	snapshot := func() map[string]string {
		t.Helper()
		rows, err := exec.DB().QueryContext(ctx,
			`SELECT COALESCE(table_type, ''), table_name
			 FROM information_schema.tables
			 WHERE table_schema='public'
			 ORDER BY table_type, table_name`)
		if err != nil {
			t.Fatalf("snapshot query: %v", err)
		}
		defer rows.Close()
		out := make(map[string]string)
		for rows.Next() {
			var kind, name string
			if err := rows.Scan(&kind, &name); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			out[kind+":"+name] = name
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
