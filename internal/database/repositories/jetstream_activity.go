package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// ActivityEntry represents a jetstream activity log entry.
type ActivityEntry struct {
	ID           int64
	Timestamp    time.Time
	Operation    string
	Collection   string
	DID          string
	RKey         *string
	Status       string
	ErrorMessage *string
	EventJSON    string
	IsValid      *bool
}

// ValidationStats holds aggregated validation statistics.
type ValidationStats struct {
	InvalidCount        int64
	InvalidByCollection []CollectionValidationCount
	LastInvalidAt       *time.Time
}

// CollectionValidationCount holds a per-collection invalid record count.
type CollectionValidationCount struct {
	Collection string
	Count      int64
}

// ActivityBucket represents aggregated activity data for a time bucket.
type ActivityBucket struct {
	Timestamp time.Time
	Total     int64
	Creates   int64
	Updates   int64
	Deletes   int64
}

// JetstreamActivityRepository handles jetstream activity persistence.
type JetstreamActivityRepository struct {
	db database.Executor
}

// NewJetstreamActivityRepository creates a new jetstream activity repository.
func NewJetstreamActivityRepository(db database.Executor) *JetstreamActivityRepository {
	return &JetstreamActivityRepository{db: db}
}

// LogActivity logs a new activity entry with 'pending' status and
// returns the ID. sourceEventID, when non-nil, dedupes redelivered
// events from the same source (Jetstream time_us / Tap event.id).
// On dedup hit, returns the existing row's ID so the caller's
// subsequent UpdateStatus targets that row instead of orphaning a
// successful redelivery (R1.5 in review-round-1).
func (r *JetstreamActivityRepository) LogActivity(
	ctx context.Context,
	timestamp time.Time,
	operation, collection, did, rkey, eventJSON string,
	sourceEventID *int64,
) (int64, error) {
	return r.LogActivityWithStatus(ctx, timestamp, operation, collection, did, rkey, eventJSON, "pending", sourceEventID)
}

// LogActivityWithStatus logs a new activity entry with a custom
// status and returns the ID. See LogActivity for sourceEventID
// semantics.
func (r *JetstreamActivityRepository) LogActivityWithStatus(
	ctx context.Context,
	timestamp time.Time,
	operation, collection, did, rkey, eventJSON, status string,
	sourceEventID *int64,
) (int64, error) {
	// event_json is a JSONB NOT NULL column. The Jetstream consumer
	// passes string(commit.Record) which is an empty string for delete
	// operations (no record body). Postgres rejects empty strings as
	// invalid JSONB. Normalise here: replace empty or whitespace-only
	// payloads with the JSON literal `null`.
	if strings.TrimSpace(eventJSON) == "" {
		eventJSON = "null"
	}

	// Always store in UTC for consistency.
	timestampStr := timestamp.UTC().Format(time.RFC3339)

	// When sourceEventID is non-nil, use ON CONFLICT DO NOTHING on
	// the partial unique index (migration 028) to swallow redelivered
	// events. RETURNING id from an inserted row is straightforward;
	// the UNION SELECT fallback fetches the existing row's id on
	// conflict so the caller's subsequent UpdateStatus has a valid
	// target — without this, a redelivered event's row stays
	// 'pending' and the orphan janitor eventually marks it
	// 'orphaned' even though processing succeeded the first time
	// (R1.5).
	//
	// NB: $8 (source_event_id) is referenced TWICE — once in the
	// INSERT VALUES, once in the fallback SELECT's WHERE clause.
	// This is intentional; both references bind to the same
	// sourceEventID parameter. Do not "fix" this to $8, $9.
	const sqlInsert = `WITH ins AS (
			INSERT INTO jetstream_activity
				(timestamp, operation, collection, did, rkey, status, event_json, source_event_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (source_event_id)
					WHERE source_event_id IS NOT NULL
					DO NOTHING
			RETURNING id
		)
		SELECT id FROM ins
		UNION ALL
		SELECT id FROM jetstream_activity
			WHERE source_event_id = $8
			AND NOT EXISTS (SELECT 1 FROM ins)
		LIMIT 1`

	params := []database.Value{
		database.Text(timestampStr),
		database.Text(operation),
		database.Text(collection),
		database.Text(did),
		database.Text(rkey),
		database.Text(status),
		database.Text(eventJSON),
		database.NullableInt(sourceEventID),
	}

	var id int64
	err := r.db.QueryRow(ctx, sqlInsert, params, &id)
	return id, err
}

