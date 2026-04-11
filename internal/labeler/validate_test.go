package labeler

import (
	"strings"
	"testing"
)

// newTestConsumer builds a Consumer just well-enough to call the
// pure-function validators. The repo pointers are left nil because
// validateLabel and parseLabelTime never touch them.
func newTestConsumer() *Consumer {
	return &Consumer{
		config: ConsumerConfig{LabelerDID: "did:plc:test"},
	}
}

func TestValidateLabel(t *testing.T) {
	c := newTestConsumer()

	cases := []struct {
		name string
		l    protoLabel
		want bool
	}{
		{
			name: "valid at:// label",
			l:    protoLabel{Src: "did:plc:x", URI: "at://did:plc:alice/app.bsky.feed.post/1", Val: "spam"},
			want: true,
		},
		{
			name: "empty src",
			l:    protoLabel{Src: "", URI: "at://did:plc:alice/x/1", Val: "spam"},
			want: false,
		},
		{
			name: "empty uri",
			l:    protoLabel{Src: "did:plc:x", URI: "", Val: "spam"},
			want: false,
		},
		{
			name: "empty val",
			l:    protoLabel{Src: "did:plc:x", URI: "at://did:plc:alice/x/1", Val: ""},
			want: false,
		},
		{
			name: "account-level did: URI rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "did:plc:alice", Val: "spam"},
			want: false,
		},
		{
			name: "bare at:// with no record path rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "at://", Val: "spam"},
			want: false,
		},
		{
			name: "http:// URI rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "http://example.com/x", Val: "spam"},
			want: false,
		},
		{
			name: "oversized val rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "at://did:plc:alice/x/1", Val: strings.Repeat("a", MaxLabelValLen+1)},
			want: false,
		},
		{
			name: "val exactly at cap is allowed",
			l:    protoLabel{Src: "did:plc:x", URI: "at://did:plc:alice/x/1", Val: strings.Repeat("a", MaxLabelValLen)},
			want: true,
		},
		{
			name: "oversized src rejected",
			l:    protoLabel{Src: strings.Repeat("s", MaxLabelSrcLen+1), URI: "at://did:plc:alice/x/1", Val: "spam"},
			want: false,
		},
		{
			name: "oversized uri rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "at://" + strings.Repeat("a", MaxLabelURILen), Val: "spam"},
			want: false,
		},
		{
			name: "oversized cid rejected",
			l:    protoLabel{Src: "did:plc:x", URI: "at://did:plc:alice/x/1", Val: "spam", CID: strings.Repeat("c", MaxLabelCIDLen+1)},
			want: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := c.validateLabel(&tt.l)
			if got != tt.want {
				t.Errorf("validateLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLabelTime(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantNil bool
	}{
		{"empty string returns nil", "", true},
		{"RFC3339 with Z", "2026-04-11T12:34:56Z", false},
		{"RFC3339 with offset", "2026-04-11T12:34:56+02:00", false},
		{"RFC3339Nano with nanoseconds", "2026-04-11T12:34:56.123456789Z", false},
		{"malformed returns nil", "not-a-date", true},
		{"bare date without timezone returns nil", "2026-04-11", true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLabelTime(tt.input)
			if tt.wantNil && got != nil {
				t.Errorf("parseLabelTime(%q) = %v, want nil", tt.input, got)
			}
			if !tt.wantNil && got == nil {
				t.Errorf("parseLabelTime(%q) = nil, want parsed time", tt.input)
			}
		})
	}
}
