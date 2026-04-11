// Package repositories contains data access layer implementations.
package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/atproto"
	"github.com/GainForest/hypergoat/internal/database"
)

// Batch size constants for SQL operations.
const (
	// BatchInsertSize is the number of records per INSERT batch (5 params each = 500 SQL params).
	BatchInsertSize = 100

	// SQLParamBatchSize is the batch size for IN-clause queries, kept under SQLite's 999 param limit.
	SQLParamBatchSize = 900

	// DefaultIterateBatchSize is the default batch size for IterateAll when none specified.
	DefaultIterateBatchSize = 1000
)

// Record represents an AT Protocol record stored in the database.
type Record struct {
	URI        string
	CID        string
	DID        string
	Collection string
	JSON       string
	IndexedAt  time.Time
	RKey       string
}

// CollectionStat represents statistics for a collection.
type CollectionStat struct {
	Collection string
	Count      int64
}

// TimeSeriesDataPoint represents a single data point in a time series.
type TimeSeriesDataPoint struct {
	Date       string // YYYY-MM-DD format
	Count      int64
	Cumulative int64
}

// CollectionTimeSeries represents time series data for a collection.
type CollectionTimeSeries struct {
	Collection   string
	TotalRecords int64
	UniqueUsers  int64
	Data         []TimeSeriesDataPoint
}

// InsertResult indicates whether a record was inserted or skipped.
type InsertResult int

const (
	Inserted InsertResult = iota
	Skipped
)

// RecordsRepository handles record persistence.
type RecordsRepository struct {
	db database.Executor
}

// NewRecordsRepository creates a new records repository.
func NewRecordsRepository(db database.Executor) *RecordsRepository {
	return &RecordsRepository{db: db}
}

// recordColumns returns the columns to select based on dialect.
func (r *RecordsRepository) recordColumns() string {
	switch r.db.Dialect() {
	case database.PostgreSQL:
		return "uri, cid, did, collection, json::text, indexed_at::text, rkey"
	default:
		return "uri, cid, did, collection, json, indexed_at, rkey"
	}
}

// Insert inserts or updates a record in the database.
// Skips if the CID already exists (content unchanged).
func (r *RecordsRepository) Insert(ctx context.Context, uri, cid, did, collection, jsonData string) (InsertResult, error) {
	// Check if URI exists with same CID
	existingCID, err := r.getCIDByURI(ctx, uri)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Skipped, err
	}

	if existingCID == cid {
		return Skipped, nil // Content unchanged
	}

	p1 := r.db.Placeholder(1)
	p2 := r.db.Placeholder(2)
	p3 := r.db.Placeholder(3)
	p4 := r.db.Placeholder(4)
	p5 := r.db.Placeholder(5)

	var sqlStr string
	switch r.db.Dialect() {
	case database.PostgreSQL:
		sqlStr = fmt.Sprintf(`INSERT INTO record (uri, cid, did, collection, json)
			VALUES (%s, %s, %s, %s, %s::jsonb)
			ON CONFLICT(uri) DO UPDATE SET
				cid = EXCLUDED.cid,
				json = EXCLUDED.json,
				indexed_at = NOW()`, p1, p2, p3, p4, p5)
	default:
		sqlStr = fmt.Sprintf(`INSERT INTO record (uri, cid, did, collection, json)
			VALUES (%s, %s, %s, %s, %s)
			ON CONFLICT(uri) DO UPDATE SET
				cid = excluded.cid,
				json = excluded.json,
				indexed_at = datetime('now')`, p1, p2, p3, p4, p5)
	}

	_, err = r.db.Exec(ctx, sqlStr, []database.Value{
		database.Text(uri),
		database.Text(cid),
		database.Text(did),
		database.Text(collection),
		database.Text(jsonData),
	})
	if err != nil {
		return Skipped, err
	}

	return Inserted, nil
}

