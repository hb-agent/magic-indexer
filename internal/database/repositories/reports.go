package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// ReportReasonType represents the type of reason for a report.
type ReportReasonType string

const (
	ReasonSpam       ReportReasonType = "spam"
	ReasonViolation  ReportReasonType = "violation"
	ReasonMisleading ReportReasonType = "misleading"
	ReasonSexual     ReportReasonType = "sexual"
	ReasonRude       ReportReasonType = "rude"
	ReasonOther      ReportReasonType = "other"
)

// ReportStatus represents the status of a report.
type ReportStatus string

const (
	StatusPending   ReportStatus = "pending"
	StatusResolved  ReportStatus = "resolved"
	StatusDismissed ReportStatus = "dismissed"
)

// Report represents a user-submitted moderation report.
type Report struct {
	ID          int64
	ReporterDID string
	SubjectURI  string
	ReasonType  ReportReasonType
	Reason      *string
	Status      ReportStatus
	ResolvedBy  *string
	ResolvedAt  *time.Time
	CreatedAt   time.Time
}

// PaginatedReports holds paginated report results.
type PaginatedReports struct {
	Reports     []Report
	HasNextPage bool
	TotalCount  int64
}

// ReportsRepository handles report persistence.
type ReportsRepository struct {
	db database.Executor
}

// NewReportsRepository creates a new reports repository.
func NewReportsRepository(db database.Executor) *ReportsRepository {
	return &ReportsRepository{db: db}
}

// Insert creates a new report.
func (r *ReportsRepository) Insert(ctx context.Context, reporterDID, subjectURI string, reasonType ReportReasonType, reason *string) (*Report, error) {
	const sqlStr = `INSERT INTO report (reporter_did, subject_uri, reason_type, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id, reporter_did, subject_uri, reason_type, reason, status, resolved_by, resolved_at, created_at`

	params := []database.Value{
		database.Text(reporterDID),
		database.Text(subjectURI),
		database.Text(string(reasonType)),
		database.NullableText(reason),
	}

	var report Report
	var reasonNull, resolvedByNull, resolvedAtNull sql.NullString
	var createdAtStr string

	err := r.db.QueryRow(ctx, sqlStr, params,
		&report.ID, &report.ReporterDID, &report.SubjectURI, &report.ReasonType,
		&reasonNull, &report.Status, &resolvedByNull, &resolvedAtNull, &createdAtStr)
	if err != nil {
		return nil, err
	}

	if reasonNull.Valid {
		report.Reason = &reasonNull.String
	}
	if resolvedByNull.Valid {
		report.ResolvedBy = &resolvedByNull.String
	}
	if resolvedAtNull.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAtNull.String)
		report.ResolvedAt = &t
	}
	report.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	return &report, nil
}

// Get retrieves a report by ID.
func (r *ReportsRepository) Get(ctx context.Context, id int64) (*Report, error) {
	const sqlStr = `SELECT id, reporter_did, subject_uri, reason_type, reason, status, resolved_by, resolved_at, created_at
		FROM report WHERE id = $1`

	var report Report
	var reasonNull, resolvedByNull, resolvedAtNull sql.NullString
	var createdAtStr string

	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Int(id)},
		&report.ID, &report.ReporterDID, &report.SubjectURI, &report.ReasonType,
		&reasonNull, &report.Status, &resolvedByNull, &resolvedAtNull, &createdAtStr)
	if err != nil {
		return nil, err
	}

	if reasonNull.Valid {
		report.Reason = &reasonNull.String
	}
	if resolvedByNull.Valid {
		report.ResolvedBy = &resolvedByNull.String
	}
	if resolvedAtNull.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAtNull.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", resolvedAtNull.String)
		}
		report.ResolvedAt = &t
	}
	report.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	if report.CreatedAt.IsZero() {
		report.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
	}

	return &report, nil
}

