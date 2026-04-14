package notifications

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// Row is a notification row returned from List.
type Row struct {
	ID              int64
	DID             string
	Reason          string
	ReasonSubject   string
	SortAt          time.Time
	Count           int
	LatestRecordURI string
	LatestRecordCID string
	LatestAuthor    string
}

// ListResult returned from List, with stable cursor to the next page.
type ListResult struct {
	Rows           []Row
	NextCursor     string
	LastSeenNotifs time.Time // for computing isRead on each row at caller
}

// Repository is the data-layer for notifications. Methods accept an Executor so
// they can operate with or without a surrounding transaction.
type Repository struct {
	db database.Executor
}

// NewRepository creates a new Repository.
func NewRepository(db database.Executor) *Repository {
	return &Repository{db: db}
}

// Apply inserts or updates notifications idempotently. Safe to call with the
// same notification set repeatedly — the UNIQUE (record_uri, recipient_did)
// constraint on notification_participant is the idempotency boundary.
func (r *Repository) Apply(ctx context.Context, notifs []Notification) error {
	for _, n := range notifs {
		if err := r.applyOne(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) applyOne(ctx context.Context, n Notification) error {
	conn := r.db.DB()

	var notificationID int64
	if n.GroupKey != "" {
		// Aggregated: upsert the envelope, returning existing or new id.
		// ON CONFLICT triggers on the partial unique index (did, group_key)
		// where group_key is not null — no WHERE clause on ON CONFLICT itself.
		err := conn.QueryRowContext(ctx, `
			INSERT INTO notification (did, reason, reason_subject, group_key, sort_at,
			                          count, latest_record_uri, latest_record_cid, latest_author)
			VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8)
			ON CONFLICT (did, group_key) DO UPDATE SET sort_at = notification.sort_at
			RETURNING id
		`, n.Recipient, n.Reason, nullIfEmpty(n.ReasonSubject), n.GroupKey, n.SortAt,
			n.RecordURI, n.RecordCID, n.Author).Scan(&notificationID)
		if err != nil {
			return fmt.Errorf("upsert envelope (aggregated): %w", err)
		}
	} else {
		// Non-aggregated: plain insert of a new envelope.
		err := conn.QueryRowContext(ctx, `
			INSERT INTO notification (did, reason, reason_subject, group_key, sort_at,
			                          count, latest_record_uri, latest_record_cid, latest_author)
			VALUES ($1, $2, $3, NULL, $4, 0, $5, $6, $7)
			RETURNING id
		`, n.Recipient, n.Reason, nullIfEmpty(n.ReasonSubject), n.SortAt,
			n.RecordURI, n.RecordCID, n.Author).Scan(&notificationID)
		if err != nil {
			return fmt.Errorf("insert envelope (non-aggregated): %w", err)
		}
	}

	// Insert participant. ON CONFLICT DO NOTHING makes replay a no-op.
	res, err := conn.ExecContext(ctx, `
		INSERT INTO notification_participant
			(notification_id, record_uri, record_cid, recipient_did, author, sort_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (record_uri, recipient_did) DO NOTHING
	`, notificationID, n.RecordURI, n.RecordCID, n.Recipient, n.Author, n.SortAt)
	if err != nil {
		return fmt.Errorf("insert participant: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Replay of an existing (record_uri, recipient_did). Undo the
		// non-aggregated envelope we just inserted. For aggregated, the
		// upsert was a no-op on an existing row (count unchanged).
		if n.GroupKey == "" {
			_, _ = conn.ExecContext(ctx,
				`DELETE FROM notification WHERE id = $1 AND count = 0`, notificationID)
		}
		return nil
	}

	// Bump envelope count, update latest_* if the new participant is the newest.
	_, err = conn.ExecContext(ctx, `
		UPDATE notification
		SET count             = count + 1,
		    sort_at           = GREATEST(sort_at, $1),
		    latest_record_uri = CASE WHEN $1 >= sort_at THEN $2 ELSE latest_record_uri END,
		    latest_record_cid = CASE WHEN $1 >= sort_at THEN $3 ELSE latest_record_cid END,
		    latest_author     = CASE WHEN $1 >= sort_at THEN $4 ELSE latest_author END
		WHERE id = $5
	`, n.SortAt, n.RecordURI, n.RecordCID, n.Author, notificationID)
	if err != nil {
		return fmt.Errorf("update envelope count: %w", err)
	}
	return nil
}

// DeleteByRecordURI removes participants for a deleted record, decrements the
// envelope count, deletes the envelope when count hits zero, and recomputes
// latest_* fields from remaining participants when the deleted one was the latest.
func (r *Repository) DeleteByRecordURI(ctx context.Context, uri string) error {
	conn := r.db.DB()

	rows, err := conn.QueryContext(ctx, `
		DELETE FROM notification_participant
		WHERE record_uri = $1
		RETURNING notification_id
	`, uri)
	if err != nil {
		return fmt.Errorf("delete participants: %w", err)
	}
	defer rows.Close()

	var notificationIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		notificationIDs = append(notificationIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range notificationIDs {
		if _, err := conn.ExecContext(ctx,
			`UPDATE notification SET count = count - 1 WHERE id = $1`, id); err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx,
			`DELETE FROM notification WHERE id = $1 AND count <= 0`, id)
		if err != nil {
			return err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			continue
		}
		// Envelope survives. Recompute latest_* from remaining participants.
		_, err = conn.ExecContext(ctx, `
			UPDATE notification n
			SET latest_record_uri = p.record_uri,
			    latest_record_cid = p.record_cid,
			    latest_author     = p.author,
			    sort_at           = p.sort_at
			FROM (
				SELECT record_uri, record_cid, author, sort_at
				FROM notification_participant
				WHERE notification_id = $1
				ORDER BY sort_at DESC, id DESC
				LIMIT 1
			) p
			WHERE n.id = $1
		`, id)
		if err != nil {
			return err
		}
	}
	return nil
}

// Cursor encodes/decodes the V2 cursor format for notifications:
// base64-URL of JSON ["v1:notif", sort_at_iso, id].
const cursorVersion = "v1:notif"

func encodeCursor(sortAt time.Time, id int64) string {
	arr := []interface{}{cursorVersion, sortAt.Format(time.RFC3339Nano), fmt.Sprintf("%d", id)}
	b, _ := json.Marshal(arr)
	return base64.URLEncoding.EncodeToString(b)
}

func decodeCursor(cursor string) (time.Time, int64, error) {
	if cursor == "" {
		return time.Time{}, 0, nil
	}
	data, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil || len(arr) != 3 {
		return time.Time{}, 0, fmt.Errorf("malformed cursor")
	}
	if arr[0] != cursorVersion {
		return time.Time{}, 0, fmt.Errorf("unsupported cursor version: %q", arr[0])
	}
	sortAt, err := time.Parse(time.RFC3339Nano, arr[1])
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor sort_at: %w", err)
	}
	var id int64
	if _, err := fmt.Sscanf(arr[2], "%d", &id); err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor id: %w", err)
	}
	return sortAt, id, nil
}

// List returns a page of notifications for a user, plus the watermark for
// computing isRead. reasons is optional; pass nil for no filter.
func (r *Repository) List(ctx context.Context, did string, reasons []string, first int, after string) (ListResult, error) {
	conn := r.db.DB()
	if first <= 0 || first > 100 {
		first = 50
	}

	cursorSortAt, cursorID, err := decodeCursor(after)
	if err != nil {
		return ListResult{}, err
	}

	// Load watermark once for isRead computation on all rows.
	var lastSeen sql.NullTime
	err = conn.QueryRowContext(ctx,
		`SELECT last_seen_notifs FROM actor_state WHERE did = $1`, did).Scan(&lastSeen)
	if err != nil && err != sql.ErrNoRows {
		return ListResult{}, fmt.Errorf("load actor_state: %w", err)
	}
	watermark := time.Time{}
	if lastSeen.Valid {
		watermark = lastSeen.Time
	}

	// Build the query conditionally.
	args := []any{did}
	q := `SELECT id, did, reason, COALESCE(reason_subject, ''), sort_at, count,
	             latest_record_uri, latest_record_cid, latest_author
	      FROM notification
	      WHERE did = $1`
	if len(reasons) > 0 {
		q += fmt.Sprintf(" AND reason = ANY($%d)", len(args)+1)
		args = append(args, reasons)
	}
	if !cursorSortAt.IsZero() {
		p1 := len(args) + 1
		p2 := len(args) + 2
		p3 := len(args) + 3
		q += fmt.Sprintf(" AND (sort_at < $%d OR (sort_at = $%d AND id < $%d))", p1, p2, p3)
		args = append(args, cursorSortAt, cursorSortAt, cursorID)
	}
	q += fmt.Sprintf(" ORDER BY sort_at DESC, id DESC LIMIT $%d", len(args)+1)
	args = append(args, first+1)

	dbRows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list notifications: %w", err)
	}
	defer dbRows.Close()

	var result []Row
	for dbRows.Next() {
		var row Row
		if err := dbRows.Scan(&row.ID, &row.DID, &row.Reason, &row.ReasonSubject,
			&row.SortAt, &row.Count, &row.LatestRecordURI, &row.LatestRecordCID, &row.LatestAuthor); err != nil {
			return ListResult{}, err
		}
		result = append(result, row)
	}
	if err := dbRows.Err(); err != nil {
		return ListResult{}, err
	}

	var nextCursor string
	if len(result) > first {
		result = result[:first]
		last := result[len(result)-1]
		nextCursor = encodeCursor(last.SortAt, last.ID)
	}

	return ListResult{Rows: result, NextCursor: nextCursor, LastSeenNotifs: watermark}, nil
}

