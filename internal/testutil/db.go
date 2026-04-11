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
// offline with zero configuration. CI (and anyone who wants Postgres
// coverage locally) can set TEST_DATABASE_URL to a postgres:// URL and
// this helper will connect there instead — that's the only way to
// exercise dialect-specific behaviour like the BOOLEAN neg column.
//
// Safety: when TEST_DATABASE_URL is a Postgres URL, the tests run
// DELETE FROM on every table before returning so repeated runs start
// clean. Never point this at a non-throwaway database.
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
		// is shared. TRUNCATE is faster than DELETE and resets SERIAL
		// sequences too.
		truncateAll(t, exec)
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
func newTestExecutor(t *testing.T) database.Executor {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		// Fall back to DATABASE_URL for convenience in CI jobs that
		// already export it for the application.
		url = os.Getenv("DATABASE_URL")
	}
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

// truncateAll clears every known table in the right order so tests
// start clean on Postgres. Order matters because of FKs: children
// first, parents last.
func truncateAll(t *testing.T, exec database.Executor) {
	t.Helper()
	tables := []string{
		"label",
		"label_preferences",
		"label_definition",
		"report",
		"record",
		"actor",
		"jetstream_activity",
		"config",
		"oauth_clients",
		"lexicons",
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
