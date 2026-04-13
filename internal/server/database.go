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
func ConnectDatabase(databaseURL string) (database.Executor, error) {
	dialect := database.ParseDialect(databaseURL)

	slog.Info("Connecting to database",
		"dialect", dialect.String(),
		"url", config.RedactPassword(databaseURL),
	)

	switch dialect {
	case database.PostgreSQL:
		return postgres.NewExecutor(databaseURL)
	default:
		return nil, fmt.Errorf("unsupported database URL: %s", databaseURL)
	}
}
