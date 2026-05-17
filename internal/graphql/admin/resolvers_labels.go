package admin

// Admin resolvers for the labels and reports surface: label
// definitions, viewer preferences, label / report listings, label
// CRUD, and report resolution.
// Extracted from resolvers.go in 2026-05-17 Track 5; see
// docs/review-2026-05-17/plan.md for the partition rationale.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
)

// LabelDefinitions returns all label definitions. Each entry now
// includes the owning labeler's `src` DID (the pre-seeded Bluesky
// defaults live under repositories.SystemLabelerSrc).
func (r *Resolver) LabelDefinitions(ctx context.Context) ([]map[string]interface{}, error) {
	defs, err := r.repos.LabelDefinitions.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get label definitions: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		result = append(result, map[string]interface{}{
			"src":               def.Src,
			"val":               def.Val,
			"description":       def.Description,
			"severity":          string(def.Severity),
			"defaultVisibility": string(def.DefaultVisibility),
			"createdAt":         def.CreatedAt.Format(time.RFC3339),
		})
	}

	return result, nil
}

// ViewerLabelPreferences returns the current user's label
// preferences, joined against the non-system label definitions. A
// user can override visibility independently per (labeler, val) tuple.
func (r *Resolver) ViewerLabelPreferences(ctx context.Context, userDID string) ([]map[string]interface{}, error) {
	// Get all non-system label definitions (both per-labeler and
	// legacy rows that don't belong to an external labeler).
	defs, err := r.repos.LabelDefinitions.GetNonSystem(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get label definitions: %w", err)
	}

	// Get user preferences across every labeler.
	prefs, err := r.repos.LabelPreferences.GetByDID(ctx, userDID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user preferences: %w", err)
	}

	// Build preference map keyed by (src, val) for quick lookup.
	type prefKey struct{ src, val string }
	prefMap := make(map[prefKey]repositories.LabelVisibility)
	for _, pref := range prefs {
		prefMap[prefKey{pref.Src, pref.LabelVal}] = pref.Visibility
	}

	// Build result with effective visibility
	result := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		visibility := def.DefaultVisibility
		if userVis, ok := prefMap[prefKey{def.Src, def.Val}]; ok {
			visibility = userVis
		}

		result = append(result, map[string]interface{}{
			"src":               def.Src,
			"val":               def.Val,
			"description":       def.Description,
			"severity":          string(def.Severity),
			"defaultVisibility": string(def.DefaultVisibility),
			"visibility":        string(visibility),
		})
	}

	return result, nil
}

// Labels returns labels with optional filters and pagination.
func (r *Resolver) Labels(ctx context.Context, uriFilter, valFilter *string, first int, after *string) (map[string]interface{}, error) {
	first = clampAdminPageSize(first)

	// Decode cursor to get afterID
	var afterID *int64
	if after != nil && *after != "" {
		decoded, err := base64.URLEncoding.DecodeString(*after)
		if err == nil {
			if id, err := strconv.ParseInt(string(decoded), 10, 64); err == nil {
				afterID = &id
			}
		}
	}

	paginated, err := r.repos.Labels.GetPaginated(ctx, uriFilter, valFilter, first, afterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}

	edges := make([]map[string]interface{}, 0, len(paginated.Labels))
	var startCursor, endCursor string

	for _, label := range paginated.Labels {
		cursor := base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(label.ID, 10)))
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		node := map[string]interface{}{
			"id":  label.ID,
			"src": label.Src,
			"uri": label.URI,
			"val": label.Val,
			"neg": label.Neg,
			"cts": label.Cts.Format(time.RFC3339),
		}
		if label.CID != nil {
			node["cid"] = *label.CID
		}
		if label.Exp != nil {
			node["exp"] = label.Exp.Format(time.RFC3339)
		}

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     paginated.HasNextPage,
			"hasPreviousPage": after != nil && *after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
		"totalCount": paginated.TotalCount,
	}, nil
}

