package labeler

import "time"

// Defaults and resource bounds used across the labeler package. All of
// these are intentionally constants rather than config knobs — we want
// one place to find and adjust them, and none of them meaningfully
// belong to the operator's tuning surface. Per-deployment tuning that
// IS operator-facing (cursor flush interval, labeler DIDs) lives on
// ConsumerConfig instead.
const (
	// MaxFrameSize bounds a single websocket binary frame from a labeler.
	// ATProto label frames are small (a few KB at most); 1 MiB leaves
	// ample headroom for legitimate batches while rejecting malicious
	// oversized payloads before they exhaust memory.
	MaxFrameSize = 1 << 20 // 1 MiB

	// EventChannelBufferSize is the buffer size for the event channel
	// between the websocket reader and the consumer. 256 absorbs
	// reasonable bursts without deferring backpressure too long.
	EventChannelBufferSize = 256

	// MaxBackfillBodyBytes bounds the size of a single queryLabels HTTP
	// response so a malicious or misbehaving labeler can't exhaust
	// memory. Well above any realistic page (at 250 labels per page
	// with generous per-label size, a page is ~100 KB).
	MaxBackfillBodyBytes = 10 << 20 // 10 MiB

	// MaxBackfillPages bounds the number of queryLabels pages we'll
	// fetch in a single Fetch call. At limit=250 per page this allows
	// ~10M labels, which far exceeds any realistic labeler backfill
	// and prevents a runaway loop if the server returns an unexpected
	// cursor sequence.
	MaxBackfillPages = 40_000

	// MaxKnownVals caps the size of the in-process label-definition
	// memoisation map. On overflow we evict the oldest entries so
	// memoization stays active instead of falling through to a
	// permanent slow path.
	MaxKnownVals = 10_000

	// Per-label length caps. All of these are generous compared to
	// typical ATProto data (DIDs ~32 bytes, at-URIs ~100 bytes, CIDs
	// ~60 bytes, label values short strings) while still rejecting
	// pathological inputs before they hit the DB.
	MaxLabelValLen = 128
	MaxLabelSrcLen = 512
	MaxLabelURILen = 512
	MaxLabelCIDLen = 256

	// BackfillProgressInterval controls how often a mid-backfill
	// progress line is emitted while the initial queryLabels sweep
	// is running.
	BackfillProgressInterval = 1000

	// BackfillCheckpointInterval is how often (in pages) we persist
	// the queryLabels cursor during backfill so an interrupted run
	// can resume without re-fetching completed pages.
	BackfillCheckpointInterval = 4

	// Default websocket timings. The pong wait must comfortably
	// exceed ping period; the write timeout covers both ping and
	// close frames.
	defaultWriteTimeout = 10 * time.Second
	defaultPongWait     = 60 * time.Second
	defaultPingPeriod   = 50 * time.Second
)
