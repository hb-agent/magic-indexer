// Package postgres provides a PostgreSQL implementation of the
// database Executor interface.
//
// Pool-level operational invariant (issue #71): every connection
// inherits `statement_timeout` from the DATABASE_URL `options=`
// parameter, injected by `injectStatementTimeout` at executor
// construction time. Do not issue `SET statement_timeout` without
// `LOCAL` anywhere in the codebase — the URL-level value is the
// contract, and a session-scoped `SET` would leak to subsequent
// users of the same pooled connection.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver

	"github.com/GainForest/hypergoat/internal/database"
)

// Executor implements database.Executor for PostgreSQL.
type Executor struct {
	db *sql.DB
}

// statementTimeoutRegex matches a `statement_timeout=` directive
// in any of the three syntactic forms Postgres accepts inside an
// `options` connection-string value:
//
//	-c statement_timeout=10000    (space — most common)
//	-cstatement_timeout=10000     (getopt short-option-with-value)
//	--statement_timeout=10000     (long-option form)
//
// Anchored at a whitespace or start-of-string boundary so the
// adjacent GUC `idle_in_transaction_session_timeout` cannot
// false-match.
var statementTimeoutRegex = regexp.MustCompile(`(^|\s)(-c\s+|-c|--)statement_timeout=`)

// injectStatementTimeout returns databaseURL with a
// `?options=-c statement_timeout=<ms>` parameter appended if the
// operator has not already set `statement_timeout` (in any of the
// three accepted Postgres syntactic forms — see
// statementTimeoutRegex). The operator's value wins and is logged
// once at startup so the deploy log shows the configured value.
//
// On parse error the original URL is returned unchanged with a
// breadcrumb — `sql.Open` will report the real parse error to the
// operator.
func injectStatementTimeout(databaseURL string, statementTimeoutMs int) string {
	if statementTimeoutMs <= 0 {
		return databaseURL
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		slog.Warn("statement_timeout injection skipped: DATABASE_URL parse failed",
			"error", err,
		)
		return databaseURL
	}
	q := parsed.Query()
	existing := q.Get("options")
	if statementTimeoutRegex.MatchString(existing) {
		// Truncate the logged options value defensively — an operator
		// who shoves something unusually large into `options` should
		// not flood the deploy log.
		shown := existing
		if len(shown) > 256 {
			shown = shown[:256] + "...(truncated)"
		}
		slog.Info("statement_timeout preserved from DATABASE_URL",
			"options", shown,
		)
		return databaseURL
	}
	directive := "-c statement_timeout=" + strconv.Itoa(statementTimeoutMs)
	merged := directive
	if existing != "" {
		merged = existing + " " + directive
	}
	q.Set("options", merged)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// NewExecutor creates a new PostgreSQL executor from a database URL.
// URL format: "postgres://user:pass@host:port/dbname?sslmode=disable"
//
// statementTimeoutMs, when > 0, is injected as a server-side hard
// kill via the URL's `options=-c statement_timeout=<ms>` parameter.
// Every connection in the pool inherits it at session start. Set
// from `DB_STATEMENT_TIMEOUT_MS`; default 30000.
func NewExecutor(databaseURL string, statementTimeoutMs int) (*Executor, error) {
	databaseURL = injectStatementTimeout(databaseURL, statementTimeoutMs)

	// Open the database using pgx driver
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, database.ConnectionError("failed to open PostgreSQL database", err)
	}

	// Configure connection pool. The lifetime bound forces periodic
	// recycling so we don't hold stale connections past a Postgres
	// side restart / failover, and the idle bound keeps the pool
	// from hoarding connections between quiet periods.
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// Test the connection
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, database.ConnectionError("failed to ping PostgreSQL database", err)
	}

	return &Executor{db: db}, nil
}

// QueryRow executes a query expected to return at most one row.
func (e *Executor) QueryRow(ctx context.Context, sqlStr string, params []database.Value, dest ...any) error {
	args := convertParams(params)
	row := e.db.QueryRowContext(ctx, sqlStr, args...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return database.QueryError("failed to scan row", err)
	}
	return nil
}

// BeginTx starts a new database transaction.
func (e *Executor) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return e.db.BeginTx(ctx, opts)
}

// ConvertParams converts []database.Value to []any for direct *sql.DB usage.
func (e *Executor) ConvertParams(params []database.Value) []any {
	return convertParams(params)
}

// Exec executes a statement without returning results.
func (e *Executor) Exec(ctx context.Context, sqlStr string, params []database.Value) (sql.Result, error) {
	args := convertParams(params)
	result, err := e.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		// Check for constraint violations
		if strings.Contains(err.Error(), "unique constraint") ||
			strings.Contains(err.Error(), "foreign key constraint") {
			return nil, database.ConstraintError("constraint violation", err)
		}
		return nil, database.QueryError("failed to execute statement", err)
	}
	return result, nil
}

// Dialect returns PostgreSQL.
func (e *Executor) Dialect() database.Dialect {
	return database.PostgreSQL
}

// Placeholder returns "$n" for the given parameter index (1-based).
func (e *Executor) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

// Placeholders returns a comma-separated list of "$n" placeholders.
func (e *Executor) Placeholders(count, startIndex int) string {
	if count <= 0 {
		return ""
	}
	placeholders := make([]string, count)
	for i := 0; i < count; i++ {
		placeholders[i] = fmt.Sprintf("$%d", startIndex+i)
	}
	return strings.Join(placeholders, ", ")
}

// Close closes the database connection.
func (e *Executor) Close() error {
	return e.db.Close()
}

// DB returns the underlying *sql.DB.
func (e *Executor) DB() *sql.DB {
	return e.db
}

// convertParams converts database.Value slice to []any for sql.DB methods.
func convertParams(params []database.Value) []any {
	if len(params) == 0 {
		return nil
	}

	args := make([]any, len(params))
	for i, param := range params {
		switch v := param.(type) {
		case database.TextValue:
			args[i] = string(v)
		case database.IntValue:
			args[i] = int64(v)
		case database.FloatValue:
			args[i] = float64(v)
		case database.BoolValue:
			// PostgreSQL uses native booleans
			args[i] = bool(v)
		case database.NullValue:
			args[i] = nil
		case database.BlobValue:
			args[i] = []byte(v)
		case database.TimestamptzValue:
			args[i] = string(v)
		default:
			args[i] = param
		}
	}
	return args
}
