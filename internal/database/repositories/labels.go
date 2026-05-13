package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// Label represents an applied label on a record or account.
type Label struct {
	ID  int64
	Src string  // DID of the labeler
	URI string  // Subject URI (at:// or did:)
	CID *string // Optional CID for version-specific label
	Val string  // Label value (e.g., 'porn', '!takedown')
	Neg bool    // True if this is a negation (retraction)
	Cts time.Time
	Exp *time.Time // Optional expiration
}

// PaginatedLabels holds paginated label results.
type PaginatedLabels struct {
	Labels      []Label
	HasNextPage bool
	TotalCount  int64
}

// LabelsRepository handles label persistence.
type LabelsRepository struct {
	db database.Executor
}

// NewLabelsRepository creates a new labels repository.
func NewLabelsRepository(db database.Executor) *LabelsRepository {
	return &LabelsRepository{db: db}
}

// parseStoredTime parses a timestamp string that may have been written
// by this code (UTC RFC3339Nano) or by Postgres' TIMESTAMPTZ serializer
// (RFC3339 with offset, no fractional seconds). Also handles the
// "YYYY-MM-DD HH:MM:SS" format for legacy data. Returns the zero time
// on unrecognized input; callers fall back to defaults via
// time.IsZero() if needed.
func parseStoredTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Time{}
}

// Insert creates a new label.
//
// If cts is non-nil, it is stored as the label's canonical timestamp.
// If cts is nil (e.g. labels authored locally via the admin API) the
// current time is used. In either case the value is always written as
// a UTC RFC3339Nano string for consistent text ordering.
func (r *LabelsRepository) Insert(ctx context.Context, src, uri string, cid *string, val string, cts, exp *time.Time) (*Label, error) {
	var sqlStr string
	var expStr *string
	if exp != nil {
		s := exp.Format(time.RFC3339Nano)
		expStr = &s
	}
	effectiveCts := cts
	if effectiveCts == nil {
		now := time.Now().UTC()
		effectiveCts = &now
	}
	ctsStr := effectiveCts.UTC().Format(time.RFC3339Nano)

	// Partial unique indexes (see migration 007) make ON CONFLICT DO
	// NOTHING actually fire, so re-ingesting a label during a
	// backfill/stream overlap is idempotent.
	sqlStr = fmt.Sprintf(`INSERT INTO label (src, uri, cid, val, cts, exp)
		VALUES (%s, %s, %s, %s, %s, %s)
		ON CONFLICT (src, uri, val, COALESCE(cid, '')) WHERE neg = false DO NOTHING
		RETURNING id, src, uri, cid, val, neg, cts, exp`,
		r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3),
		r.db.Placeholder(4), r.db.Placeholder(5), r.db.Placeholder(6))

	params := []database.Value{
		database.Text(src),
		database.Text(uri),
		database.NullableText(cid),
		database.Text(val),
		database.Text(ctsStr),
		database.NullableText(expStr),
	}

	var label Label
	var retCtsStr string
	var cidNull, expNull sql.NullString
	var neg bool
	err := r.db.QueryRow(ctx, sqlStr, params,
		&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &retCtsStr, &expNull)
	if err != nil {
		// ON CONFLICT DO NOTHING → zero rows returned → sql.ErrNoRows.
		// Treat as an idempotent success: look up the existing row so
		// callers still receive a populated Label.
		if errors.Is(err, sql.ErrNoRows) {
			return r.findExistingAssertion(ctx, src, uri, val, cid)
		}
		return nil, err
	}
	label.Neg = neg
	label.Cts = parseStoredTime(retCtsStr)
	if cidNull.Valid {
		label.CID = &cidNull.String
	}
	if expNull.Valid {
		t := parseStoredTime(expNull.String)
		label.Exp = &t
	}
	return &label, nil
}