// BatchInsert inserts multiple records efficiently.
// Wraps all batch inserts in a single transaction for better performance.
func (r *RecordsRepository) BatchInsert(ctx context.Context, records []*Record) error {
	if len(records) == 0 {
		return nil
	}

	// Start transaction for all batches
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback is a no-op if Commit succeeds

	// Process in batches to stay within SQL parameter limits
	batchSize := BatchInsertSize
	for i := 0; i < len(records); i += batchSize {
		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}
		batch := records[i:end]

		if err := r.insertBatchTx(ctx, tx, batch); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// insertBatchTx inserts a batch of records within a transaction.
func (r *RecordsRepository) insertBatchTx(ctx context.Context, tx *sql.Tx, records []*Record) error {
	// Build value placeholders
	var valueSets []string
	var args []any

	for i, rec := range records {
		base := i * 5
		var valueSet string

		if r.db.Dialect() == database.PostgreSQL {
			valueSet = fmt.Sprintf("(%s, %s, %s, %s, %s::jsonb)",
				r.db.Placeholder(base+1),
				r.db.Placeholder(base+2),
				r.db.Placeholder(base+3),
				r.db.Placeholder(base+4),
				r.db.Placeholder(base+5))
		} else {
			valueSet = fmt.Sprintf("(%s, %s, %s, %s, %s)",
				r.db.Placeholder(base+1),
				r.db.Placeholder(base+2),
				r.db.Placeholder(base+3),
				r.db.Placeholder(base+4),
				r.db.Placeholder(base+5))
		}
		valueSets = append(valueSets, valueSet)

		args = append(args, rec.URI, rec.CID, rec.DID, rec.Collection, rec.JSON)
	}

	var sqlStr string
	switch r.db.Dialect() {
	case database.PostgreSQL:
		sqlStr = fmt.Sprintf(`INSERT INTO record (uri, cid, did, collection, json)
			VALUES %s
			ON CONFLICT(uri) DO UPDATE SET
				cid = EXCLUDED.cid,
				json = EXCLUDED.json,
				indexed_at = NOW()`, strings.Join(valueSets, ", "))
	default:
		sqlStr = fmt.Sprintf(`INSERT INTO record (uri, cid, did, collection, json)
			VALUES %s
			ON CONFLICT(uri) DO UPDATE SET
				cid = excluded.cid,
				json = excluded.json,
				indexed_at = datetime('now')`, strings.Join(valueSets, ", "))
	}

	_, err := tx.ExecContext(ctx, sqlStr, args...)
	return err
}

// GetByURI retrieves a record by its URI.
func (r *RecordsRepository) GetByURI(ctx context.Context, uri string) (*Record, error) {
	sqlStr := fmt.Sprintf("SELECT %s FROM record WHERE uri = %s",
		r.recordColumns(), r.db.Placeholder(1))

	var rec Record
	var indexedAtStr string
	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(uri)},
		&rec.URI, &rec.CID, &rec.DID, &rec.Collection, &rec.JSON, &indexedAtStr, &rec.RKey)
	if err != nil {
		return nil, err
	}

	rec.IndexedAt, _ = time.Parse(time.RFC3339, indexedAtStr)
	return &rec, nil
}

