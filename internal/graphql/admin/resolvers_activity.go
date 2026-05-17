package admin

// Admin resolvers for activity / dashboard surface.
// Extracted from resolvers.go in 2026-05-17 Track 5; see
// docs/review-2026-05-17/plan.md for the partition rationale and
// the call-site dependency analysis from plan-review round 1.

import (
	"context"
	"fmt"
	"time"

	"github.com/GainForest/hypergoat/internal/atproto"
	"github.com/GainForest/hypergoat/internal/database/repositories"
)

// ActivityBuckets returns aggregated activity data for the specified time range.
func (r *Resolver) ActivityBuckets(ctx context.Context, timeRange string) ([]map[string]interface{}, error) {
	buckets, err := r.repos.Activity.GetActivityBuckets(ctx, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to get activity buckets: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, map[string]interface{}{
			"timestamp": bucket.Timestamp.Format(time.RFC3339),
			"total":     bucket.Total,
			"creates":   bucket.Creates,
			"updates":   bucket.Updates,
			"deletes":   bucket.Deletes,
		})
	}

	return result, nil
}

// CollectionOverview returns per-collection record counts with invalid counts.
func (r *Resolver) CollectionOverview(ctx context.Context) ([]map[string]interface{}, error) {
	overview, err := r.repos.Records.GetCollectionOverview(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get collection overview: %w", err)
	}
	result := make([]map[string]interface{}, 0, len(overview))
	for _, c := range overview {
		result = append(result, map[string]interface{}{
			"collection":   c.Collection,
			"recordCount":  c.RecordCount,
			"invalidCount": c.InvalidCount,
		})
	}
	return result, nil
}

// RecentActivity returns recent activity entries.
func (r *Resolver) RecentActivity(ctx context.Context, hours int) ([]map[string]interface{}, error) {
	entries, err := r.repos.Activity.GetRecentActivity(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent activity: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		item := map[string]interface{}{
			"id":         entry.ID,
			"timestamp":  entry.Timestamp.Format(time.RFC3339),
			"operation":  entry.Operation,
			"collection": entry.Collection,
			"did":        entry.DID,
			"status":     entry.Status,
			"eventJson":  entry.EventJSON,
		}
		if entry.RKey != nil {
			item["rkey"] = *entry.RKey
		}
		if entry.ErrorMessage != nil {
			item["errorMessage"] = *entry.ErrorMessage
		}
		if entry.IsValid != nil {
			item["isValid"] = *entry.IsValid
		}
		result = append(result, item)
	}

	return result, nil
}

// ValidationStats returns aggregated validation statistics for the specified time range.
func (r *Resolver) ValidationStats(ctx context.Context, timeRange string) (map[string]interface{}, error) {
	stats, err := r.repos.Activity.GetValidationStats(ctx, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to get validation stats: %w", err)
	}

	// Get recent invalid entries
	recentInvalid, err := r.repos.Activity.GetRecentInvalidActivity(ctx, 20)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent invalid activity: %w", err)
	}

	// Map recent invalid to GraphQL format
	recentItems := make([]map[string]interface{}, 0, len(recentInvalid))
	for _, entry := range recentInvalid {
		item := map[string]interface{}{
			"id":         entry.ID,
			"timestamp":  entry.Timestamp.Format(time.RFC3339),
			"operation":  entry.Operation,
			"collection": entry.Collection,
			"did":        entry.DID,
			"status":     entry.Status,
			"eventJson":  entry.EventJSON,
		}
		if entry.RKey != nil {
			item["rkey"] = *entry.RKey
		}
		if entry.ErrorMessage != nil {
			item["errorMessage"] = *entry.ErrorMessage
		}
		if entry.IsValid != nil {
			item["isValid"] = *entry.IsValid
		}
		recentItems = append(recentItems, item)
	}

	// Map invalidByCollection
	byCollection := make([]map[string]interface{}, 0, len(stats.InvalidByCollection))
	for _, c := range stats.InvalidByCollection {
		byCollection = append(byCollection, map[string]interface{}{
			"collection": c.Collection,
			"count":      c.Count,
		})
	}

	result := map[string]interface{}{
		"invalidCount":        stats.InvalidCount,
		"invalidByCollection": byCollection,
		"recentInvalid":       recentItems,
	}
	if stats.LastInvalidAt != nil {
		result["lastInvalidAt"] = stats.LastInvalidAt.Format(time.RFC3339)
	}

	return result, nil
}

// PopulateActivity creates activity entries from existing records in the database.
// This is useful after a backfill to populate the activity dashboard with historical data.
func (r *Resolver) PopulateActivity(ctx context.Context) (int64, error) {
	// First clear existing activity to avoid duplicates
	if err := r.repos.Activity.DeleteAll(ctx); err != nil {
		return 0, fmt.Errorf("failed to clear existing activity: %w", err)
	}

	var count int64
	_, err := r.repos.Records.IterateAll(ctx, 1000, func(rec *repositories.Record) error {
		// Extract createdAt from the record JSON
		timestamp := atproto.ExtractCreatedAt(rec.JSON, time.Now())

		// Log as a successful create operation
		if _, logErr := r.repos.Activity.LogActivityWithStatus(ctx, timestamp, "create", rec.Collection, rec.DID, rec.RKey, rec.JSON, "success"); logErr == nil {
			count++
		}
		return nil
	})

	if err != nil {
		return count, fmt.Errorf("error iterating records: %w", err)
	}

	return count, nil
}