// findExistingAssertion returns the current active (non-negated) label for
// the given (src, uri, val, cid) tuple. Used to resolve the "row already
// existed" branch of ON CONFLICT DO NOTHING so Insert always returns a
// populated Label for callers.
func (r *LabelsRepository) findExistingAssertion(ctx context.Context, src, uri, val string, cid *string) (*Label, error) {
	negFalse := "false"
	var sqlStr string
	var params []database.Value
	if cid == nil {
		sqlStr = fmt.Sprintf(`SELECT id, src, uri, cid, val, neg, cts, exp FROM label
			WHERE src = %s AND uri = %s AND val = %s AND cid IS NULL AND neg = %s
			ORDER BY id DESC LIMIT 1`,
			r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3), negFalse)
		params = []database.Value{database.Text(src), database.Text(uri), database.Text(val)}
	} else {
		sqlStr = fmt.Sprintf(`SELECT id, src, uri, cid, val, neg, cts, exp FROM label
			WHERE src = %s AND uri = %s AND val = %s AND cid = %s AND neg = %s
			ORDER BY id DESC LIMIT 1`,
			r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3), r.db.Placeholder(4), negFalse)
		params = []database.Value{database.Text(src), database.Text(uri), database.Text(val), database.Text(*cid)}
	}

	var label Label
	var retCtsStr string
	var cidNull, expNull sql.NullString
	var neg bool
	if err := r.db.QueryRow(ctx, sqlStr, params,
		&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &retCtsStr, &expNull); err != nil {
		return nil, err
	}
	label.Neg = neg
	label.Cts = parseStoredTime(retCtsStr)
	if cidNull.Valid {
		label.CID = &cidNull.String
	}
	if expNull.Valid {
		t := parseStoredTime(expNull.String)
		label.Exp = &t
	}
	return &label, nil
}

// InsertNegation creates a negation (retraction) label.
//
// Like Insert, cts is always persisted in UTC RFC3339Nano format for
// consistent text ordering. If cts is nil the current time is used.
func (r *LabelsRepository) InsertNegation(ctx context.Context, src, uri, val string, cts *time.Time) (*Label, error) {
	effectiveCts := cts
	if effectiveCts == nil {
		now := time.Now().UTC()
		effectiveCts = &now
	}
	ctsStr := effectiveCts.UTC().Format(time.RFC3339Nano)

	negTrue := "true"

	sqlStr := fmt.Sprintf(`INSERT INTO label (src, uri, val, neg, cts)
		VALUES (%s, %s, %s, %s, %s)
		ON CONFLICT (src, uri, val) WHERE neg = true DO NOTHING
		RETURNING id, src, uri, cid, val, neg, cts, exp`,
		r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3), negTrue, r.db.Placeholder(4))

	params := []database.Value{
		database.Text(src),
		database.Text(uri),
		database.Text(val),
		database.Text(ctsStr),
	}

	var label Label
	var retCtsStr string
	var cidNull, expNull sql.NullString
	var neg bool
	err := r.db.QueryRow(ctx, sqlStr, params,
		&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &retCtsStr, &expNull)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r.findExistingNegation(ctx, src, uri, val)
		}
		return nil, err
	}
	label.Neg = neg
	label.Cts = parseStoredTime(retCtsStr)
	if cidNull.Valid {
		label.CID = &cidNull.String
	}
	_ = expNull // InsertNegation does not set exp
	return &label, nil
}

// findExistingNegation returns the current negation row for the given
// (src, uri, val) tuple. Used to resolve the ON CONFLICT DO NOTHING
// branch of InsertNegation.
func (r *LabelsRepository) findExistingNegation(ctx context.Context, src, uri, val string) (*Label, error) {
	negTrue := "true"
	sqlStr := fmt.Sprintf(`SELECT id, src, uri, cid, val, neg, cts, exp FROM label
		WHERE src = %s AND uri = %s AND val = %s AND neg = %s
		ORDER BY id DESC LIMIT 1`,
		r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3), negTrue)
	params := []database.Value{database.Text(src), database.Text(uri), database.Text(val)}

	var label Label
	var retCtsStr string
	var cidNull, expNull sql.NullString
	var neg bool
	if err := r.db.QueryRow(ctx, sqlStr, params,
		&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &retCtsStr, &expNull); err != nil {
		return nil, err
	}
	label.Neg = neg
	label.Cts = parseStoredTime(retCtsStr)
	if cidNull.Valid {
		label.CID = &cidNull.String
	}
	_ = expNull
	return &label, nil
}

// GetByID retrieves a label by ID.
func (r *LabelsRepository) GetByID(ctx context.Context, id int64) (*Label, error) {
	sqlStr := fmt.Sprintf(`SELECT id, src, uri, cid, val, neg, cts, exp
		FROM label WHERE id = %s`, r.db.Placeholder(1))

	var label Label
	var ctsStr string
	var cidNull, expNull sql.NullString
	var neg bool

	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Int(id)},
		&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &ctsStr, &expNull)
	if err != nil {
		return nil, err
	}

	label.Neg = neg
	label.Cts = parseStoredTime(ctsStr)
	if cidNull.Valid {
		label.CID = &cidNull.String
	}
	if expNull.Valid {
		t := parseStoredTime(expNull.String)
		label.Exp = &t
	}

	return &label, nil
}