// UpdateStatus updates the status, optional error message, and optional validation result of an activity entry.
func (r *JetstreamActivityRepository) UpdateStatus(
	ctx context.Context,
	id int64,
	status string,
	errorMessage *string,
	isValid *bool,
) error {
	const sqlUpdate = `UPDATE jetstream_activity
		SET status = $1, error_message = $2, is_valid = $3
		WHERE id = $4`

	params := []database.Value{
		database.Text(status),
		database.NullableText(errorMessage),
		database.NullableBool(isValid),
		database.Int(id),
	}

	_, err := r.db.Exec(ctx, sqlUpdate, params)
	return err
}

// GetRecentActivity returns activity entries from the last N hours.
func (r *JetstreamActivityRepository) GetRecentActivity(ctx context.Context, hours int) ([]ActivityEntry, error) {
	sqlStr := fmt.Sprintf(`SELECT id, timestamp, operation, collection, did, rkey, status, error_message, event_json, is_valid
		FROM jetstream_activity
		WHERE timestamp >= NOW() - INTERVAL '%d hours'
		ORDER BY timestamp DESC
		LIMIT 1000`, hours)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanActivityEntries(rows)
}

// CleanupOldActivity deletes activity entries older than the specified hours.
func (r *JetstreamActivityRepository) CleanupOldActivity(ctx context.Context, hours int) error {
	sqlStr := fmt.Sprintf(`DELETE FROM jetstream_activity
		WHERE timestamp < NOW() - INTERVAL '%d hours'`, hours)

	_, err := r.db.Exec(ctx, sqlStr, nil)
	return err
}

// OrphanPendingActivity marks any activity row still in 'pending' state
// after maxAgeMinutes as 'orphaned' with an explanatory error message.
// This closes the window where LogActivity + UpdateStatus are written
// separately: if the process crashes between the two writes, the row
// never leaves pending. The background janitor runs this on a ticker so
// the admin UI's recentActivity view doesn't accumulate zombie rows.
func (r *JetstreamActivityRepository) OrphanPendingActivity(ctx context.Context, maxAgeMinutes int) (int64, error) {
	sqlStr := fmt.Sprintf(`UPDATE jetstream_activity
		SET status = 'orphaned',
		    error_message = 'activity status never updated within %d minutes'
		WHERE status = 'pending'
		  AND timestamp < NOW() - INTERVAL '%d minutes'`, maxAgeMinutes, maxAgeMinutes)
	res, err := r.db.Exec(ctx, sqlStr, nil)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// GetActivityBuckets returns aggregated activity data for the specified time range.
func (r *JetstreamActivityRepository) GetActivityBuckets(ctx context.Context, timeRange string) ([]ActivityBucket, error) {
	var sqlStr string

	switch timeRange {
	case "ONE_HOUR":
		sqlStr = r.buildBucketQuery(1, 5)
	case "THREE_HOURS":
		sqlStr = r.buildBucketQuery(3, 15)
	case "SIX_HOURS":
		sqlStr = r.buildBucketQuery(6, 30)
	case "ONE_DAY":
		sqlStr = r.buildBucketQuery(24, 60)
	case "SEVEN_DAYS":
		sqlStr = r.buildBucketQuery(168, 1440)
	default:
		sqlStr = r.buildBucketQuery(1, 5)
	}

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []ActivityBucket
	for rows.Next() {
		var bucket ActivityBucket
		var timestampStr string

		if err := rows.Scan(&timestampStr, &bucket.Total, &bucket.Creates, &bucket.Updates, &bucket.Deletes); err != nil {
			return nil, err
		}

		bucket.Timestamp, _ = time.Parse(time.RFC3339, timestampStr)
		if bucket.Timestamp.IsZero() {
			bucket.Timestamp, _ = time.Parse("2006-01-02 15:04:05", timestampStr)
		}
		buckets = append(buckets, bucket)
	}

	return buckets, rows.Err()
}

func (r *JetstreamActivityRepository) buildBucketQuery(hours, minutes int) string {
	return fmt.Sprintf(`SELECT
		date_trunc('hour', timestamp) +
			INTERVAL '%d minutes' * FLOOR(EXTRACT(MINUTE FROM timestamp) / %d) as bucket,
		COUNT(*) as total,
		COUNT(*) FILTER (WHERE operation = 'create') as creates,
		COUNT(*) FILTER (WHERE operation = 'update') as updates,
		COUNT(*) FILTER (WHERE operation = 'delete') as deletes
	FROM jetstream_activity
	WHERE timestamp >= NOW() - INTERVAL '%d hours'
	GROUP BY bucket
	ORDER BY bucket ASC`, minutes, minutes, hours)
}

// DeleteAll removes all activity entries.
func (r *JetstreamActivityRepository) DeleteAll(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM jetstream_activity", nil)
	return err
}

// GetCount returns the total number of activity entries.
func (r *JetstreamActivityRepository) GetCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM jetstream_activity", nil, &count)
	return count, err
}