// GetByURIs retrieves multiple records by their URIs.
func (r *RecordsRepository) GetByURIs(ctx context.Context, uris []string) ([]*Record, error) {
	if len(uris) == 0 {
		return nil, nil
	}

	placeholders := r.db.Placeholders(len(uris), 1)
	sqlStr := fmt.Sprintf("SELECT %s FROM record WHERE uri IN (%s)",
		r.recordColumns(), placeholders)

	params := make([]database.Value, len(uris))
	for i, uri := range uris {
		params[i] = database.Text(uri)
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, r.db.ConvertParams(params)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetByCollection retrieves records for a specific collection.
func (r *RecordsRepository) GetByCollection(ctx context.Context, collection string, limit int) ([]*Record, error) {
	return r.GetByCollectionWithKeysetCursor(ctx, collection, limit, "", "")
}

// GetByCollectionWithCursor retrieves records for a specific collection with cursor-based pagination.
// The cursor is the indexed_at timestamp of the last record from the previous page.
// Records are ordered by indexed_at DESC (newest first) for chronological feed display.
func (r *RecordsRepository) GetByCollectionWithCursor(ctx context.Context, collection string, limit int, afterTimestamp string) ([]*Record, error) {
	var sqlStr string
	var args []any

	if afterTimestamp == "" {
		// No cursor - get first page, ordered by indexed_at DESC (newest first)
		sqlStr = fmt.Sprintf("SELECT %s FROM record WHERE collection = %s ORDER BY indexed_at DESC, uri DESC LIMIT %d",
			r.recordColumns(), r.db.Placeholder(1), limit)
		args = []any{collection}
	} else {
		// With cursor - get records older than the cursor timestamp
		// Using indexed_at < cursor for "load more" (older posts)
		sqlStr = fmt.Sprintf("SELECT %s FROM record WHERE collection = %s AND indexed_at < %s ORDER BY indexed_at DESC, uri DESC LIMIT %d",
			r.recordColumns(), r.db.Placeholder(1), r.db.Placeholder(2), limit)
		args = []any{collection, afterTimestamp}
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetByCollectionWithKeysetCursor retrieves records using deterministic keyset pagination.
// The cursor is a composite (indexed_at, uri) pair. Records are ordered by (indexed_at DESC, uri DESC).
// When afterTimestamp and afterURI are provided, returns records that sort after the cursor position.
func (r *RecordsRepository) GetByCollectionWithKeysetCursor(ctx context.Context, collection string, limit int, afterTimestamp, afterURI string) ([]*Record, error) {
	var sqlStr string
	var args []any

	if afterTimestamp == "" && afterURI == "" {
		// No cursor - get first page
		sqlStr = fmt.Sprintf("SELECT %s FROM record WHERE collection = %s ORDER BY indexed_at DESC, uri DESC LIMIT %d",
			r.recordColumns(), r.db.Placeholder(1), limit)
		args = []any{collection}
	} else {
		// Keyset pagination: get records that sort after (afterTimestamp, afterURI)
		// ORDER BY indexed_at DESC, uri DESC means "after" = less than
		sqlStr = fmt.Sprintf("SELECT %s FROM record WHERE collection = %s AND (indexed_at < %s OR (indexed_at = %s AND uri < %s)) ORDER BY indexed_at DESC, uri DESC LIMIT %d",
			r.recordColumns(), r.db.Placeholder(1), r.db.Placeholder(2), r.db.Placeholder(3), r.db.Placeholder(4), limit)
		args = []any{collection, afterTimestamp, afterTimestamp, afterURI}
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// LabelFilter narrows a record query by labels attached to each record.
// Empty slices mean "no filter". Include and Exclude can be combined.
//
// The indexer is deliberately neutral about which labeler is authoritative:
// LabelerSrcs is a list (not a single DID) so that a query can scope
// filtering to a specific subset of labelers, or — when empty — to every
// labeler whose labels have been ingested. This lets the server serve
// labels without editorializing about which labeler is "right"; the
// caller decides their trust set.
type LabelFilter struct {
	// LabelerSrcs restricts the subquery to labels whose `src` is in this
	// list. An empty list means "any labeler" (no src filter).
	LabelerSrcs []string
	// Include: only records that have at least one of these active labels.
	Include []string
	// Exclude: drop records that have any of these active labels.
	Exclude []string
}

// IsEmpty reports whether the filter imposes no constraints.
func (f LabelFilter) IsEmpty() bool {
	return len(f.Include) == 0 && len(f.Exclude) == 0
}

// GetByCollectionWithLabelFilterAndKeysetCursor is the label-aware sibling of
// GetByCollectionWithKeysetCursor. It applies Include/Exclude semantics from
// the label table while preserving the same composite (indexed_at, uri) keyset
// ordering. If filter.IsEmpty() it delegates to the existing method.
func (r *RecordsRepository) GetByCollectionWithLabelFilterAndKeysetCursor(
	ctx context.Context,
	collection string,
	limit int,
	afterTimestamp, afterURI string,
	filter LabelFilter,
) ([]*Record, error) {
	if filter.IsEmpty() {
		return r.GetByCollectionWithKeysetCursor(ctx, collection, limit, afterTimestamp, afterURI)
	}

	var (
		whereClauses []string
		args         []any
	)
	paramIdx := 1
	ph := func() string {
		s := r.db.Placeholder(paramIdx)
		paramIdx++
		return s
	}
	// Postgres stores neg as BOOLEAN; SQLite as INTEGER. Use
	// dialect-correct literals so the query plans correctly on both.
	negFalse, negTrue := "0", "1"
	nowLit := "datetime('now')"
	if r.db.Dialect() == database.PostgreSQL {
		negFalse, negTrue = "false", "true"
		nowLit = "NOW()"
	}

	// collection = ?
	whereClauses = append(whereClauses, fmt.Sprintf("r.collection = %s", ph()))
	args = append(args, collection)

	// keyset cursor
	if afterTimestamp != "" || afterURI != "" {
		p1 := ph()
		p2 := ph()
		p3 := ph()
		whereClauses = append(whereClauses,
			fmt.Sprintf("(r.indexed_at < %s OR (r.indexed_at = %s AND r.uri < %s))", p1, p2, p3))
		args = append(args, afterTimestamp, afterTimestamp, afterURI)
	}

	// labelFilterSub builds an EXISTS / NOT EXISTS subquery for the
	// Include or Exclude set. The placeholders are generated in the
	// same order as they appear in the SQL text so SQLite positional
	// binding and Postgres numeric binding both match up. An empty
	// LabelerSrcs list means "any labeler".
	labelFilterSub := func(vals []string, exists bool) string {
		valPhs := make([]string, len(vals))
		for i, v := range vals {
			valPhs[i] = ph()
			args = append(args, v)
		}
		srcClause := ""
		if len(filter.LabelerSrcs) > 0 {
			srcPhs := make([]string, len(filter.LabelerSrcs))
			for i, s := range filter.LabelerSrcs {
				srcPhs[i] = ph()
				args = append(args, s)
			}
			srcClause = " AND l.src IN (" + strings.Join(srcPhs, ", ") + ")"
		}
		verb := "EXISTS"
		if !exists {
			verb = "NOT EXISTS"
		}
		return fmt.Sprintf(`%s (
			SELECT 1 FROM label l
			WHERE l.uri = r.uri
			  AND l.neg = %s
			  AND (l.exp IS NULL OR l.exp > %s)
			  AND l.val IN (%s)%s
			  AND NOT EXISTS (
			    SELECT 1 FROM label neg
			    WHERE neg.uri = l.uri
			      AND neg.src = l.src
			      AND neg.val = l.val
			      AND neg.neg = %s
			      AND neg.cts >= l.cts
			  )
		)`, verb, negFalse, nowLit, strings.Join(valPhs, ", "), srcClause, negTrue)
	}

	// Include: EXISTS a non-negated label whose val matches one of the
	// Include set (optionally restricted to LabelerSrcs).
	if len(filter.Include) > 0 {
		whereClauses = append(whereClauses, labelFilterSub(filter.Include, true))
	}

	// Exclude: NOT EXISTS an active label whose val matches one of the
	// Exclude set (optionally restricted to LabelerSrcs).
	if len(filter.Exclude) > 0 {
		whereClauses = append(whereClauses, labelFilterSub(filter.Exclude, false))
	}

	// Build columns with "r." prefix.
	cols := r.recordColumns()
	prefixed := make([]string, 0, 8)
	for _, c := range strings.Split(cols, ", ") {
		c = strings.TrimSpace(c)
		// Handle "json::text" and "indexed_at::text" Postgres casts.
		if idx := strings.Index(c, "::"); idx > 0 {
			name := c[:idx]
			cast := c[idx:]
			prefixed = append(prefixed, "r."+name+cast)
		} else {
			prefixed = append(prefixed, "r."+c)
		}
	}
	selectCols := strings.Join(prefixed, ", ")

	sqlStr := fmt.Sprintf(
		"SELECT %s FROM record r WHERE %s ORDER BY r.indexed_at DESC, r.uri DESC LIMIT %d",
		selectCols,
		strings.Join(whereClauses, " AND "),
		limit,
	)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetByDID retrieves all records for a specific DID.
func (r *RecordsRepository) GetByDID(ctx context.Context, did string) ([]*Record, error) {
	sqlStr := fmt.Sprintf("SELECT %s FROM record WHERE did = %s ORDER BY indexed_at DESC",
		r.recordColumns(), r.db.Placeholder(1))

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, did)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// Delete removes a record by URI.
func (r *RecordsRepository) Delete(ctx context.Context, uri string) error {
	sqlStr := fmt.Sprintf("DELETE FROM record WHERE uri = %s", r.db.Placeholder(1))
	_, err := r.db.Exec(ctx, sqlStr, []database.Value{database.Text(uri)})
	return err
}

// DeleteAll removes all records.
func (r *RecordsRepository) DeleteAll(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM record", nil)
	return err
}

// GetCount returns the total number of records.
func (r *RecordsRepository) GetCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM record", nil, &count)
	return count, err
}

// GetCollectionStats returns statistics for all collections.
func (r *RecordsRepository) GetCollectionStats(ctx context.Context) ([]CollectionStat, error) {
	sqlStr := "SELECT collection, COUNT(*) as count FROM record GROUP BY collection ORDER BY count DESC"

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []CollectionStat
	for rows.Next() {
		var stat CollectionStat
		if err := rows.Scan(&stat.Collection, &stat.Count); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}

	return stats, rows.Err()
}

// GetCollectionStatsFiltered returns statistics for specified collections.
// If collections is empty, returns stats for all collections.
func (r *RecordsRepository) GetCollectionStatsFiltered(ctx context.Context, collections []string) ([]CollectionStat, error) {
	if len(collections) == 0 {
		return r.GetCollectionStats(ctx)
	}

	placeholders := r.db.Placeholders(len(collections), 1)
	sqlStr := fmt.Sprintf("SELECT collection, COUNT(*) as count FROM record WHERE collection IN (%s) GROUP BY collection ORDER BY count DESC", placeholders)

	params := make([]database.Value, len(collections))
	for i, c := range collections {
		params[i] = database.Text(c)
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, r.db.ConvertParams(params)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []CollectionStat
	for rows.Next() {
		var stat CollectionStat
		if err := rows.Scan(&stat.Collection, &stat.Count); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}

	return stats, rows.Err()
}

// GetCollectionTimeSeries returns time series data for a collection.
// Records are grouped by date extracted from createdAt, eventDate, or indexed_at.
func (r *RecordsRepository) GetCollectionTimeSeries(ctx context.Context, collection string) (*CollectionTimeSeries, error) {
	var sqlStr string

	switch r.db.Dialect() {
	case database.PostgreSQL:
		// PostgreSQL: Extract date from JSON fields or fall back to indexed_at
		sqlStr = fmt.Sprintf(`
			SELECT 
				DATE(COALESCE(
					(json->>'createdAt')::timestamp,
					(json->>'eventDate')::timestamp,
					indexed_at
				)) as record_date,
				COUNT(*) as count
			FROM record 
			WHERE collection = %s
			GROUP BY record_date
			ORDER BY record_date`, r.db.Placeholder(1))
	default:
		// SQLite: Use json_extract for JSON fields
		sqlStr = fmt.Sprintf(`
			SELECT 
				DATE(COALESCE(
					json_extract(json, '$.createdAt'),
					json_extract(json, '$.eventDate'),
					indexed_at
				)) as record_date,
				COUNT(*) as count
			FROM record 
			WHERE collection = %s
			GROUP BY record_date
			ORDER BY record_date`, r.db.Placeholder(1))
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, collection)
	if err != nil {
		return nil, fmt.Errorf("failed to query time series: %w", err)
	}
	defer rows.Close()

	var data []TimeSeriesDataPoint
	var cumulative int64

	for rows.Next() {
		var date string
		var count int64
		if err := rows.Scan(&date, &count); err != nil {
			return nil, err
		}
		cumulative += count
		data = append(data, TimeSeriesDataPoint{
			Date:       date,
			Count:      count,
			Cumulative: cumulative,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Get total records and unique users
	var totalRecords, uniqueUsers int64
	countSQL := fmt.Sprintf("SELECT COUNT(*), COUNT(DISTINCT did) FROM record WHERE collection = %s", r.db.Placeholder(1))
	if err := r.db.QueryRow(ctx, countSQL, []database.Value{database.Text(collection)}, &totalRecords, &uniqueUsers); err != nil {
		return nil, fmt.Errorf("failed to get collection totals: %w", err)
	}

	return &CollectionTimeSeries{
		Collection:   collection,
		TotalRecords: totalRecords,
		UniqueUsers:  uniqueUsers,
		Data:         data,
	}, nil
}

// GetCIDsByURIs returns a map of URI -> CID for records that exist.
// Used for deduplication before batch insert.
func (r *RecordsRepository) GetCIDsByURIs(ctx context.Context, uris []string) (map[string]string, error) {
	if len(uris) == 0 {
		return make(map[string]string), nil
	}

	result := make(map[string]string)

	// Process in batches of 900 to avoid SQL parameter limits
	batchSize := SQLParamBatchSize
	for i := 0; i < len(uris); i += batchSize {
		end := i + batchSize
		if end > len(uris) {
			end = len(uris)
		}
		batch := uris[i:end]

		placeholders := r.db.Placeholders(len(batch), 1)
		sqlStr := fmt.Sprintf("SELECT uri, cid FROM record WHERE uri IN (%s)", placeholders)

		params := make([]database.Value, len(batch))
		for j, uri := range batch {
			params[j] = database.Text(uri)
		}

		rows, err := r.db.DB().QueryContext(ctx, sqlStr, r.db.ConvertParams(params)...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var uri, cid string
			if err := rows.Scan(&uri, &cid); err != nil {
				rows.Close()
				return nil, err
			}
			result[uri] = cid
		}
		rows.Close()

		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// GetExistingCIDs returns a set of CIDs that already exist in the database.
// Used to detect duplicate content across different URIs.
func (r *RecordsRepository) GetExistingCIDs(ctx context.Context, cids []string) (map[string]bool, error) {
	if len(cids) == 0 {
		return make(map[string]bool), nil
	}

	result := make(map[string]bool)

	// Process in batches of 900 to avoid SQL parameter limits
	batchSize := SQLParamBatchSize
	for i := 0; i < len(cids); i += batchSize {
		end := i + batchSize
		if end > len(cids) {
			end = len(cids)
		}
		batch := cids[i:end]

		placeholders := r.db.Placeholders(len(batch), 1)
		sqlStr := fmt.Sprintf("SELECT cid FROM record WHERE cid IN (%s)", placeholders)

		params := make([]database.Value, len(batch))
		for j, cid := range batch {
			params[j] = database.Text(cid)
		}

		rows, err := r.db.DB().QueryContext(ctx, sqlStr, r.db.ConvertParams(params)...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			result[cid] = true
		}
		rows.Close()

		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// Helper functions

func (r *RecordsRepository) getCIDByURI(ctx context.Context, uri string) (string, error) {
	var cid string
	err := r.db.QueryRow(ctx, fmt.Sprintf("SELECT cid FROM record WHERE uri = %s", r.db.Placeholder(1)),
		[]database.Value{database.Text(uri)}, &cid)
	return cid, err
}

func scanRecords(rows *sql.Rows) ([]*Record, error) {
	var records []*Record
	for rows.Next() {
		var rec Record
		var indexedAtStr string
		if err := rows.Scan(&rec.URI, &rec.CID, &rec.DID, &rec.Collection, &rec.JSON, &indexedAtStr, &rec.RKey); err != nil {
			return nil, err
		}
		// Try various timestamp formats
		rec.IndexedAt = atproto.ParseTimestamp(indexedAtStr)
		records = append(records, &rec)
	}
	return records, rows.Err()
}

// IterateAll calls the provided function for each record in the database.
// Records are processed in batches to manage memory usage.
// Returns the total number of records processed.
func (r *RecordsRepository) IterateAll(ctx context.Context, batchSize int, fn func(*Record) error) (int64, error) {
	if batchSize <= 0 {
		batchSize = DefaultIterateBatchSize
	}

	var totalProcessed int64
	var lastURI string

	for {
		// Fetch next batch ordered by URI (for stable pagination)
		var sqlStr string
		var params []database.Value

		if lastURI == "" {
			sqlStr = fmt.Sprintf("SELECT %s FROM record ORDER BY uri LIMIT %d",
				r.recordColumns(), batchSize)
			params = nil
		} else {
			sqlStr = fmt.Sprintf("SELECT %s FROM record WHERE uri > %s ORDER BY uri LIMIT %d",
				r.recordColumns(), r.db.Placeholder(1), batchSize)
			params = []database.Value{database.Text(lastURI)}
		}

		var args []any
		if params != nil {
			args = r.db.ConvertParams(params)
		}

		rows, err := r.db.DB().QueryContext(ctx, sqlStr, args...)
		if err != nil {
			return totalProcessed, err
		}

		records, err := scanRecords(rows)
		rows.Close()
		if err != nil {
			return totalProcessed, err
		}

		if len(records) == 0 {
			break // No more records
		}

		// Process each record
		for _, rec := range records {
			if err := fn(rec); err != nil {
				return totalProcessed, err
			}
			totalProcessed++
			lastURI = rec.URI
		}

		// If we got fewer records than batch size, we're done
		if len(records) < batchSize {
			break
		}
	}

	return totalProcessed, nil
}
