// Package migrations handles database schema migrations.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GainForest/hypergoat/internal/database"
)

//go:embed postgres/*.sql
var postgresMigrations embed.FS

// PostgresFS returns the embedded Postgres migration filesystem.
// Exposed so package-level tests can inspect the migration set
// without spinning up a database connection (see
// migrations_indexnames_test.go, which guards against duplicate
// CREATE INDEX names across migration files — a foot-gun that
// caused 013 to silently no-op against 001).
func PostgresFS() embed.FS { return postgresMigrations }

// Migration represents a single migration.
type Migration struct {
	Version string
	Name    string
	UpSQL   string
	DownSQL string
}

// Run applies all pending migrations.
func Run(ctx context.Context, exec database.Executor) error {
	// Create migrations table if it doesn't exist
	if err := createMigrationsTable(ctx, exec); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get applied migrations
	applied, err := getAppliedMigrations(ctx, exec)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Load migrations for the current dialect
	migrations, err := loadMigrations(exec.Dialect())
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	// Apply pending migrations. Each migration runs inside a single
	// transaction with its schema_migrations row write so we can
	// never persist partial DDL or end up with an applied schema
	// change that the tracking table disagrees about. Postgres
	// supports transactional DDL for the operations we actually
	// run (CREATE/ALTER/DROP TABLE, CREATE INDEX). If a future
	// migration ever needs a non-transactional operation (e.g.,
	// Postgres `CREATE INDEX CONCURRENTLY`) it should be flagged
	// and handled outside this loop.
	for _, m := range migrations {
		if applied[m.Version] {
			slog.Debug("Migration already applied", "version", m.Version, "name", m.Name)
			continue
		}

		slog.Info("Applying migration", "version", m.Version, "name", m.Name)

		if isNonTransactional(m.UpSQL) {
			// Non-transactional migrations (e.g., CREATE INDEX CONCURRENTLY)
			// run outside a transaction. The DDL and version tracking are
			// separate statements.
			if err := applyMigrationNoTx(ctx, exec, m); err != nil {
				return err
			}
		} else {
			if err := applyMigrationTx(ctx, exec, m); err != nil {
				return err
			}
		}

		slog.Info("Migration applied successfully", "version", m.Version)
	}

	return nil
}

// applyMigrationTx applies a single migration's UpSQL and records its
// row in schema_migrations inside one transaction.
func applyMigrationTx(ctx context.Context, exec database.Executor, m Migration) error {
	tx, err := exec.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx for migration %s: %w", m.Version, err)
	}
	// Guard against panics and error returns leaving a stray tx.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, m.UpSQL); err != nil {
		return fmt.Errorf("failed to apply migration %s: %w", m.Version, err)
	}

	insertSQL := fmt.Sprintf(
		"INSERT INTO schema_migrations (version) VALUES (%s)",
		exec.Placeholder(1),
	)
	if _, err := tx.ExecContext(ctx, insertSQL, m.Version); err != nil {
		return fmt.Errorf("failed to record migration %s: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration %s: %w", m.Version, err)
	}
	committed = true
	return nil
}

// isNonTransactional checks if a migration SQL starts with the "-- no-transaction"
// sentinel comment. Such migrations cannot run inside a transaction (e.g., CREATE
// INDEX CONCURRENTLY in PostgreSQL).
func isNonTransactional(sqlText string) bool {
	return strings.HasPrefix(strings.TrimSpace(sqlText), "-- no-transaction")
}

// applyMigrationNoTx applies a migration outside a transaction. Used for
// operations like CREATE INDEX CONCURRENTLY that cannot run in a transaction.
// The DDL runs first; if it succeeds, the version is recorded. If the version
// recording fails, the DDL is still applied (operator must fix manually).
func applyMigrationNoTx(ctx context.Context, exec database.Executor, m Migration) error {
	slog.Warn("Running migration outside transaction (non-transactional)", "version", m.Version)

	if _, err := exec.DB().ExecContext(ctx, m.UpSQL); err != nil {
		return fmt.Errorf("failed to apply non-transactional migration %s: %w", m.Version, err)
	}

	insertSQL := fmt.Sprintf(
		"INSERT INTO schema_migrations (version) VALUES (%s)",
		exec.Placeholder(1),
	)
	if _, err := exec.DB().ExecContext(ctx, insertSQL, m.Version); err != nil {
		return fmt.Errorf("migration %s DDL succeeded but failed to record version (manual fix needed): %w", m.Version, err)
	}

	return nil
}

