// Package labeler subscribes to AT Protocol labelers and persists labels
// to the local database. It mirrors the shape of internal/jetstream but
// speaks the DAG-CBOR framed subscribeLabels protocol instead of JSON.
package labeler

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// FrameHeader is the first CBOR object in a subscribeLabels frame.
// Successful frames use op=1. op=-1 signals an error frame with a body of
// {"error": string, "message": string} which callers can log and drop.
type FrameHeader struct {
	Op int8   `cbor:"op"`
	T  string `cbor:"t,omitempty"`
}

// LabelsBody is the second CBOR object in a #labels frame.
type LabelsBody struct {
	Seq    int64        `cbor:"seq"`
	Labels []ProtoLabel `cbor:"labels"`
}

// ProtoLabel is the wire shape of com.atproto.label.defs#label.
// Fields match the Lexicon; `Sig` is captured but not verified.
type ProtoLabel struct {
	Ver int32  `cbor:"ver,omitempty"`
	Src string `cbor:"src"`
	URI string `cbor:"uri"`
	CID string `cbor:"cid,omitempty"`
	Val string `cbor:"val"`
	Neg bool   `cbor:"neg,omitempty"`
	Cts string `cbor:"cts"`
	Exp string `cbor:"exp,omitempty"`
	Sig []byte `cbor:"sig,omitempty"`
}

// ErrorBody is the body shape for error frames (op=-1).
type ErrorBody struct {
	Error   string `cbor:"error"`
	Message string `cbor:"message,omitempty"`
}

// DecodeFrame parses a single websocket binary message (a header object
// followed by a body object, both DAG-CBOR) into the header and the raw
// body bytes. Callers further-decode the body based on header.T.
func DecodeFrame(msg []byte) (*FrameHeader, []byte, error) {
	if len(msg) == 0 {
		return nil, nil, fmt.Errorf("decode frame: empty message")
	}
	var hdr FrameHeader
	rest, err := cbor.UnmarshalFirst(msg, &hdr)
	if err != nil {
		return nil, nil, fmt.Errorf("decode frame header: %w", err)
	}
	return &hdr, rest, nil
}

// DecodeLabelsBody decodes the body of a #labels frame.
func DecodeLabelsBody(body []byte) (*LabelsBody, error) {
	var lb LabelsBody
	if err := cbor.Unmarshal(body, &lb); err != nil {
		return nil, fmt.Errorf("decode labels body: %w", err)
	}
	return &lb, nil
}

// DecodeErrorBody decodes the body of an error frame (op=-1).
func DecodeErrorBody(body []byte) (*ErrorBody, error) {
	var eb ErrorBody
	if err := cbor.Unmarshal(body, &eb); err != nil {
		return nil, fmt.Errorf("decode error body: %w", err)
	}
	return &eb, nil
}