// GetPaginated retrieves reports with optional status filter and pagination.
func (r *ReportsRepository) GetPaginated(ctx context.Context, statusFilter *ReportStatus, first int, afterID *int64) (*PaginatedReports, error) {
	// Build WHERE clause
	var conditions []string
	var params []any
	paramIdx := 1

	if statusFilter != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", paramIdx))
		params = append(params, string(*statusFilter))
		paramIdx++
	}

	if afterID != nil {
		conditions = append(conditions, fmt.Sprintf("id < $%d", paramIdx))
		params = append(params, *afterID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM report %s", whereClause)
	var totalCount int64
	if err := r.db.DB().QueryRowContext(ctx, countSQL, params...).Scan(&totalCount); err != nil {
		return nil, err
	}

	// Get reports
	sqlStr := fmt.Sprintf(`SELECT id, reporter_did, subject_uri, reason_type, reason, status, resolved_by, resolved_at, created_at
		FROM report %s
		ORDER BY id DESC
		LIMIT %d`, whereClause, first+1)

	rows, err := r.db.DB().QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reports, err := scanReports(rows)
	if err != nil {
		return nil, err
	}

	hasNextPage := len(reports) > first
	if hasNextPage {
		reports = reports[:first]
	}

	return &PaginatedReports{
		Reports:     reports,
		HasNextPage: hasNextPage,
		TotalCount:  totalCount,
	}, nil
}

// Resolve updates a report's status and resolution details.
func (r *ReportsRepository) Resolve(ctx context.Context, id int64, status ReportStatus, resolvedBy string) (*Report, error) {
	const sqlStr = `UPDATE report
		SET status = $1, resolved_by = $2, resolved_at = NOW()
		WHERE id = $3`

	params := []database.Value{
		database.Text(string(status)),
		database.Text(resolvedBy),
		database.Int(id),
	}

	_, err := r.db.Exec(ctx, sqlStr, params)
	if err != nil {
		return nil, err
	}

	return r.Get(ctx, id)
}

// GetByReporterAndSubject retrieves a report by reporter DID and subject URI.
func (r *ReportsRepository) GetByReporterAndSubject(ctx context.Context, reporterDID, subjectURI string) (*Report, error) {
	const sqlStr = `SELECT id, reporter_did, subject_uri, reason_type, reason, status, resolved_by, resolved_at, created_at
		FROM report WHERE reporter_did = $1 AND subject_uri = $2`

	var report Report
	var reasonNull, resolvedByNull, resolvedAtNull sql.NullString
	var createdAtStr string

	err := r.db.QueryRow(ctx, sqlStr, []database.Value{database.Text(reporterDID), database.Text(subjectURI)},
		&report.ID, &report.ReporterDID, &report.SubjectURI, &report.ReasonType,
		&reasonNull, &report.Status, &resolvedByNull, &resolvedAtNull, &createdAtStr)
	if err != nil {
		return nil, err
	}

	if reasonNull.Valid {
		report.Reason = &reasonNull.String
	}
	if resolvedByNull.Valid {
		report.ResolvedBy = &resolvedByNull.String
	}
	if resolvedAtNull.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAtNull.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", resolvedAtNull.String)
		}
		report.ResolvedAt = &t
	}
	report.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	if report.CreatedAt.IsZero() {
		report.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
	}

	return &report, nil
}

// DeleteAll removes all reports.
func (r *ReportsRepository) DeleteAll(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM report", nil)
	return err
}

// ValidateReasonType validates a reason type value.
func ValidateReasonType(reasonType string) (ReportReasonType, error) {
	switch reasonType {
	case "spam":
		return ReasonSpam, nil
	case "violation":
		return ReasonViolation, nil
	case "misleading":
		return ReasonMisleading, nil
	case "sexual":
		return ReasonSexual, nil
	case "rude":
		return ReasonRude, nil
	case "other":
		return ReasonOther, nil
	default:
		return "", fmt.Errorf("invalid reason type: %s", reasonType)
	}
}

// ValidateReportStatus validates a report status value.
func ValidateReportStatus(status string) (ReportStatus, error) {
	switch status {
	case "pending":
		return StatusPending, nil
	case "resolved":
		return StatusResolved, nil
	case "dismissed":
		return StatusDismissed, nil
	default:
		return "", fmt.Errorf("invalid report status: %s", status)
	}
}

// Helper function to scan reports from rows
func scanReports(rows *sql.Rows) ([]Report, error) {
	var reports []Report
	for rows.Next() {
		var report Report
		var reasonNull, resolvedByNull, resolvedAtNull sql.NullString
		var createdAtStr string

		if err := rows.Scan(&report.ID, &report.ReporterDID, &report.SubjectURI, &report.ReasonType,
			&reasonNull, &report.Status, &resolvedByNull, &resolvedAtNull, &createdAtStr); err != nil {
			return nil, err
		}

		if reasonNull.Valid {
			report.Reason = &reasonNull.String
		}
		if resolvedByNull.Valid {
			report.ResolvedBy = &resolvedByNull.String
		}
		if resolvedAtNull.Valid {
			t, _ := time.Parse(time.RFC3339, resolvedAtNull.String)
			if t.IsZero() {
				t, _ = time.Parse("2006-01-02 15:04:05", resolvedAtNull.String)
			}
			report.ResolvedAt = &t
		}
		report.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		if report.CreatedAt.IsZero() {
			report.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		}
		reports = append(reports, report)
	}

	return reports, rows.Err()
}