// Helper function to scan activity entries from rows
func scanActivityEntries(rows *sql.Rows) ([]ActivityEntry, error) {
	var entries []ActivityEntry
	for rows.Next() {
		var entry ActivityEntry
		var timestampStr string
		var rkey sql.NullString
		var errorMessage sql.NullString
		var isValid sql.NullBool

		if err := rows.Scan(&entry.ID, &timestampStr, &entry.Operation, &entry.Collection,
			&entry.DID, &rkey, &entry.Status, &errorMessage, &entry.EventJSON, &isValid); err != nil {
			return nil, err
		}

		entry.Timestamp, _ = time.Parse(time.RFC3339, timestampStr)
		if entry.Timestamp.IsZero() {
			entry.Timestamp, _ = time.Parse("2006-01-02 15:04:05", timestampStr)
		}
		if rkey.Valid {
			entry.RKey = &rkey.String
		}
		if errorMessage.Valid {
			entry.ErrorMessage = &errorMessage.String
		}
		if isValid.Valid {
			v := isValid.Bool
			entry.IsValid = &v
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// GetValidationStats returns aggregated validation statistics for the specified time range.
func (r *JetstreamActivityRepository) GetValidationStats(ctx context.Context, timeRange string) (*ValidationStats, error) {
	hours := 24
	switch timeRange {
	case "ONE_HOUR":
		hours = 1
	case "THREE_HOURS":
		hours = 3
	case "SIX_HOURS":
		hours = 6
	case "ONE_DAY":
		hours = 24
	case "SEVEN_DAYS":
		hours = 168
	}

	stats := &ValidationStats{}

	// Get invalid count and last invalid timestamp
	countSQL := fmt.Sprintf(`SELECT COUNT(*), MAX(timestamp)
		FROM jetstream_activity
		WHERE is_valid = false AND timestamp >= NOW() - INTERVAL '%d hours'`, hours)

	var lastInvalidStr sql.NullString
	if err := r.db.DB().QueryRowContext(ctx, countSQL).Scan(&stats.InvalidCount, &lastInvalidStr); err != nil {
		return nil, fmt.Errorf("failed to get invalid count: %w", err)
	}
	if lastInvalidStr.Valid {
		t, _ := time.Parse(time.RFC3339, lastInvalidStr.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", lastInvalidStr.String)
		}
		if !t.IsZero() {
			stats.LastInvalidAt = &t
		}
	}

	// Get invalid count by collection
	byCollSQL := fmt.Sprintf(`SELECT collection, COUNT(*) as cnt
		FROM jetstream_activity
		WHERE is_valid = false AND timestamp >= NOW() - INTERVAL '%d hours'
		GROUP BY collection
		ORDER BY cnt DESC
		LIMIT 10`, hours)

	rows, err := r.db.DB().QueryContext(ctx, byCollSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to get invalid by collection: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var c CollectionValidationCount
		if err := rows.Scan(&c.Collection, &c.Count); err != nil {
			return nil, err
		}
		stats.InvalidByCollection = append(stats.InvalidByCollection, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// GetRecentInvalidActivity returns the most recent invalid activity entries.
func (r *JetstreamActivityRepository) GetRecentInvalidActivity(ctx context.Context, limit int) ([]ActivityEntry, error) {
	sqlStr := fmt.Sprintf(`SELECT id, timestamp, operation, collection, did, rkey, status, error_message, event_json, is_valid
		FROM jetstream_activity
		WHERE is_valid = false
		ORDER BY timestamp DESC
		LIMIT %d`, limit)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanActivityEntries(rows)
}
