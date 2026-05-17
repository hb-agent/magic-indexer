package postgres

import (
	"net/url"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database"
)

func TestConvertParams(t *testing.T) {
	tests := []struct {
		name   string
		params []database.Value
		want   []any
	}{
		{
			name:   "nil params",
			params: nil,
			want:   nil,
		},
		{
			name:   "empty params",
			params: []database.Value{},
			want:   nil,
		},
		{
			name: "TextValue",
			params: []database.Value{
				database.TextValue("hello"),
			},
			want: []any{"hello"},
		},
		{
			name: "IntValue",
			params: []database.Value{
				database.IntValue(42),
			},
			want: []any{int64(42)},
		},
		{
			name: "FloatValue",
			params: []database.Value{
				database.FloatValue(3.14),
			},
			want: []any{float64(3.14)},
		},
		{
			name: "BoolValue true",
			params: []database.Value{
				database.BoolValue(true),
			},
			want: []any{true},
		},
		{
			name: "BoolValue false",
			params: []database.Value{
				database.BoolValue(false),
			},
			want: []any{false},
		},
		{
			name: "NullValue",
			params: []database.Value{
				database.NullValue{},
			},
			want: []any{nil},
		},
		{
			name: "BlobValue",
			params: []database.Value{
				database.BlobValue([]byte{1, 2, 3}),
			},
			want: []any{[]byte{1, 2, 3}},
		},
		{
			name: "TimestamptzValue",
			params: []database.Value{
				database.TimestamptzValue("2024-01-15T10:30:00Z"),
			},
			want: []any{"2024-01-15T10:30:00Z"},
		},
		{
			name: "mixed values",
			params: []database.Value{
				database.TextValue("name"),
				database.IntValue(42),
				database.BoolValue(true),
				database.NullValue{},
			},
			want: []any{"name", int64(42), true, nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertParams(tt.params)

			if len(got) != len(tt.want) {
				t.Errorf("convertParams() length = %d, want %d", len(got), len(tt.want))
				return
			}

			for i := range got {
				// Special handling for byte slices
				if gotBytes, ok := got[i].([]byte); ok {
					wantBytes, ok := tt.want[i].([]byte)
					if !ok {
						t.Errorf("convertParams()[%d] = %T, want %T", i, got[i], tt.want[i])
						continue
					}
					if string(gotBytes) != string(wantBytes) {
						t.Errorf("convertParams()[%d] = %v, want %v", i, gotBytes, wantBytes)
					}
					continue
				}

				if got[i] != tt.want[i] {
					t.Errorf("convertParams()[%d] = %v (%T), want %v (%T)", i, got[i], got[i], tt.want[i], tt.want[i])
				}
			}
		})
	}
}

func TestInjectStatementTimeout_PristineURL(t *testing.T) {
	in := "postgres://u:p@h:5432/db?sslmode=disable"
	got := injectStatementTimeout(in, 30000)
	// Parse the result so we don't depend on parameter ordering.
	values := mustParseQuery(t, got)
	if v := values.Get("options"); v != "-c statement_timeout=30000" {
		t.Errorf("options = %q, want %q", v, "-c statement_timeout=30000")
	}
	if v := values.Get("sslmode"); v != "disable" {
		t.Errorf("sslmode = %q, want disable", v)
	}
}

func TestInjectStatementTimeout_ZeroDisabled(t *testing.T) {
	in := "postgres://u:p@h:5432/db?sslmode=disable"
	got := injectStatementTimeout(in, 0)
	if got != in {
		t.Errorf("expected URL unchanged when timeout=0, got %q", got)
	}
}

func TestInjectStatementTimeout_PreservesIdleInTxTimeout(t *testing.T) {
	// The substring `statement_timeout` lives inside
	// `idle_in_transaction_session_timeout`. A naive check would
	// false-match and skip our append — regex-anchor must reject this.
	in := "postgres://u:p@h:5432/db?options=-c%20idle_in_transaction_session_timeout%3D300000"
	got := injectStatementTimeout(in, 30000)
	values := mustParseQuery(t, got)
	options := values.Get("options")
	wantSubstrings := []string{
		"-c idle_in_transaction_session_timeout=300000",
		"-c statement_timeout=30000",
	}
	for _, sub := range wantSubstrings {
		if !contains(options, sub) {
			t.Errorf("options = %q, expected substring %q", options, sub)
		}
	}
}

func TestInjectStatementTimeout_OperatorOverridePreserved(t *testing.T) {
	// Operator already set statement_timeout — we must not touch it.
	in := "postgres://u:p@h:5432/db?options=-c%20statement_timeout%3D10000"
	got := injectStatementTimeout(in, 30000)
	if got != in {
		t.Errorf("operator override not preserved:\n  got: %s\n want: %s", got, in)
	}
}

