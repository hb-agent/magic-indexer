// Package testutil provides shared test helpers for the hypergoat test suite.
package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/migrations"
	"github.com/GainForest/hypergoat/internal/database/postgres"
	"github.com/GainForest/hypergoat/internal/database/repositories"
)

// TestDB holds a test database with all migrations applied and
// pre-constructed repository instances.
type TestDB struct {
	Executor         database.Executor
	Records          *repositories.RecordsRepository
	Actors           *repositories.ActorsRepository
	Config           *repositories.ConfigRepository
	Lexicons         *repositories.LexiconsRepository
	Activity         *repositories.JetstreamActivityRepository
	OAuthClients     *repositories.OAuthClientsRepository
	Labels           *repositories.LabelsRepository
	LabelDefinitions *repositories.LabelDefinitionsRepository
	LabelPreferences *repositories.LabelPreferencesRepository
	Reports          *repositories.ReportsRepository
}

// SetupTestDB creates a fresh test database with all migrations applied.
// The database is automatically closed when the test completes.
//
// When TEST_DATABASE_URL is set, that Postgres URL is used. Otherwise
// the default local dev URL postgres://hypergoat:hypergoat@localhost:5432/hypergoat_test?sslmode=disable
// is used.
//
// Safety: this helper runs DELETE FROM on every table (except
// label_definition, whose seeded rows from migration 003 are needed
// by FK checks) before returning, so repeated runs start clean.
// Never point this at a non-throwaway database.
func SetupTestDB(t *testing.T) *TestDB {
	t.Helper()

	exec := newTestExecutor(t)

	ctx := context.Background()
	if err := migrations.Run(ctx, exec); err != nil {
		// Migration tests may leave the DB in a dirty state (tables
		// exist but schema_migrations was dropped). Drop everything
		// and retry.
		_, _ = exec.DB().ExecContext(ctx, `
			DO $$ DECLARE r RECORD;
			BEGIN
				FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
					EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(r.tablename) || ' CASCADE';
				END LOOP;
			END $$;
		`)
		if err := migrations.Run(ctx, exec); err != nil {
			exec.Close()
			t.Fatalf("Failed to run migrations (even after schema reset): %v", err)
		}
	}

	// Each test needs an empty slate since the database is shared
	// across invocations.
	resetBetweenTests(t, exec)

	db := &TestDB{
		Executor:         exec,
		Records:          repositories.NewRecordsRepository(exec),
		Actors:           repositories.NewActorsRepository(exec),
		Config:           repositories.NewConfigRepository(exec),
		Lexicons:         repositories.NewLexiconsRepository(exec),
		Activity:         repositories.NewJetstreamActivityRepository(exec),
		OAuthClients:     repositories.NewOAuthClientsRepository(exec),
		Labels:           repositories.NewLabelsRepository(exec),
		LabelDefinitions: repositories.NewLabelDefinitionsRepository(exec),
		LabelPreferences: repositories.NewLabelPreferencesRepository(exec),
		Reports:          repositories.NewReportsRepository(exec),
	}

	t.Cleanup(func() {
		exec.Close()
	})

	return db
}

// newTestExecutor returns a Postgres executor. When TEST_DATABASE_URL
// is set, that URL is used; otherwise a local default is assumed.
func newTestExecutor(t *testing.T) database.Executor {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://hypergoat:hypergoat@localhost:5432/hypergoat_test?sslmode=disable"
		t.Log("TEST_DATABASE_URL not set, using default:", url)
	}

	if !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://") {
		t.Fatalf("Unrecognized TEST_DATABASE_URL %q: expected postgres:// or postgresql:// prefix", url)
	}

	// Tests use no server-side statement_timeout cap (issue #71's
	// Layer 1) — test harnesses set deadlines via context.Context as
	// needed. Tests that exercise the timeout path inject the value
	// directly via TEST-only env handling.
	exec, err := postgres.NewExecutor(url, 0)
	if err != nil {
		t.Fatalf("Failed to create Postgres test database at %s: %v", redact(url), err)
	}
	return exec
}

// resetBetweenTests clears every mutable table in the right order so
// shared-Postgres runs start clean. label_definition is deliberately
// left alone because its migration-seeded Bluesky default rows are
// referenced by label.val as a foreign key — truncating it would
// cause every subsequent label insert to fail FK validation.
// Order matters because of FKs: children first, parents last.
func resetBetweenTests(t *testing.T, exec database.Executor) {
	t.Helper()
	tables := []string{
		// Children first (FK order), parents last.
		"notification_participant",
		"notification",
		"actor_state",
		"oauth_authorization_code",
		"oauth_access_token",
		"oauth_refresh_token",
		"oauth_par_request",
		"oauth_dpop_jti",
		"oauth_dpop_nonce",
		"oauth_auth_request",
		"oauth_atp_request",
		"oauth_atp_session",
		"admin_session",
		"oauth_client",
		"label",
		"label_preferences",
		"report",
		"record",
		"actor",
		"lexicon",
		"jetstream_activity",
		"jetstream_cursor",
		"config",
	}
	for _, tbl := range tables {
		// Ignore errors — the table may not exist in all migration states.
		_, _ = exec.Exec(context.Background(), "DELETE FROM "+tbl, nil)
	}
}

// redact hides the password portion of a database URL for logging.
func redact(url string) string {
	at := strings.Index(url, "@")
	if at < 0 {
		return url
	}
	start := strings.Index(url, "://")
	if start < 0 {
		return url
	}
	start += 3
	creds := url[start:at]
	if i := strings.Index(creds, ":"); i >= 0 {
		return url[:start+i+1] + "***" + url[at:]
	}
	return url
}
