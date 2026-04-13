package postgres

import (
	"testing"

	"github.com/GainForest/hypergoat/internal/database"
)

func TestExecutor_Dialect(t *testing.T) {
	e := &Executor{}
	if got := e.Dialect(); got != database.PostgreSQL {
		t.Errorf("Executor.Dialect() = %v, want %v", got, database.PostgreSQL)
	}
}

func TestExecutor_Placeholder(t *testing.T) {
	e := &Executor{}

	tests := []struct {
		index int
		want  string
	}{
		{1, "$1"},
		{2, "$2"},
		{10, "$10"},
		{100, "$100"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := e.Placeholder(tt.index)
			if got != tt.want {
				t.Errorf("Executor.Placeholder(%d) = %q, want %q", tt.index, got, tt.want)
			}
		})
	}
}

func TestExecutor_Placeholders(t *testing.T) {
	e := &Executor{}

	tests := []struct {
		name       string
		count      int
		startIndex int
		want       string
	}{
		{
			name:       "zero count",
			count:      0,
			startIndex: 1,
			want:       "",
		},
		{
			name:       "negative count",
			count:      -1,
			startIndex: 1,
			want:       "",
		},
		{
			name:       "single placeholder",
			count:      1,
			startIndex: 1,
			want:       "$1",
		},
		{
			name:       "multiple placeholders",
			count:      3,
			startIndex: 1,
			want:       "$1, $2, $3",
		},
		{
			name:       "non-zero start index",
			count:      3,
			startIndex: 5,
			want:       "$5, $6, $7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.Placeholders(tt.count, tt.startIndex)
			if got != tt.want {
				t.Errorf("Executor.Placeholders(%d, %d) = %q, want %q", tt.count, tt.startIndex, got, tt.want)
			}
		})
	}
}

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
