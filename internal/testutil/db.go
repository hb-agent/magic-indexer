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
	"github.com/GainForest/hypergoat/internal/database/sqlite"
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
// By default an in-memory SQLite database is used so `go test` works
// offline with zero configuration and in CI. Developers (or later CI
// jobs) that want to exercise dialect-specific behaviour like the
// BOOLEAN neg column can opt in by setting TEST_DATABASE_URL to a
// postgres:// URL. We deliberately do NOT honour the application's
// DATABASE_URL env var here — many upstream tests pre-date Postgres
// testing and compare JSONB output verbatim, which fails when keys
// are re-ordered. TEST_DATABASE_URL is the explicit opt-in so nobody
// accidentally trips those failures.
//
// Safety: when TEST_DATABASE_URL is a Postgres URL, this helper runs
// DELETE FROM on every table (except label_definition, whose seeded
// rows from migration 003 are needed by FK checks) before returning,
// so repeated runs start clean. Never point this at a non-throwaway
// database.
func SetupTestDB(t *testing.T) *TestDB {
	t.Helper()

	exec := newTestExecutor(t)

	ctx := context.Background()
	if err := migrations.Run(ctx, exec); err != nil {
		exec.Close()
		t.Fatalf("Failed to run migrations: %v", err)
	}

	if exec.Dialect() == database.PostgreSQL {
		// Each test needs an empty slate on Postgres since the database
		// is shared across invocations.
		resetBetweenTests(t, exec)
	}

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

// newTestExecutor picks SQLite or Postgres based on TEST_DATABASE_URL.
// An unset or sqlite:// URL yields an in-memory SQLite database; a
// postgres:// URL yields a pgx executor connected to that database.
// Note: DATABASE_URL is intentionally NOT consulted here — see the
// SetupTestDB comment for rationale.
func newTestExecutor(t *testing.T) database.Executor {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" || strings.HasPrefix(url, "sqlite:") {
		exec, err := sqlite.NewExecutor("sqlite::memory:")
		if err != nil {
			t.Fatalf("Failed to create SQLite test database: %v", err)
		}
		return exec
	}

	if strings.HasPrefix(url, "postgres://") || strings.HasPrefix(url, "postgresql://") {
		exec, err := postgres.NewExecutor(url)
		if err != nil {
			t.Fatalf("Failed to create Postgres test database at %s: %v", redact(url), err)
		}
		return exec
	}

	t.Fatalf("Unrecognized TEST_DATABASE_URL %q: expected sqlite: or postgres: prefix", url)
	return nil // unreachable
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
