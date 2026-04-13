package database

import (
	"errors"
	"testing"
	"time"
)

func TestDialectString(t *testing.T) {
	tests := []struct {
		dialect Dialect
		want    string
	}{
		{PostgreSQL, "postgresql"},
		{Dialect(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.dialect.String()
			if got != tt.want {
				t.Errorf("Dialect(%d).String() = %q, want %q", tt.dialect, got, tt.want)
			}
		})
	}
}

func TestParseDialect(t *testing.T) {
	tests := []struct {
		name        string
		databaseURL string
		want        Dialect
	}{
		{
			name:        "postgres URL",
			databaseURL: "postgres://user:pass@localhost/db",
			want:        PostgreSQL,
		},
		{
			name:        "postgresql URL",
			databaseURL: "postgresql://user:pass@localhost/db",
			want:        PostgreSQL,
		},
		{
			name:        "POSTGRES uppercase",
			databaseURL: "POSTGRES://user:pass@localhost/db",
			want:        PostgreSQL,
		},
		{
			name:        "unknown returns invalid dialect",
			databaseURL: "mysql://user:pass@localhost/db",
			want:        Dialect(-1),
		},
		{
			name:        "empty string returns invalid dialect",
			databaseURL: "",
			want:        Dialect(-1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDialect(tt.databaseURL)
			if got != tt.want {
				t.Errorf("ParseDialect(%q) = %v, want %v", tt.databaseURL, got, tt.want)
			}
		})
	}
}

func TestDbError(t *testing.T) {
	t.Run("error with cause", func(t *testing.T) {
		cause := errors.New("underlying error")
		err := &DbError{Type: "query", Message: "failed to execute", Cause: cause}

		expected := "query: failed to execute: underlying error"
		if err.Error() != expected {
			t.Errorf("DbError.Error() = %q, want %q", err.Error(), expected)
		}

		if !errors.Is(err, cause) {
			t.Errorf("DbError.Unwrap() = %v, want %v", err.Unwrap(), cause)
		}
	})

	t.Run("error without cause", func(t *testing.T) {
		err := &DbError{Type: "connection", Message: "connection refused"}

		expected := "connection: connection refused"
		if err.Error() != expected {
			t.Errorf("DbError.Error() = %q, want %q", err.Error(), expected)
		}

		if err.Unwrap() != nil {
			t.Errorf("DbError.Unwrap() = %v, want nil", err.Unwrap())
		}
	})
}

func TestErrorConstructors(t *testing.T) {
	cause := errors.New("test error")

	tests := []struct {
		name     string
		err      *DbError
		wantType string
	}{
		{
			name:     "ConnectionError",
			err:      ConnectionError("connection failed", cause),
			wantType: "connection",
		},
		{
			name:     "QueryError",
			err:      QueryError("query failed", cause),
			wantType: "query",
		},
		{
			name:     "DecodeError",
			err:      DecodeError("decode failed", cause),
			wantType: "decode",
		},
		{
			name:     "ConstraintError",
			err:      ConstraintError("constraint violated", cause),
			wantType: "constraint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Type != tt.wantType {
				t.Errorf("%s Type = %q, want %q", tt.name, tt.err.Type, tt.wantType)
			}
			if !errors.Is(tt.err, cause) {
				t.Errorf("%s Cause = %v, want %v", tt.name, tt.err.Cause, cause)
			}
		})
	}
}

func TestValueHelpers(t *testing.T) {
	t.Run("Text", func(t *testing.T) {
		v := Text("hello")
		if v != TextValue("hello") {
			t.Errorf("Text(%q) = %v, want TextValue(%q)", "hello", v, "hello")
		}
	})

	t.Run("Int", func(t *testing.T) {
		v := Int(42)
		if v != IntValue(42) {
			t.Errorf("Int(%d) = %v, want IntValue(%d)", 42, v, 42)
		}
	})

	t.Run("Float", func(t *testing.T) {
		v := Float(3.14)
		if v != FloatValue(3.14) {
			t.Errorf("Float(%f) = %v, want FloatValue(%f)", 3.14, v, 3.14)
		}
	})

	t.Run("Bool", func(t *testing.T) {
		v := Bool(true)
		if v != BoolValue(true) {
			t.Errorf("Bool(%v) = %v, want BoolValue(%v)", true, v, true)
		}
	})

	t.Run("Null", func(t *testing.T) {
		v := Null()
		if v != (NullValue{}) {
			t.Errorf("Null() = %v, want NullValue{}", v)
		}
	})

	t.Run("Blob", func(t *testing.T) {
		data := []byte{1, 2, 3}
		v := Blob(data)
		if string(v) != string(BlobValue(data)) {
			t.Errorf("Blob(%v) = %v, want BlobValue(%v)", data, v, data)
		}
	})
}

func TestTimestamptz(t *testing.T) {
	tests := []struct {
		name    string
		time    time.Time
		wantHas string // Substring that should be in the result
	}{
		{
			name:    "UTC time",
			time:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			wantHas: "2024-01-15T10:30:00Z",
		},
		{
			name:    "non-UTC time gets converted",
			time:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.FixedZone("EST", -5*60*60)),
			wantHas: "2024-01-15T15:30:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Timestamptz(tt.time)
			if string(got) != tt.wantHas {
				t.Errorf("Timestamptz(%v) = %q, want %q", tt.time, got, tt.wantHas)
			}
		})
	}
}

func TestTimestamptzString(t *testing.T) {
	input := "2024-01-15T10:30:00Z"
	got := TimestamptzString(input)
	if string(got) != input {
		t.Errorf("TimestamptzString(%q) = %q, want %q", input, got, input)
	}
}

func TestNullableText(t *testing.T) {
	tests := []struct {
		name    string
		input   *string
		wantNil bool
	}{
		{
			name:    "nil returns NullValue",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "non-nil returns TextValue",
			input:   ptrString("hello"),
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NullableText(tt.input)
			_, isNull := got.(NullValue)
			if isNull != tt.wantNil {
				t.Errorf("NullableText(%v) isNull = %v, want %v", tt.input, isNull, tt.wantNil)
			}
		})
	}
}

func TestNullableInt(t *testing.T) {
	tests := []struct {
		name    string
		input   *int64
		wantNil bool
	}{
		{
			name:    "nil returns NullValue",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "non-nil returns IntValue",
			input:   ptrInt64(42),
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NullableInt(tt.input)
			_, isNull := got.(NullValue)
			if isNull != tt.wantNil {
				t.Errorf("NullableInt(%v) isNull = %v, want %v", tt.input, isNull, tt.wantNil)
			}
		})
	}
}

// Helper functions for creating pointers
func ptrString(s string) *string {
	return &s
}

func ptrInt64(i int64) *int64 {
	return &i
}
