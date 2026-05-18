// Package database provides a unified interface for database operations.
//
// magic-indexer targets Postgres. The Dialect enum, ParseDialect helper,
// and the Executor.Placeholder/Placeholders methods that previously
// pretended dialect portability were removed in review-2026-05-17 part 2
// (Track 7) — every repository now writes literal $N placeholders
// directly.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Value represents a parameter value for database queries.
type Value interface {
	isValue()
}

// TextValue represents a string value.
type TextValue string

func (TextValue) isValue() {}

// IntValue represents an integer value.
type IntValue int64

func (IntValue) isValue() {}

// FloatValue represents a floating point value.
type FloatValue float64

func (FloatValue) isValue() {}

// BoolValue represents a boolean value.
type BoolValue bool

func (BoolValue) isValue() {}

// NullValue represents a null value.
type NullValue struct{}

func (NullValue) isValue() {}

// BlobValue represents binary data.
type BlobValue []byte

func (BlobValue) isValue() {}

// TimestamptzValue represents an ISO 8601 timestamp (TIMESTAMPTZ).
type TimestamptzValue string

func (TimestamptzValue) isValue() {}

// DbError represents a database error with categorization.
type DbError struct {
	Type    string // "connection", "query", "decode", "constraint"
	Message string
	Cause   error
}

func (e *DbError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *DbError) Unwrap() error {
	return e.Cause
}

// Error constructors
func ConnectionError(msg string, cause error) *DbError {
	return &DbError{Type: "connection", Message: msg, Cause: cause}
}

func QueryError(msg string, cause error) *DbError {
	return &DbError{Type: "query", Message: msg, Cause: cause}
}

func ConstraintError(msg string, cause error) *DbError {
	return &DbError{Type: "constraint", Message: msg, Cause: cause}
}

// Executor provides a unified interface for database operations.
// Postgres-only after review-2026-05-17 part 2.
type Executor interface {
	// QueryRow executes a query expected to return at most one row.
	QueryRow(ctx context.Context, sql string, params []Value, dest ...any) error

	// Exec executes a statement without returning results.
	Exec(ctx context.Context, sql string, params []Value) (sql.Result, error)

	// BeginTx starts a new database transaction.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

	// ConvertParams converts []Value to []any for use with direct *sql.DB calls.
	ConvertParams(params []Value) []any

	// Close closes the database connection.
	Close() error

	// DB returns the underlying *sql.DB for advanced operations.
	DB() *sql.DB
}

// Helper functions for Value conversions

// Text creates a TextValue.
func Text(s string) TextValue {
	return TextValue(s)
}

// Int creates an IntValue.
func Int(i int64) IntValue {
	return IntValue(i)
}

// Float creates a FloatValue.
func Float(f float64) FloatValue {
	return FloatValue(f)
}

// Bool creates a BoolValue.
func Bool(b bool) BoolValue {
	return BoolValue(b)
}

// Null creates a NullValue.
func Null() NullValue {
	return NullValue{}
}

// Blob creates a BlobValue.
func Blob(b []byte) BlobValue {
	return BlobValue(b)
}

// Timestamptz creates a TimestamptzValue from a time.Time.
func Timestamptz(t time.Time) TimestamptzValue {
	return TimestamptzValue(t.UTC().Format(time.RFC3339))
}

// TimestamptzString creates a TimestamptzValue from an ISO 8601 string.
func TimestamptzString(s string) TimestamptzValue {
	return TimestamptzValue(s)
}

// NullableText returns a TextValue or NullValue.
func NullableText(s *string) Value {
	if s == nil {
		return Null()
	}
	return Text(*s)
}

// NullableInt returns an IntValue or NullValue.
func NullableInt(i *int64) Value {
	if i == nil {
		return Null()
	}
	return Int(*i)
}

// NullableBool returns a BoolValue or NullValue.
func NullableBool(b *bool) Value {
	if b == nil {
		return Null()
	}
	return Bool(*b)
}
