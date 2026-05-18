package jetstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GainForest/hypergoat/internal/consumer"
	"github.com/GainForest/hypergoat/internal/cursor"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/metrics"
)

// ConsumerConfig configures the Jetstream consumer.
type ConsumerConfig struct {
	// JetstreamURL is the Jetstream WebSocket endpoint.
	JetstreamURL string

	// Collections to subscribe to.
	Collections []string

	// DisableCursor disables cursor tracking.
	DisableCursor bool

	// CursorFlushInterval is how often to flush the cursor to database.
	CursorFlushInterval time.Duration
}

// Consumer consumes events from Jetstream and stores them in the database.
type Consumer struct {
	config     ConsumerConfig
	client     *Client
	configRepo *repositories.ConfigRepository
	processor  *ingestion.RecordProcessor

	// Cursor tracking (uses shared cursor.Flusher)
	cursorFlusher *cursor.Flusher
	cursorDone    chan struct{}

	// Stats
	stats      Stats
	statsMu    sync.RWMutex
	statsStart time.Time

	// For reconnection
	ctx       context.Context
	ctxCancel context.CancelFunc
	clientMu  sync.Mutex
	running   bool

	// errLog: rate-limited error logging policy — first 5 errors at
	// full severity, then 1/min so a sustained DB outage doesn't
	// fill the log with millions of identical lines. Atomic
	// counters because /metrics scraping may read them concurrently
	// with the dispatcher goroutine (R1.6).
	errLog rateLimitedErrLogger
}

// rateLimitedErrLogger throttles repeated error lines from the
// hot ingestion path. Mirrors the equivalent type in internal/tap;
// they're deliberately not shared (A12 — don't refactor for a
// hypothetical third consumer).
//
// suppressed + lastEmit are atomic because the throttle-emit
// branch uses a CAS to ensure exactly one summary line per
// throttle interval even if log() is called concurrently. In
// practice both callers run on the single processEvents
// goroutine, so the CAS is belt-and-braces — but the cost is
// trivial and it future-proofs against a second caller (e.g. a
// metrics handler reading the same struct).
type rateLimitedErrLogger struct {
	loudCount     int
	loudLimit     int
	throttleEvery time.Duration
	suppressed    atomic.Int64
	lastEmit      atomic.Int64 // unix nanoseconds
}

// Defaults for rateLimitedErrLogger. Per-package constants rather
// than a shared helper so each consumer can tune independently;
// see plan.md A12 for the "don't share consumer machinery"
// decision.
const (
	defaultErrLogLoudLimit = 5
	defaultErrLogThrottle  = time.Minute
)

func (l *rateLimitedErrLogger) log(msg string, args ...any) {
	if l.loudCount < l.loudLimit {
		l.loudCount++
		slog.Error(msg, args...)
		return
	}
	count := l.suppressed.Add(1)
	now := time.Now().UnixNano()
	last := l.lastEmit.Load()
	if time.Duration(now-last) >= l.throttleEvery {
		if l.lastEmit.CompareAndSwap(last, now) {
			args = append(args, "occurrences_since_last_log", count)
			slog.Error(msg+" (rate-limited)", args...)
			l.suppressed.Store(0)
		}
	}
}

// Stats tracks consumer statistics.
type Stats struct {
	EventsReceived int64
	RecordsCreated int64
	RecordsUpdated int64
	RecordsDeleted int64
	Errors         int64
}

// NewConsumer creates a new Jetstream consumer.
func NewConsumer(
	config ConsumerConfig,
	processor *ingestion.RecordProcessor,
	configRepo *repositories.ConfigRepository,
) *Consumer {
	if config.CursorFlushInterval == 0 {
		config.CursorFlushInterval = 5 * time.Second
	}

	c := &Consumer{
		config:     config,
		configRepo: configRepo,
		processor:  processor,
		cursorDone: make(chan struct{}),
		statsStart: time.Now(),
		errLog: rateLimitedErrLogger{
			loudLimit:     defaultErrLogLoudLimit,
			throttleEvery: defaultErrLogThrottle,
		},
	}

	c.cursorFlusher = cursor.NewFlusher(
		func(ctx context.Context, cur int64) error {
			return c.saveCursor(ctx, cur)
		},
		config.CursorFlushInterval,
		"jetstream",
	)

	return c
}

// Start begins consuming events from Jetstream with automatic reconnection.
func (c *Consumer) Start(ctx context.Context) error {
	c.clientMu.Lock()
	c.ctx, c.ctxCancel = context.WithCancel(ctx)
	c.running = true
	c.clientMu.Unlock()

	return consumer.RunWithReconnect(c.ctx, c.startInternal, consumer.BackoffOpts{})
}

// startInternal does the actual connection and event processing.
func (c *Consumer) startInternal(ctx context.Context) error {
	// Clean up old client if exists (for reconnection scenarios)
	c.clientMu.Lock()
	if c.client != nil {
		c.client.Stop()
		c.client = nil
	}
	c.clientMu.Unlock()

	// Load cursor from database (fresh load on each connection attempt)
	cur, err := c.loadCursor(ctx)
	if err != nil {
		slog.Warn("Failed to load cursor, starting from live", "error", err)
	} else if cur > 0 {
		slog.Info("Resuming from cursor", "cursor", cur)
	}

	// Create client
	c.clientMu.Lock()
	c.client = NewClient(ClientConfig{
		URL:           c.config.JetstreamURL,
		Collections:   c.config.Collections,
		Cursor:        cur,
		DisableCursor: c.config.DisableCursor,
	})
	c.clientMu.Unlock()

	// Connect
	if err := c.client.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Start cursor flusher
	if !c.config.DisableCursor {
		go c.cursorFlusher.Run(ctx)
	}

	// Start event processor
	go c.processEvents(ctx, c.client)

	// Run client (blocking)
	return c.client.Run(ctx)
}

