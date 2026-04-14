package ingestion

import (
	"testing"
	"time"
)

func TestComputeSortAt(t *testing.T) {
	now := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		createdAt *time.Time
		want      time.Time
	}{
		{
			name:      "nil falls back to now",
			createdAt: nil,
			want:      now,
		},
		{
			name:      "zero falls back to now",
			createdAt: &time.Time{},
			want:      now,
		},
		{
			name:      "past createdAt is honored verbatim",
			createdAt: ptrTime(now.Add(-1 * time.Hour)),
			want:      now.Add(-1 * time.Hour),
		},
		{
			name:      "slight future within clamp window is honored",
			createdAt: ptrTime(now.Add(2 * time.Minute)),
			want:      now.Add(2 * time.Minute),
		},
		{
			name:      "far-future createdAt clamps to now+clampWindow",
			createdAt: ptrTime(now.Add(10 * 365 * 24 * time.Hour)),
			want:      now.Add(sortAtClampWindow),
		},
		{
			name:      "boundary: exactly at clamp is honored",
			createdAt: ptrTime(now.Add(sortAtClampWindow)),
			want:      now.Add(sortAtClampWindow),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSortAt(tc.createdAt, now)
			if !got.Equal(tc.want) {
				t.Errorf("ComputeSortAt = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractCreatedAt(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *time.Time
	}{
		{"empty", "", nil},
		{"no createdAt", `{"text":"hi"}`, nil},
		{"malformed json", `{{{`, nil},
		{"empty string", `{"createdAt":""}`, nil},
		{"unparseable", `{"createdAt":"yesterday"}`, nil},
		{
			name: "valid RFC3339",
			body: `{"createdAt":"2026-04-13T12:00:00Z","text":"hi"}`,
			want: ptrTime(time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCreatedAt([]byte(tc.body))
			switch {
			case tc.want == nil && got == nil:
				// ok
			case tc.want == nil || got == nil:
				t.Errorf("ExtractCreatedAt = %v, want %v", got, tc.want)
			case !got.Equal(*tc.want):
				t.Errorf("ExtractCreatedAt = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
