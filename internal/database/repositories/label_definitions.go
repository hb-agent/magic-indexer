package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// SystemLabelerSrc is the sentinel DID under which the pre-seeded
// Bluesky label definitions (!takedown, !warn, porn, etc.) live.
// Treat it as an instance-owned labeler whose semantics are defined
// by the operator rather than any external ATProto labeler.
const SystemLabelerSrc = "did:web:system"

// LabelSeverity represents the severity level of a label.
type LabelSeverity string

const (
	SeverityInform   LabelSeverity = "inform"
	SeverityAlert    LabelSeverity = "alert"
	SeverityTakedown LabelSeverity = "takedown"
)

// LabelVisibility represents how labeled content should be displayed.
type LabelVisibility string

const (
	VisibilityIgnore LabelVisibility = "ignore"
	VisibilityShow   LabelVisibility = "show"
	VisibilityWarn   LabelVisibility = "warn"
	VisibilityHide   LabelVisibility = "hide"
)

// LabelDefinition represents a label type definition owned by a
// specific labeler (identified by Src). Two labelers may publish the
// same Val with different descriptions, severities, and default
// visibilities — each gets its own row keyed by (Src, Val).
type LabelDefinition struct {
	Src               string // Labeler DID that owns this definition
	Val               string
	Description       string
	Severity          LabelSeverity
	DefaultVisibility LabelVisibility
	CreatedAt         time.Time
}

// LabelDefinitionsRepository handles label definition persistence.
type LabelDefinitionsRepository struct {
	db database.Executor
}

// NewLabelDefinitionsRepository creates a new label definitions repository.
func NewLabelDefinitionsRepository(db database.Executor) *LabelDefinitionsRepository {
	return &LabelDefinitionsRepository{db: db}
}

// GetAll retrieves every label definition, ordered by (src, val).
func (r *LabelDefinitionsRepository) GetAll(ctx context.Context) ([]LabelDefinition, error) {
	sqlStr := "SELECT src, val, description, severity, default_visibility, created_at FROM label_definition ORDER BY src, val"

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabelDefinitions(rows)
}

// GetBySrc retrieves every label definition owned by the given
// labeler. Pass SystemLabelerSrc to fetch only the pre-seeded Bluesky
// defaults.
func (r *LabelDefinitionsRepository) GetBySrc(ctx context.Context, src string) ([]LabelDefinition, error) {
	rows, err := r.db.DB().QueryContext(ctx,
		"SELECT src, val, description, severity, default_visibility, created_at FROM label_definition WHERE src = $1 ORDER BY val",
		src)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabelDefinitions(rows)
}

// GetNonSystem retrieves all non-system label definitions — rows not
// owned by SystemLabelerSrc AND whose val does not start with `!`.
// Used by the admin UI to list user-manageable labels.
func (r *LabelDefinitionsRepository) GetNonSystem(ctx context.Context) ([]LabelDefinition, error) {
	rows, err := r.db.DB().QueryContext(ctx, `SELECT src, val, description, severity, default_visibility, created_at
		FROM label_definition
		WHERE src <> $1 AND val NOT LIKE '!%'
		ORDER BY src, val`, SystemLabelerSrc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabelDefinitions(rows)
}

// Get retrieves a single label definition for a specific (src, val).
func (r *LabelDefinitionsRepository) Get(ctx context.Context, src, val string) (*LabelDefinition, error) {
	var def LabelDefinition
	var createdAtStr string

	err := r.db.QueryRow(ctx, `SELECT src, val, description, severity, default_visibility, created_at
		FROM label_definition WHERE src = $1 AND val = $2`,
		[]database.Value{database.Text(src), database.Text(val)},
		&def.Src, &def.Val, &def.Description, &def.Severity, &def.DefaultVisibility, &createdAtStr)
	if err != nil {
		return nil, err
	}

	def.CreatedAt = parseStoredTime(createdAtStr)

	return &def, nil
}

// Insert creates a new label definition for the given labeler.
//
// Idempotent on (src, val) via ON CONFLICT DO NOTHING. This closes a
// race on the labeler ingest path: two consumer goroutines can both
// observe Exists=false before either commits, then both try to Insert,
// and the loser previously saw a duplicate-key error. With ON CONFLICT
// DO NOTHING the loser returns nil and the existing row is kept.
// Callers that specifically want to know whether a new row was created
// vs a pre-existing row won in the race can call Exists() afterwards.
func (r *LabelDefinitionsRepository) Insert(ctx context.Context, src, val, description string, severity LabelSeverity, defaultVisibility LabelVisibility) error {
	_, err := r.db.Exec(ctx, `INSERT INTO label_definition (src, val, description, severity, default_visibility)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (src, val) DO NOTHING`,
		[]database.Value{
			database.Text(src),
			database.Text(val),
			database.Text(description),
			database.Text(string(severity)),
			database.Text(string(defaultVisibility)),
		})
	return err
}

// Exists checks whether a label definition exists for a specific
// (src, val) combination.
func (r *LabelDefinitionsRepository) Exists(ctx context.Context, src, val string) (bool, error) {
	var count int64
	err := r.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM label_definition WHERE src = $1 AND val = $2",
		[]database.Value{database.Text(src), database.Text(val)}, &count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}

// ValidateVisibility validates a visibility value.
func ValidateVisibility(visibility string) (LabelVisibility, error) {
	switch visibility {
	case "ignore":
		return VisibilityIgnore, nil
	case "show":
		return VisibilityShow, nil
	case "warn":
		return VisibilityWarn, nil
	case "hide":
		return VisibilityHide, nil
	default:
		return "", fmt.Errorf("invalid visibility: %s", visibility)
	}
}

// ValidateSeverity validates a severity value.
func ValidateSeverity(severity string) (LabelSeverity, error) {
	switch severity {
	case "inform":
		return SeverityInform, nil
	case "alert":
		return SeverityAlert, nil
	case "takedown":
		return SeverityTakedown, nil
	default:
		return "", fmt.Errorf("invalid severity: %s", severity)
	}
}

// Helper function to scan label definitions from rows
func scanLabelDefinitions(rows *sql.Rows) ([]LabelDefinition, error) {
	var definitions []LabelDefinition
	for rows.Next() {
		var def LabelDefinition
		var createdAtStr string

		if err := rows.Scan(&def.Src, &def.Val, &def.Description, &def.Severity, &def.DefaultVisibility, &createdAtStr); err != nil {
			return nil, err
		}

		def.CreatedAt = parseStoredTime(createdAtStr)
		definitions = append(definitions, def)
	}

	return definitions, rows.Err()
}
