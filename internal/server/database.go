// Package server contains the main server initialization and orchestration.
package server

import (
	"fmt"
	"log/slog"

	"github.com/GainForest/hypergoat/internal/config"
	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/postgres"
)

// ConnectDatabase creates a database executor based on the database URL.
// Supported formats:
//   - postgres://user:pass@host:port/dbname
//   - postgresql://user:pass@host:port/dbname
//
// statementTimeoutMs is the server-side per-statement timeout
// (issue #71's Layer 1). Postgres applies it at session start to
// every connection in the pool. Pass 0 to disable (tests).
func ConnectDatabase(databaseURL string, statementTimeoutMs int) (database.Executor, error) {
	dialect := database.ParseDialect(databaseURL)

	slog.Info("Connecting to database",
		"dialect", dialect.String(),
		"url", config.RedactPassword(databaseURL),
		"statement_timeout_ms", statementTimeoutMs,
	)

	switch dialect {
	case database.PostgreSQL:
		return postgres.NewExecutor(databaseURL, statementTimeoutMs)
	default:
		return nil, fmt.Errorf("unsupported database URL: %s", databaseURL)
	}
}