// Reports returns reports with optional status filter and pagination.
func (r *Resolver) Reports(ctx context.Context, statusFilter *string, first int, after *string) (map[string]interface{}, error) {
	first = clampAdminPageSize(first)

	// Convert status filter
	var status *repositories.ReportStatus
	if statusFilter != nil {
		s := repositories.ReportStatus(*statusFilter)
		status = &s
	}

	// Decode cursor to get afterID
	var afterID *int64
	if after != nil && *after != "" {
		decoded, err := base64.URLEncoding.DecodeString(*after)
		if err == nil {
			if id, err := strconv.ParseInt(string(decoded), 10, 64); err == nil {
				afterID = &id
			}
		}
	}

	paginated, err := r.repos.Reports.GetPaginated(ctx, status, first, afterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get reports: %w", err)
	}

	edges := make([]map[string]interface{}, 0, len(paginated.Reports))
	var startCursor, endCursor string

	for _, report := range paginated.Reports {
		cursor := base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(report.ID, 10)))
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		node := map[string]interface{}{
			"id":          report.ID,
			"reporterDid": report.ReporterDID,
			"subjectUri":  report.SubjectURI,
			"reasonType":  string(report.ReasonType),
			"status":      string(report.Status),
			"createdAt":   report.CreatedAt.Format(time.RFC3339),
		}
		if report.Reason != nil {
			node["reason"] = *report.Reason
		}
		if report.ResolvedBy != nil {
			node["resolvedBy"] = *report.ResolvedBy
		}
		if report.ResolvedAt != nil {
			node["resolvedAt"] = report.ResolvedAt.Format(time.RFC3339)
		}

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     paginated.HasNextPage,
			"hasPreviousPage": after != nil && *after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
		"totalCount": paginated.TotalCount,
	}, nil
}

// =============================================================================
// Mutation Resolvers
// =============================================================================

// UpdateSettings updates system settings. Every applied change
// emits a structured audit log line (`event=admin_settings_changed
// field=<name> before=… after=… actor_did=…`) and increments
// hypergoat_admin_settings_changed_total{field=<name>}. Both `before`
// and `after` go through logsafe.String — they are operator-supplied
// URLs and free-form strings that must not forge log lines.
//
// Only fields actually applied (after validation) are audited; a
// validation rejection returns early and the metric stays flat.

// CreateLabel creates a new label on a record or account. The admin
// creates labels under this server's domain DID, so we check the
// label definition under that same src — a label value defined
// elsewhere by a remote labeler doesn't authorise this server to
// emit it.
func (r *Resolver) CreateLabel(ctx context.Context, uri, val string, cid, exp *string) (map[string]interface{}, error) {
	// Validate URI format
	if !repositories.IsValidSubjectURI(uri) {
		return nil, fmt.Errorf("invalid subject URI: must start with 'at://' or 'did:'")
	}

	// Validate label value is defined for this server's labeler src.
	// Pre-seeded Bluesky values live under SystemLabelerSrc, so we
	// also accept those as a fallback for the built-in takedown
	// vocabulary.
	exists, err := r.repos.LabelDefinitions.Exists(ctx, r.domainDID, val)
	if err != nil {
		return nil, fmt.Errorf("failed to check label definition: %w", err)
	}
	if !exists {
		// Fallback: accept pre-seeded system labels like !takedown
		// without requiring the admin to pre-create them under the
		// domain DID.
		systemExists, err := r.repos.LabelDefinitions.Exists(ctx, repositories.SystemLabelerSrc, val)
		if err != nil {
			return nil, fmt.Errorf("failed to check label definition: %w", err)
		}
		if !systemExists {
			return nil, fmt.Errorf("label value '%s' not defined for this labeler", val)
		}
	}

	// Parse expiration if provided
	var expTime *time.Time
	if exp != nil {
		t, err := time.Parse(time.RFC3339, *exp)
		if err != nil {
			return nil, fmt.Errorf("invalid expiration format: %w", err)
		}
		expTime = &t
	}

	label, err := r.repos.Labels.Insert(ctx, r.domainDID, uri, cid, val, nil, expTime)
	if err != nil {
		return nil, fmt.Errorf("failed to create label: %w", err)
	}

	result := map[string]interface{}{
		"id":  label.ID,
		"src": label.Src,
		"uri": label.URI,
		"val": label.Val,
		"neg": label.Neg,
		"cts": label.Cts.Format(time.RFC3339),
	}
	if label.CID != nil {
		result["cid"] = *label.CID
	}
	if label.Exp != nil {
		result["exp"] = label.Exp.Format(time.RFC3339)
	}

	return result, nil
}

