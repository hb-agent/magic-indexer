package labeler

import (
	"bytes"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func encodeFrame(t *testing.T, hdr frameHeader, body any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := cbor.NewEncoder(&buf)
	if err := enc.Encode(hdr); err != nil {
		t.Fatalf("encode header: %v", err)
	}
	if err := enc.Encode(body); err != nil {
		t.Fatalf("encode body: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeFrame_LabelsBody(t *testing.T) {
	msg := encodeFrame(t, frameHeader{Op: 1, T: "#labels"}, labelsBody{
		Seq: 42,
		Labels: []protoLabel{
			{
				Src: "did:plc:labelerz",
				URI: "at://did:plc:alice/app.bsky.feed.post/1",
				Val: "high-quality",
				Cts: "2026-04-11T00:00:00Z",
			},
			{
				Src: "did:plc:labelerz",
				URI: "at://did:plc:alice/app.bsky.feed.post/2",
				Val: "draft",
				Neg: true,
				Cts: "2026-04-11T00:00:01Z",
			},
		},
	})

	hdr, body, err := decodeFrame(msg)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if hdr.Op != 1 || hdr.T != "#labels" {
		t.Fatalf("unexpected header: %+v", hdr)
	}

	lb, err := decodeLabelsBody(body)
	if err != nil {
		t.Fatalf("decode labels body: %v", err)
	}
	if lb.Seq != 42 {
		t.Errorf("Seq = %d, want 42", lb.Seq)
	}
	if len(lb.Labels) != 2 {
		t.Fatalf("len(Labels) = %d, want 2", len(lb.Labels))
	}
	if lb.Labels[0].Val != "high-quality" {
		t.Errorf("label[0].Val = %q, want high-quality", lb.Labels[0].Val)
	}
	if !lb.Labels[1].Neg {
		t.Errorf("label[1].Neg = false, want true")
	}
}

func TestDecodeFrame_ErrorBody(t *testing.T) {
	msg := encodeFrame(t, frameHeader{Op: -1}, errorBody{
		Error:   "ConsumerTooSlow",
		Message: "cursor too far behind",
	})

	hdr, body, err := decodeFrame(msg)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if hdr.Op != -1 {
		t.Fatalf("Op = %d, want -1", hdr.Op)
	}

	eb, err := decodeErrorBody(body)
	if err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if eb.Error != "ConsumerTooSlow" {
		t.Errorf("Error = %q", eb.Error)
	}
}

func TestDecodeFrame_InfoBody_OutdatedCursor(t *testing.T) {
	msg := encodeFrame(t, frameHeader{Op: 1, T: "#info"}, infoBody{
		Name:    "OutdatedCursor",
		Message: "cursor is too old",
	})

	hdr, body, err := decodeFrame(msg)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if hdr.Op != 1 || hdr.T != "#info" {
		t.Fatalf("unexpected header: %+v", hdr)
	}

	ib, err := decodeInfoBody(body)
	if err != nil {
		t.Fatalf("decode info body: %v", err)
	}
	if ib.Name != "OutdatedCursor" {
		t.Errorf("Name = %q, want OutdatedCursor", ib.Name)
	}
}

func TestDecodeFrame_Truncated(t *testing.T) {
	_, _, err := decodeFrame([]byte{})
	if err == nil {
		t.Fatal("expected error on empty frame, got nil")
	}
}
