package repositories

import (
	"context"
	"database/sql"
	"time"

	"github.com/GainForest/hypergoat/internal/database"
)

// LabelPreference represents a user's preference for a specific
// (labeler, label value) combination. A user can now choose to
// hide a label from labeler A while ignoring the same value from
// labeler B.
type LabelPreference struct {
	DID        string
	Src        string // Labeler DID the preference applies to
	LabelVal   string
	Visibility LabelVisibility
	CreatedAt  time.Time
}

// LabelPreferencesRepository handles label preference persistence.
type LabelPreferencesRepository struct {
	db database.Executor
}

// NewLabelPreferencesRepository creates a new label preferences repository.
func NewLabelPreferencesRepository(db database.Executor) *LabelPreferencesRepository {
	return &LabelPreferencesRepository{db: db}
}

// GetByDID retrieves all label preferences for a user, across every
// labeler.
func (r *LabelPreferencesRepository) GetByDID(ctx context.Context, did string) ([]LabelPreference, error) {
	rows, err := r.db.DB().QueryContext(ctx, `SELECT did, src, label_val, visibility, created_at
		FROM actor_label_preference
		WHERE did = $1
		ORDER BY src, label_val`, did)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLabelPreferences(rows)
}

// Get retrieves a specific (did, src, labelVal) preference.
func (r *LabelPreferencesRepository) Get(ctx context.Context, did, src, labelVal string) (*LabelPreference, error) {
	var pref LabelPreference
	var createdAtStr string

	err := r.db.QueryRow(ctx, `SELECT did, src, label_val, visibility, created_at
		FROM actor_label_preference
		WHERE did = $1 AND src = $2 AND label_val = $3`,
		[]database.Value{database.Text(did), database.Text(src), database.Text(labelVal)},
		&pref.DID, &pref.Src, &pref.LabelVal, &pref.Visibility, &createdAtStr)
	if err != nil {
		return nil, err
	}

	pref.CreatedAt = parseStoredTime(createdAtStr)
	return &pref, nil
}

// Set creates or updates a label preference scoped to a specific labeler.
func (r *LabelPreferencesRepository) Set(ctx context.Context, did, src, labelVal string, visibility LabelVisibility) (*LabelPreference, error) {
	_, err := r.db.Exec(ctx, `INSERT INTO actor_label_preference (did, src, label_val, visibility)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (did, src, label_val) DO UPDATE SET
			visibility = EXCLUDED.visibility,
			created_at = NOW()`,
		[]database.Value{
			database.Text(did),
			database.Text(src),
			database.Text(labelVal),
			database.Text(string(visibility)),
		})
	if err != nil {
		return nil, err
	}

	return r.Get(ctx, did, src, labelVal)
}

// Delete removes a single preference (resetting it to the default
// visibility for that labeler).
func (r *LabelPreferencesRepository) Delete(ctx context.Context, did, src, labelVal string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM actor_label_preference
		WHERE did = $1 AND src = $2 AND label_val = $3`,
		[]database.Value{
			database.Text(did),
			database.Text(src),
			database.Text(labelVal),
		})
	return err
}

// DeleteByDID removes all label preferences for a user.
func (r *LabelPreferencesRepository) DeleteByDID(ctx context.Context, did string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM actor_label_preference WHERE did = $1",
		[]database.Value{database.Text(did)})
	return err
}

// Helper function to scan label preferences from rows
func scanLabelPreferences(rows *sql.Rows) ([]LabelPreference, error) {
	var preferences []LabelPreference
	for rows.Next() {
		var pref LabelPreference
		var createdAtStr string

		if err := rows.Scan(&pref.DID, &pref.Src, &pref.LabelVal, &pref.Visibility, &createdAtStr); err != nil {
			return nil, err
		}

		pref.CreatedAt = parseStoredTime(createdAtStr)
		preferences = append(preferences, pref)
	}

	return preferences, rows.Err()
}
