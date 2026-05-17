// Package server contains the main server initialization and orchestration.
package server

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/GainForest/hypergoat/internal/config"
	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/postgres"
)

// ConnectDatabase creates a database executor for a Postgres URL.
// Supported formats:
//   - postgres://user:pass@host:port/dbname
//   - postgresql://user:pass@host:port/dbname
//
// statementTimeoutMs is the server-side per-statement timeout
// (issue #71's Layer 1). Postgres applies it at session start to
// every connection in the pool. Pass 0 to disable (tests).
//
// Rejection error redacts the password so a misconfigured operator
// doesn't paste credentials into a log line (R1.2 in
// docs/review-2026-05-17-part-2/review-round-1.md — pre-existing bug
// folded into Track 7.Z when the dialect switch went away).
func ConnectDatabase(databaseURL string, statementTimeoutMs int) (database.Executor, error) {
	redacted := config.RedactPassword(databaseURL)

	if !isPostgresURL(databaseURL) {
		return nil, fmt.Errorf("unsupported database URL %q: scheme must be postgres:// or postgresql://", redacted)
	}

	slog.Info("Connecting to Postgres",
		"url", redacted,
		"statement_timeout_ms", statementTimeoutMs,
	)

	return postgres.NewExecutor(databaseURL, statementTimeoutMs)
}

// isPostgresURL reports whether databaseURL has a postgres or postgresql scheme.
func isPostgresURL(databaseURL string) bool {
	lower := strings.ToLower(databaseURL)
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}