// UnreadCount returns the number of unread notifications, capped at
// UnreadCountCap+1 to signal "more than cap."
func (r *Repository) UnreadCount(ctx context.Context, did string) (count int, more bool, err error) {
	conn := r.db.DB()
	var c int
	err = conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM notification
		  WHERE did = $1
		    AND sort_at > COALESCE((SELECT last_seen_notifs FROM actor_state WHERE did = $1), 'epoch')
		  LIMIT $2
		) t
	`, did, UnreadCountCap+1).Scan(&c)
	if err != nil {
		return 0, false, fmt.Errorf("unread count: %w", err)
	}
	if c > UnreadCountCap {
		return UnreadCountCap, true, nil
	}
	return c, false, nil
}

// UpdateSeen sets the seen watermark for a user. seenAt is clamped to now().
// Monotonic: never backdates an existing watermark.
func (r *Repository) UpdateSeen(ctx context.Context, did string, seenAt time.Time) error {
	now := time.Now()
	if seenAt.IsZero() || seenAt.After(now) {
		seenAt = now
	}
	conn := r.db.DB()
	_, err := conn.ExecContext(ctx, `
		INSERT INTO actor_state (did, last_seen_notifs) VALUES ($1, $2)
		ON CONFLICT (did) DO UPDATE
		SET last_seen_notifs = GREATEST(actor_state.last_seen_notifs, EXCLUDED.last_seen_notifs)
	`, did, seenAt)
	if err != nil {
		return fmt.Errorf("update seen: %w", err)
	}
	return nil
}

// nullIfEmpty converts "" to SQL NULL for the reason_subject column.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// DeleteOldNotifications deletes notifications older than the cutoff in batches.
// Used by the retention worker. Returns true when there may be more to delete.
func (r *Repository) DeleteOldNotifications(ctx context.Context, olderThan time.Time, batchSize int) (bool, error) {
	conn := r.db.DB()
	res, err := conn.ExecContext(ctx, `
		DELETE FROM notification
		WHERE id IN (
		  SELECT id FROM notification
		  WHERE sort_at < $1
		  ORDER BY id
		  LIMIT $2
		)
	`, olderThan, batchSize)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected >= int64(batchSize), nil
}

// TryAdvisoryLock attempts to acquire a session-scoped advisory lock.
// Returns (true, unlockFn) when acquired, (false, nop) otherwise.
func (r *Repository) TryAdvisoryLock(ctx context.Context, key int64) (bool, func(), error) {
	conn := r.db.DB()
	var locked bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		return false, nil, err
	}
	if !locked {
		return false, func() {}, nil
	}
	unlock := func() {
		_, _ = conn.ExecContext(context.Background(),
			`SELECT pg_advisory_unlock($1)`, key)
	}
	return true, unlock, nil
}