// Rollback reverses the last applied migration.
func Rollback(ctx context.Context, exec database.Executor) error {
	// Get the last applied migration
	var version string
	err := exec.QueryRow(ctx,
		"SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1",
		nil, &version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Info("No migrations to rollback")
			return nil
		}
		return fmt.Errorf("failed to get last migration: %w", err)
	}

	// Load migrations
	migrations, err := loadMigrations(exec.Dialect())
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	// Find the migration to rollback
	var migration *Migration
	for i := range migrations {
		if migrations[i].Version == version {
			migration = &migrations[i]
			break
		}
	}

	if migration == nil {
		return fmt.Errorf("migration %s not found", version)
	}

	slog.Info("Rolling back migration", "version", version, "name", migration.Name)

	// Non-transactional downs (e.g. DROP INDEX CONCURRENTLY) must run
	// outside a transaction — the sentinel on the down file opts into
	// that path, mirroring the up-migration behavior.
	if isNonTransactional(migration.DownSQL) {
		return rollbackMigrationNoTx(ctx, exec, *migration)
	}

	// Roll back DownSQL and the schema_migrations delete in a
	// single transaction so a crash in the middle cannot leave the
	// two out of sync (matching the Run/applyMigrationTx path).
	tx, err := exec.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin rollback tx for %s: %w", version, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, migration.DownSQL); err != nil {
		return fmt.Errorf("failed to rollback migration %s: %w", version, err)
	}

	deleteSQL := fmt.Sprintf(
		"DELETE FROM schema_migrations WHERE version = %s",
		exec.Placeholder(1),
	)
	if _, err := tx.ExecContext(ctx, deleteSQL, version); err != nil {
		return fmt.Errorf("failed to remove migration record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit rollback of migration %s: %w", version, err)
	}
	committed = true

	slog.Info("Migration rolled back successfully", "version", version)
	return nil
}

// rollbackMigrationNoTx rolls back a migration whose DownSQL opts out of
// transactions (e.g. DROP INDEX CONCURRENTLY). Mirrors applyMigrationNoTx
// — the DDL runs first, then the schema_migrations row is removed. If the
// delete fails after the DDL succeeded the operator must clean up
// manually; the inconsistency is logged and returned as an error.
func rollbackMigrationNoTx(ctx context.Context, exec database.Executor, m Migration) error {
	slog.Warn("Rolling back migration outside transaction (non-transactional)", "version", m.Version)

	if _, err := exec.DB().ExecContext(ctx, m.DownSQL); err != nil {
		return fmt.Errorf("failed to rollback non-transactional migration %s: %w", m.Version, err)
	}

	deleteSQL := fmt.Sprintf(
		"DELETE FROM schema_migrations WHERE version = %s",
		exec.Placeholder(1),
	)
	if _, err := exec.DB().ExecContext(ctx, deleteSQL, m.Version); err != nil {
		return fmt.Errorf("migration %s rollback DDL succeeded but failed to remove schema_migrations row (manual fix needed): %w", m.Version, err)
	}

	slog.Info("Migration rolled back successfully", "version", m.Version)
	return nil
}

func createMigrationsTable(ctx context.Context, exec database.Executor) error {
	sqlStr := `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
	)`

	_, err := exec.DB().ExecContext(ctx, sqlStr)
	return err
}

func getAppliedMigrations(ctx context.Context, exec database.Executor) (map[string]bool, error) {
	applied := make(map[string]bool)

	rows, err := exec.DB().QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, rows.Err()
}

func loadMigrations(dialect database.Dialect) ([]Migration, error) {
	var fs embed.FS
	var dir string

	switch dialect {
	case database.PostgreSQL:
		fs = postgresMigrations
		dir = "postgres"
	default:
		return nil, fmt.Errorf("unsupported dialect: %s", dialect)
	}

	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Group up/down files by version
	migrationFiles := make(map[string]map[string]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		// Parse filename: 001_name.up.sql or 001_name.down.sql
		parts := strings.Split(name, ".")
		if len(parts) < 3 {
			continue
		}

		direction := parts[len(parts)-2] // "up" or "down"
		if direction != "up" && direction != "down" {
			continue
		}

		baseName := strings.Join(parts[:len(parts)-2], ".")
		version := strings.Split(baseName, "_")[0]

		if migrationFiles[version] == nil {
			migrationFiles[version] = make(map[string]string)
			migrationFiles[version]["name"] = baseName
		}

		content, err := fs.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("failed to read migration %s: %w", name, err)
		}

		migrationFiles[version][direction] = string(content)
	}

	// Convert to slice and sort
	var migrations []Migration
	for version, files := range migrationFiles {
		migrations = append(migrations, Migration{
			Version: version,
			Name:    files["name"],
			UpSQL:   files["up"],
			DownSQL: files["down"],
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}
