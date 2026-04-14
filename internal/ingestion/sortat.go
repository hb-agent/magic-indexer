package ingestion

import (
	"encoding/json"
	"time"
)

// sortAtClampWindow is the maximum amount of future-drift we'll trust in a
// record's self-reported createdAt. Bluesky uses ~5 minutes to absorb NTP
// skew on honest clients while still clamping obviously-futured posts so
// one malicious/misconfigured actor can't pin themselves to the top of
// every feed forever. Issue #26.
const sortAtClampWindow = 5 * time.Minute

// ComputeSortAt returns the timestamp used for feed ordering (sort_at).
//
// The formula is: min(createdAt, now + clampWindow), falling back to now if
// createdAt is unparseable or absent. This matches the Bluesky AppView's
// behavior: honor the record's self-reported timestamp when plausible, but
// clamp it against the indexer's clock so a bad actor can't stake out the
// top of the feed by setting createdAt to the year 3000.
//
// A zero createdAt (caller passed nil or parse failed) returns `now`, so
// callers can pre-parse the JSON and pass the resulting *time.Time without
// double-checking for zero.
func ComputeSortAt(createdAt *time.Time, now time.Time) time.Time {
	if createdAt == nil || createdAt.IsZero() {
		return now
	}
	clamp := now.Add(sortAtClampWindow)
	if createdAt.After(clamp) {
		return clamp
	}
	return *createdAt
}

// ExtractCreatedAt pulls the top-level `createdAt` field from a record JSON
// payload and parses it as RFC 3339. Returns nil if the field is missing,
// empty, or doesn't parse — the caller should treat that the same as "use
// now" (see ComputeSortAt). This intentionally only looks at the root
// object; nested timestamps would be lexicon-specific and belong in a
// per-collection resolver, not here.
func ExtractCreatedAt(recordJSON []byte) *time.Time {
	if len(recordJSON) == 0 {
		return nil
	}
	var envelope struct {
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal(recordJSON, &envelope); err != nil {
		return nil
	}
	if envelope.CreatedAt == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, envelope.CreatedAt)
	if err != nil {
		return nil
	}
	return &t
}