// NegateLabel retracts a label from a record or account.
func (r *Resolver) NegateLabel(ctx context.Context, uri, val string) (map[string]interface{}, error) {
	// Validate URI format
	if !repositories.IsValidSubjectURI(uri) {
		return nil, fmt.Errorf("invalid subject URI: must start with 'at://' or 'did:'")
	}

	label, err := r.repos.Labels.InsertNegation(ctx, r.domainDID, uri, val, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to negate label: %w", err)
	}

	return map[string]interface{}{
		"id":  label.ID,
		"src": label.Src,
		"uri": label.URI,
		"val": label.Val,
		"neg": label.Neg,
		"cts": label.Cts.Format(time.RFC3339),
	}, nil
}

// CreateLabelDefinition creates a new label definition under this
// server's labeler (r.domainDID). Admins can still seed globally-
// scoped system labels by passing src = SystemLabelerSrc explicitly
// via the admin GraphQL mutation — see admin/schema.go.
func (r *Resolver) CreateLabelDefinition(ctx context.Context, src, val, description, severity string, defaultVisibility *string) (map[string]interface{}, error) {
	if src == "" {
		src = r.domainDID
	}

	// Bound the stringy fields so the admin API can't blow up the DB
	// with multi-megabyte values. The wire-side labeler ingest path
	// already caps these via labeler.MaxLabelValLen et al; mirror
	// those limits here.
	if val == "" {
		return nil, fmt.Errorf("val is required")
	}
	if len(val) > 128 {
		return nil, fmt.Errorf("val must be at most 128 bytes")
	}
	if len(description) > 1024 {
		return nil, fmt.Errorf("description must be at most 1024 bytes")
	}
	if len(src) > 512 {
		return nil, fmt.Errorf("src must be at most 512 bytes")
	}

	// Validate severity
	sev, err := repositories.ValidateSeverity(severity)
	if err != nil {
		return nil, err
	}

	// Default visibility
	vis := repositories.VisibilityWarn
	if defaultVisibility != nil {
		vis, err = repositories.ValidateVisibility(*defaultVisibility)
		if err != nil {
			return nil, err
		}
	}

	// Check if already exists for this labeler
	exists, err := r.repos.LabelDefinitions.Exists(ctx, src, val)
	if err != nil {
		return nil, fmt.Errorf("failed to check label definition: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("label '%s' already exists for labeler %s", val, src)
	}

	if err := r.repos.LabelDefinitions.Insert(ctx, src, val, description, sev, vis); err != nil {
		return nil, fmt.Errorf("failed to create label definition: %w", err)
	}

	def, err := r.repos.LabelDefinitions.Get(ctx, src, val)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve created definition: %w", err)
	}

	return map[string]interface{}{
		"src":               def.Src,
		"val":               def.Val,
		"description":       def.Description,
		"severity":          string(def.Severity),
		"defaultVisibility": string(def.DefaultVisibility),
		"createdAt":         def.CreatedAt.Format(time.RFC3339),
	}, nil
}

// ResolveReport resolves a moderation report.
func (r *Resolver) ResolveReport(ctx context.Context, id int64, action string, labelVal *string, resolverDID string) (map[string]interface{}, error) {
	// Get the report
	report, err := r.repos.Reports.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("report not found")
		}
		return nil, fmt.Errorf("failed to get report: %w", err)
	}

	var status repositories.ReportStatus
	switch action {
	case "apply_label":
		if labelVal == nil {
			return nil, fmt.Errorf("labelVal required for apply_label action")
		}
		// Apply the label
		_, err := r.repos.Labels.Insert(ctx, r.domainDID, report.SubjectURI, nil, *labelVal, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to apply label: %w", err)
		}
		status = repositories.StatusResolved
	case "dismiss":
		status = repositories.StatusDismissed
	default:
		return nil, fmt.Errorf("invalid action: %s", action)
	}

	// Update report status
	updatedReport, err := r.repos.Reports.Resolve(ctx, id, status, resolverDID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve report: %w", err)
	}

	result := map[string]interface{}{
		"id":          updatedReport.ID,
		"reporterDid": updatedReport.ReporterDID,
		"subjectUri":  updatedReport.SubjectURI,
		"reasonType":  string(updatedReport.ReasonType),
		"status":      string(updatedReport.Status),
		"createdAt":   updatedReport.CreatedAt.Format(time.RFC3339),
	}
	if updatedReport.Reason != nil {
		result["reason"] = *updatedReport.Reason
	}
	if updatedReport.ResolvedBy != nil {
		result["resolvedBy"] = *updatedReport.ResolvedBy
	}
	if updatedReport.ResolvedAt != nil {
		result["resolvedAt"] = updatedReport.ResolvedAt.Format(time.RFC3339)
	}

	return result, nil
}
