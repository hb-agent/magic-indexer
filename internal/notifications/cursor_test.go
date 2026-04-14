package notifications

import (
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	now := time.Now().UTC().Round(time.Nanosecond)
	encoded := encodeCursor(now, 42)

	sortAt, id, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !sortAt.Equal(now) {
		t.Errorf("sortAt round-trip mismatch: %v vs %v", sortAt, now)
	}
	if id != 42 {
		t.Errorf("id round-trip mismatch: %d", id)
	}
}

func TestCursorEmpty(t *testing.T) {
	sortAt, id, err := decodeCursor("")
	if err != nil {
		t.Errorf("empty cursor should decode to zero value without error, got %v", err)
	}
	if !sortAt.IsZero() || id != 0 {
		t.Errorf("empty cursor should decode to zero values")
	}
}

func TestCursorInvalidBase64(t *testing.T) {
	_, _, err := decodeCursor("!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestCursorWrongVersion(t *testing.T) {
	// Manually encode a cursor with a wrong version tag.
	// Using a legacy-ish format.
	encoded := "WyJ2Mjpub3RpZiIsIjIwMjYtMDEtMDFUMDA6MDA6MDBaIiwiNDIiXQ==" // ["v2:notif", ...]
	_, _, err := decodeCursor(encoded)
	if err == nil {
		t.Error("expected error for wrong version tag")
	}
}

func TestCursorMalformedJSON(t *testing.T) {
	// Base64 encode a non-JSON string.
	encoded := "bm90LWpzb24="
	_, _, err := decodeCursor(encoded)
	if err == nil {
		t.Error("expected error for non-JSON cursor content")
	}
}