func TestInjectStatementTimeout_MultiFlagOptionsPreserved(t *testing.T) {
	// Operator set both search_path and statement_timeout — we must
	// leave both intact.
	in := "postgres://u:p@h:5432/db?options=-c%20statement_timeout%3D10000%20-c%20search_path%3Dfoo"
	got := injectStatementTimeout(in, 30000)
	values := mustParseQuery(t, got)
	options := values.Get("options")
	if !contains(options, "statement_timeout=10000") {
		t.Errorf("operator statement_timeout dropped: %q", options)
	}
	if !contains(options, "search_path=foo") {
		t.Errorf("operator search_path dropped: %q", options)
	}
	if contains(options, "statement_timeout=30000") {
		t.Errorf("plan's default leaked over operator override: %q", options)
	}
}

func TestInjectStatementTimeout_OtherUrlParamsSurvive(t *testing.T) {
	// SSL params, application_name with a percent-encoded value,
	// connect_timeout — all must survive the rewrite. Verifies
	// the url.Values round-trip is not lossy.
	in := "postgres://u:p@h:5432/db?sslmode=require&sslrootcert=/path/with%20space/ca.pem&application_name=hypergoat&connect_timeout=10"
	got := injectStatementTimeout(in, 30000)
	values := mustParseQuery(t, got)
	checks := map[string]string{
		"sslmode":          "require",
		"sslrootcert":      "/path/with space/ca.pem",
		"application_name": "hypergoat",
		"connect_timeout":  "10",
	}
	for k, want := range checks {
		if v := values.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if v := values.Get("options"); v != "-c statement_timeout=30000" {
		t.Errorf("options = %q, want -c statement_timeout=30000", v)
	}
}

func TestInjectStatementTimeout_AppendsToExistingFlags(t *testing.T) {
	// Operator set a non-statement_timeout -c flag; we must append
	// (not replace) and preserve the existing entry.
	in := "postgres://u:p@h:5432/db?options=-c%20search_path%3Dfoo"
	got := injectStatementTimeout(in, 30000)
	values := mustParseQuery(t, got)
	options := values.Get("options")
	if !contains(options, "search_path=foo") {
		t.Errorf("search_path dropped: %q", options)
	}
	if !contains(options, "statement_timeout=30000") {
		t.Errorf("statement_timeout not appended: %q", options)
	}
}

func TestInjectStatementTimeout_MalformedURLReturnedUnchanged(t *testing.T) {
	// We don't try to fix malformed URLs — sql.Open will surface
	// the real parse error to the operator.
	in := "::not a url"
	got := injectStatementTimeout(in, 30000)
	if got != in {
		t.Errorf("expected URL unchanged on parse error, got %q", got)
	}
}

// --- helpers ---

func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Query()
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// Sec-M1: Postgres accepts three syntactic forms of -c
// directive. The regex must catch all three so the
// operator-override contract holds for each.

func TestInjectStatementTimeout_PreservesGetoptShortForm(t *testing.T) {
	// `-cstatement_timeout=10000` (no space) — getopt
	// short-option-with-value form. Postgres accepts it.
	in := "postgres://u:p@h:5432/db?options=-cstatement_timeout%3D10000"
	got := injectStatementTimeout(in, 30000)
	if got != in {
		t.Errorf("operator -c<no-space> override not preserved:\n  got:  %s\n want: %s", got, in)
	}
}

func TestInjectStatementTimeout_PreservesLongOptionForm(t *testing.T) {
	// `--statement_timeout=10000` — long-option form. Equivalent
	// to `-c statement_timeout=10000` per Postgres docs.
	in := "postgres://u:p@h:5432/db?options=--statement_timeout%3D10000"
	got := injectStatementTimeout(in, 30000)
	if got != in {
		t.Errorf("operator --long-form override not preserved:\n  got:  %s\n want: %s", got, in)
	}
}

func TestInjectStatementTimeout_DoesNotFalseMatchLongerName(t *testing.T) {
	// A hypothetical GUC like `--statement_timeout_extra=` must not
	// match the regex — the `=` immediately after `statement_timeout`
	// is the anchor that distinguishes them.
	in := "postgres://u:p@h:5432/db?options=--statement_timeout_extra%3D5000"
	got := injectStatementTimeout(in, 30000)
	values := mustParseQuery(t, got)
	options := values.Get("options")
	if !contains(options, "statement_timeout_extra=5000") {
		t.Errorf("longer-named GUC was dropped: %q", options)
	}
	if !contains(options, "-c statement_timeout=30000") {
		t.Errorf("our default was not appended (regex false-matched the longer name): %q", options)
	}
}