// Stop stops the consumer.
func (c *Consumer) Stop() {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()

	c.running = false
	if c.ctxCancel != nil {
		c.ctxCancel()
	}
	select {
	case <-c.cursorDone:
		// Already closed
	default:
		close(c.cursorDone)
	}
	if c.client != nil {
		c.client.Stop()
	}
}

// UpdateCollections updates the subscribed collections and reconnects.
func (c *Consumer) UpdateCollections(parent context.Context, collections []string) error {
	c.clientMu.Lock()
	wasRunning := c.running
	oldClient := c.client
	c.clientMu.Unlock()

	if !wasRunning {
		c.clientMu.Lock()
		c.config.Collections = collections
		c.clientMu.Unlock()
		slog.Info("Updated Jetstream collections (not running)", "collections", collections)
		return nil
	}

	slog.Info("Updating Jetstream collections, reconnecting...", "collections", collections)

	// Stop old client
	if oldClient != nil {
		oldClient.Stop()
	}

	c.clientMu.Lock()
	c.config.Collections = collections
	c.cursorDone = make(chan struct{})
	if c.ctxCancel != nil {
		c.ctxCancel()
	}
	c.ctx, c.ctxCancel = context.WithCancel(parent)
	ctx := c.ctx
	c.clientMu.Unlock()

	// Start in background with shared reconnection logic.
	go func() {
		_ = consumer.RunWithReconnect(ctx, c.startInternal, consumer.BackoffOpts{})
	}()

	return nil
}

// Collections returns the currently subscribed collections.
func (c *Consumer) Collections() []string {
	c.clientMu.Lock()
	cols := make([]string, len(c.config.Collections))
	copy(cols, c.config.Collections)
	c.clientMu.Unlock()
	return cols
}

// Stats returns the current statistics.
func (c *Consumer) Stats() Stats {
	c.statsMu.RLock()
	defer c.statsMu.RUnlock()
	return c.stats
}

// processEvents handles incoming events.
func (c *Consumer) processEvents(ctx context.Context, client *Client) {
	for event := range client.Events() {
		c.statsMu.Lock()
		c.stats.EventsReceived++
		eventCount := c.stats.EventsReceived
		c.statsMu.Unlock()

		// Process commit events
		if event.IsCommit() {
			slog.Debug("[jetstream] Event received",
				"collection", event.Commit.Collection,
				"operation", event.Commit.Operation,
				"did", event.DID,
				"total_events", eventCount,
			)
			metrics.RecordJetstreamEvent(event.Commit.Collection, string(event.Commit.Operation))

			commit := event.Commit
			// SourceEventID = TimeUS so LogActivity can dedupe a
			// redelivered commit on the partial unique index added by
			// migration 028. Without this, a crash between
			// LogActivity and record insert produces a duplicate
			// pending activity row on restart even though CID dedup
			// catches the record itself.
			sourceID := event.TimeUS
			err := c.processor.ProcessRecord(ctx, ingestion.ProcessOp{
				DID:           event.DID,
				URI:           commit.URI(event.DID),
				Collection:    commit.Collection,
				RKey:          commit.RKey,
				CID:           commit.CID,
				Operation:     ingestion.Operation(commit.Operation),
				Record:        commit.Record,
				SourceEventID: &sourceID,
			})
			if err != nil {
				c.errLog.log("Failed to handle commit",
					"error", err,
					"did", event.DID,
					"collection", commit.Collection,
				)
				metrics.RecordJetstreamError()
				metrics.IngestionError(metrics.IngestionConsumerJetstream)
				c.statsMu.Lock()
				c.stats.Errors++
				c.statsMu.Unlock()
				continue // Don't advance cursor on failure
			}

			// Update local stats
			c.statsMu.Lock()
			switch commit.Operation {
			case OpCreate:
				c.stats.RecordsCreated++
			case OpUpdate:
				c.stats.RecordsUpdated++
			case OpDelete:
				c.stats.RecordsDeleted++
			}
			c.statsMu.Unlock()
		}

		// Update cursor only after successful processing
		c.cursorFlusher.SetCurrent(event.TimeUS)
	}
}

// loadCursor loads the cursor from the config table.
func (c *Consumer) loadCursor(ctx context.Context) (int64, error) {
	value, err := c.configRepo.Get(ctx, "jetstream_cursor")
	if err != nil {
		return 0, err
	}
	if value == "" {
		return 0, nil
	}

	var cur int64
	if err := json.Unmarshal([]byte(value), &cur); err != nil {
		return 0, err
	}
	return cur, nil
}

// saveCursor saves the cursor to the config table.
func (c *Consumer) saveCursor(ctx context.Context, cur int64) error {
	value, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return c.configRepo.Set(ctx, "jetstream_cursor", string(value))
}
