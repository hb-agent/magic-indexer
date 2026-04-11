package labeler

import "testing"

// FuzzDecodeFrame feeds random bytes through decodeFrame +
// decodeLabelsBody + decodeInfoBody + decodeErrorBody and asserts
// that none of them panic on any input. Oversized / truncated /
// garbage frames should all return errors, not crash.
//
// Run locally with: go test -run=^$ -fuzz=FuzzDecodeFrame -fuzztime=30s
// In CI, this test executes the seed corpus on every run.
func FuzzDecodeFrame(f *testing.F) {
	// Seed with the valid-case shapes we already cover in frame_test.go
	// plus a few adversarial shapes.
	seeds := [][]byte{
		{},
		{0x00},
		{0xff, 0xff, 0xff, 0xff},
		// Valid empty-map CBOR (0xa0) twice
		{0xa0, 0xa0},
		// A single header with nothing else
		{0xa1, 0x61, 0x74, 0x66, 0x23, 0x6c, 0x61, 0x62, 0x65, 0x6c, 0x73},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeFrame panicked on %d-byte input: %v", len(data), r)
			}
		}()
		hdr, body, err := decodeFrame(data)
		if err != nil {
			return
		}
		// Dispatch on header type the same way the client does so
		// every decode path is exercised.
		if hdr == nil {
			return
		}
		switch {
		case hdr.Op == 1 && hdr.T == "#labels":
			_, _ = decodeLabelsBody(body)
		case hdr.Op == 1 && hdr.T == "#info":
			_, _ = decodeInfoBody(body)
		case hdr.Op == -1:
			_, _ = decodeErrorBody(body)
		}
	})
}