// GetAllForURI returns every label row stored against a single URI,
// including negations and expired labels. This is a diagnostic view
// used by the /admin/label-chain inspection endpoint — ordinary
// query paths should use GetByURIs, which filters to the active set.
// Results are ordered by cts descending so an operator reading the
// response tops-down sees the most recent activity first.
func (r *LabelsRepository) GetAllForURI(ctx context.Context, uri string) ([]Label, error) {
	sqlStr := fmt.Sprintf(`SELECT l.id, l.src, l.uri, l.cid, l.val, l.neg, l.cts, l.exp
		FROM label l
		WHERE l.uri = %s
		ORDER BY l.cts DESC, l.id DESC`, r.db.Placeholder(1))

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, uri)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabels(rows)
}

// GetByURIs retrieves active (non-negated) labels for a list of URIs.
func (r *LabelsRepository) GetByURIs(ctx context.Context, uris []string) ([]Label, error) {
	if len(uris) == 0 {
		return nil, nil
	}

	placeholders := r.db.Placeholders(len(uris), 1)
	negFalse, negTrue := "false", "true"
	now := "NOW()"
	// Get only labels that haven't been negated. The negation check uses
	// cts (the labeler's canonical timestamp) rather than the local
	// auto-increment id, so a backfilled negation with an earlier wire
	// cts correctly retracts an already-streamed assertion. Expired
	// labels (l.exp <= now) are filtered out here rather than cleaned
	// up by a background job.
	sqlStr := fmt.Sprintf(`SELECT l.id, l.src, l.uri, l.cid, l.val, l.neg, l.cts, l.exp
		FROM label l
		WHERE l.uri IN (%s) AND l.neg = %s
		AND (l.exp IS NULL OR l.exp > %s)
		AND NOT EXISTS (
			SELECT 1 FROM label neg
			WHERE neg.uri = l.uri AND neg.src = l.src AND neg.val = l.val
			  AND neg.neg = %s AND neg.cts >= l.cts
		)
		ORDER BY l.cts DESC`, placeholders, negFalse, now, negTrue)

	params := make([]any, len(uris))
	for i, uri := range uris {
		params[i] = uri
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabels(rows)
}

// GetPaginated retrieves labels with optional filters and pagination.
func (r *LabelsRepository) GetPaginated(ctx context.Context, uriFilter, valFilter *string, first int, afterID *int64) (*PaginatedLabels, error) {
	// Build WHERE clause
	var conditions []string
	var params []any
	paramIdx := 1

	if uriFilter != nil {
		conditions = append(conditions, fmt.Sprintf("uri = %s", r.db.Placeholder(paramIdx)))
		params = append(params, *uriFilter)
		paramIdx++
	}

	if valFilter != nil {
		conditions = append(conditions, fmt.Sprintf("val = %s", r.db.Placeholder(paramIdx)))
		params = append(params, *valFilter)
		paramIdx++
	}

	if afterID != nil {
		conditions = append(conditions, fmt.Sprintf("id < %s", r.db.Placeholder(paramIdx)))
		params = append(params, *afterID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM label %s", whereClause)
	var totalCount int64
	if err := r.db.DB().QueryRowContext(ctx, countSQL, params...).Scan(&totalCount); err != nil {
		return nil, err
	}

	// Get labels
	sqlStr := fmt.Sprintf(`SELECT id, src, uri, cid, val, neg, cts, exp
		FROM label %s
		ORDER BY id DESC
		LIMIT %d`, whereClause, first+1)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	labels, err := scanLabels(rows)
	if err != nil {
		return nil, err
	}

	hasNextPage := len(labels) > first
	if hasNextPage {
		labels = labels[:first]
	}

	return &PaginatedLabels{
		Labels:      labels,
		HasNextPage: hasNextPage,
		TotalCount:  totalCount,
	}, nil
}

// HasTakedown checks if a URI has an active !takedown label from any
// trusted labeler. When allowedSrcs is non-empty, only takedowns from
// those labelers count; an empty list means "any labeler". In a
// multi-labeler deployment this lets operators scope which labelers
// can initiate a takedown across the whole index.
func (r *LabelsRepository) HasTakedown(ctx context.Context, uri string, allowedSrcs []string) (bool, error) {
	negFalse, negTrue := "false", "true"

	params := []database.Value{database.Text(uri)}
	paramIdx := 2
	srcClause := ""
	if len(allowedSrcs) > 0 {
		srcPhs := make([]string, len(allowedSrcs))
		for i, s := range allowedSrcs {
			srcPhs[i] = r.db.Placeholder(paramIdx)
			paramIdx++
			params = append(params, database.Text(s))
		}
		srcClause = " AND src IN (" + strings.Join(srcPhs, ", ") + ")"
	}

	sqlStr := fmt.Sprintf(`SELECT COUNT(*) FROM label
		WHERE uri = %s AND val = '!takedown' AND neg = %s%s
		AND (exp IS NULL OR exp > %s)
		AND NOT EXISTS (
			SELECT 1 FROM label neg
			WHERE neg.uri = label.uri AND neg.src = label.src AND neg.val = '!takedown'
			  AND neg.neg = %s AND neg.cts >= label.cts
		)`, r.db.Placeholder(1), negFalse, srcClause, "NOW()", negTrue)

	var count int64
	err := r.db.QueryRow(ctx, sqlStr, params, &count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetTakedownURIs returns the subset of the given URIs that have an
// active !takedown label from any trusted labeler. When allowedSrcs is
// non-empty, only takedowns from those labelers are considered.
func (r *LabelsRepository) GetTakedownURIs(ctx context.Context, uris, allowedSrcs []string) ([]string, error) {
	if len(uris) == 0 {
		return nil, nil
	}

	negFalse, negTrue := "false", "true"

	uriPhs := make([]string, len(uris))
	params := make([]any, 0, len(uris)+len(allowedSrcs))
	for i, u := range uris {
		uriPhs[i] = r.db.Placeholder(i + 1)
		params = append(params, u)
	}

	srcClause := ""
	if len(allowedSrcs) > 0 {
		srcPhs := make([]string, len(allowedSrcs))
		for i, s := range allowedSrcs {
			srcPhs[i] = r.db.Placeholder(len(uris) + i + 1)
			params = append(params, s)
		}
		srcClause = " AND l.src IN (" + strings.Join(srcPhs, ", ") + ")"
	}

	sqlStr := fmt.Sprintf(`SELECT DISTINCT l.uri FROM label l
		WHERE l.uri IN (%s) AND l.val = '!takedown' AND l.neg = %s%s
		AND (l.exp IS NULL OR l.exp > %s)
		AND NOT EXISTS (
			SELECT 1 FROM label neg
			WHERE neg.uri = l.uri AND neg.src = l.src AND neg.val = '!takedown'
			  AND neg.neg = %s AND neg.cts >= l.cts
		)`, strings.Join(uriPhs, ", "), negFalse, srcClause, "NOW()", negTrue)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var uri string
		if err := rows.Scan(&uri); err != nil {
			return nil, err
		}
		result = append(result, uri)
	}

	return result, rows.Err()
}

// DeleteAll removes all labels.
func (r *LabelsRepository) DeleteAll(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM label", nil)
	return err
}

// IsValidSubjectURI validates an AT Protocol subject URI format.
// Permissive shape check by design: a subject URI is either an at:// URI
// or a DID URI (`did:plc:...` / `did:web:...`). The strict per-method
// DID format check lives in `internal/atproto/did.IsValid`; this helper
// only discriminates between the two URI families.
// allow-did-prefix: format discriminator, not validator
func IsValidSubjectURI(uri string) bool {
	return strings.HasPrefix(uri, "at://") || strings.HasPrefix(uri, "did:") //nolint:revive // allow-did-prefix
}

// Helper function to scan labels from rows
func scanLabels(rows *sql.Rows) ([]Label, error) {
	var labels []Label
	for rows.Next() {
		var label Label
		var ctsStr string
		var cidNull, expNull sql.NullString
		var neg bool

		if err := rows.Scan(&label.ID, &label.Src, &label.URI, &cidNull, &label.Val, &neg, &ctsStr, &expNull); err != nil {
			return nil, err
		}

		label.Neg = neg
		label.Cts = parseStoredTime(ctsStr)
		if cidNull.Valid {
			label.CID = &cidNull.String
		}
		if expNull.Valid {
			t := parseStoredTime(expNull.String)
			label.Exp = &t
		}
		labels = append(labels, label)
	}

	return labels, rows.Err()
}
