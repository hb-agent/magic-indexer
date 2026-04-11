package labeler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// ConsumerConfig configures a labeler consumer.
type ConsumerConfig struct {
	// LabelerDID identifies the labeler. Used as the `src` filter for
	// queryLabels backfill and to key the cursor in the config table.
	LabelerDID string

	// PDSHost is the labeler's host. If empty, the consumer resolves
	// LabelerDID via the DIDResolver on first connect.
	PDSHost string

	// DisableCursor disables cursor persistence (development only).
	DisableCursor bool

	// CursorFlushInterval is how often to persist the current cursor.
	CursorFlushInterval time.Duration

	// SkipBackfill skips the one-time queryLabels backfill. Used in tests
	// and when the operator explicitly wants live-only ingestion.
	SkipBackfill bool
}

// Stats tracks per-consumer counters for operational visibility.
type Stats struct {
	EventsReceived      int64 // #labels frames consumed
	LabelsReceived      int64 // raw labels decoded (pre-validation)
	LabelsPersisted     int64 // labels successfully upserted
	LabelsRejected      int64 // labels dropped due to validation or DB error
	AccountLevelSkipped int64 // labels rejected because URI is did:... (unreachable)
	ReconnectAttempts   int64
	OutdatedCursors     int64 // times the server reported OutdatedCursor
	LastSeq             int64 // most recent seq acked
}

// Consumer runs one labeler subscription end-to-end: resolve DID, run the
// initial backfill, then stream updates with cursor-based resumption and
// exponential backoff on disconnect.
type Consumer struct {
	config   ConsumerConfig
	labels   *repositories.LabelsRepository
	labelDef *repositories.LabelDefinitionsRepository
	cfgRepo  *repositories.ConfigRepository
	resolver *oauth.DIDResolver

	// client is the currently-active websocket client. Accessed only with
	// clientMu held. Goroutines that need the client for the duration of a
	// generation (processEvents) receive it as a parameter so they never
	// observe a new generation's client.
	client   *Client
	clientMu sync.Mutex

	cursor   int64
	cursorMu sync.Mutex

	// knownVals memoises label_definition vals we've already ensured so
	// we don't thrash the DB on every incoming label. Bounded by
	// MaxKnownVals with FIFO eviction so memoization stays active even
	// under a labeler that emits an unbounded number of distinct vals.
	knownValsMu    sync.Mutex
	knownVals      map[string]struct{}
	knownValsOrder []string

	// Stats (protected by statsMu)
	stats   Stats
	statsMu sync.RWMutex

	ctx       context.Context
	ctxCancel context.CancelFunc

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewConsumer creates a new labeler consumer.
func NewConsumer(
	config ConsumerConfig,
	labels *repositories.LabelsRepository,
	labelDef *repositories.LabelDefinitionsRepository,
	cfgRepo *repositories.ConfigRepository,
	resolver *oauth.DIDResolver,
) *Consumer {
	if config.CursorFlushInterval == 0 {
		config.CursorFlushInterval = 5 * time.Second
	}
	return &Consumer{
		config:    config,
		labels:    labels,
		labelDef:  labelDef,
		cfgRepo:   cfgRepo,
		resolver:  resolver,
		knownVals: make(map[string]struct{}),
	}
}

// Stats returns a snapshot of the consumer's counters.
func (c *Consumer) Stats() Stats {
	c.statsMu.RLock()
	defer c.statsMu.RUnlock()
	return c.stats
}

// statsAdd atomically applies a mutation to the Stats struct.
func (c *Consumer) statsAdd(fn func(*Stats)) {
	c.statsMu.Lock()
	fn(&c.stats)
	c.statsMu.Unlock()
}

// cursorKey is the config-table key used to persist this labeler's seq.
func (c *Consumer) cursorKey() string {
	return "labeler_cursor:" + c.config.LabelerDID
}

// Start runs the consumer: resolve the labeler host, backfill if needed,
// then stream updates with automatic reconnect until ctx is cancelled.
// Guarded by startOnce so a second Start call on the same Consumer is a
// no-op (mirrors Stop's sync.Once protection).
func (c *Consumer) Start(ctx context.Context) error {
	var started bool
	c.startOnce.Do(func() {
		started = true
	})
	if !started {
		return fmt.Errorf("labeler: Consumer already started")
	}

	c.clientMu.Lock()
	c.ctx, c.ctxCancel = context.WithCancel(ctx)
	c.clientMu.Unlock()

	// Resolve labeler host if not explicitly configured.
	if c.config.PDSHost == "" {
		host, err := c.resolveLabelerHost()
		if err != nil {
			return fmt.Errorf("resolve labeler %s: %w", c.config.LabelerDID, err)
		}
		c.config.PDSHost = host
	}
	slog.Info("Labeler host resolved",
		"did", c.config.LabelerDID,
		"host", c.config.PDSHost,
	)

	// One-time backfill if we have no stored cursor for this labeler.
	c.backfillIfNeeded()

	backoff := time.Second
	maxBackoff := 2 * time.Minute
	attempt := 0

	for {
		err := c.runOnce(c.ctx)

		select {
		case <-c.ctx.Done():
			c.finalFlush()
			return c.ctx.Err()
		default:
		}

		// OutdatedCursor recovery: the labeler has garbage-collected
		// seqs older than our stored cursor, so we cannot resume from
		// where we left off. Clear both the subscription cursor AND
		// any stale backfill checkpoint, re-run backfill to catch
		// up, and reconnect fresh.
		if errors.Is(err, errOutdatedCursor) {
			slog.Warn("Labeler cursor is outdated; clearing and re-backfilling",
				"did", c.config.LabelerDID)
			c.statsAdd(func(s *Stats) { s.OutdatedCursors++ })
			c.cursorMu.Lock()
			c.cursor = 0
			c.cursorMu.Unlock()
			if cerr := c.clearCursor(c.ctx); cerr != nil {
				slog.Warn("Failed to clear labeler cursor on OutdatedCursor recovery",
					"did", c.config.LabelerDID, "error", cerr)
			}
			c.clearBackfillCursor(c.ctx)
			c.backfillIfNeeded()
		}

		attempt++
		c.statsAdd(func(s *Stats) { s.ReconnectAttempts++ })

		if err != nil {
			slog.Error("Labeler subscription lost, will reconnect",
				"did", c.config.LabelerDID,
				"attempt", attempt,
				"error", err,
				"backoff", backoff)
		} else {
			slog.Warn("Labeler subscription closed, will reconnect",
				"did", c.config.LabelerDID,
				"attempt", attempt,
				"backoff", backoff)
		}

		select {
		case <-c.ctx.Done():
			c.finalFlush()
			return c.ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		slog.Info("Reconnecting to labeler",
			"did", c.config.LabelerDID, "attempt", attempt+1)
	}
}

// runOnce opens one websocket connection and processes events until it
// disconnects. processEvents and cursorFlusher receive the current client
// and the current generation's context by parameter, so they can never
// observe a later generation's state. On return, the client is always
// stopped so its event channel closes and processEvents exits cleanly.
func (c *Consumer) runOnce(ctx context.Context) error {
	// Stop any lingering client from a previous generation.
	c.clientMu.Lock()
	if c.client != nil {
		c.client.Stop()
		c.client = nil
	}
	c.clientMu.Unlock()

	cursor, err := c.loadCursor(ctx)
	if err != nil {
		slog.Debug("No stored labeler cursor, starting live",
			"did", c.config.LabelerDID)
	} else if cursor > 0 {
		slog.Debug("Resuming labeler from cursor",
			"did", c.config.LabelerDID, "cursor", cursor)
	}

	client := NewClient(ClientConfig{
		PDSHost: c.config.PDSHost,
		Cursor:  cursor,
	})

	// Connect before publishing the client to c.client so that a dead
	// client (failed connect) never leaks into shared state.
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	c.clientMu.Lock()
	c.client = client
	c.clientMu.Unlock()

	// Each generation gets its own flusher-stop channel. When Run returns
	// we close it so the flusher for this generation exits; the next
	// generation will spawn a fresh one.
	genDone := make(chan struct{})
	if !c.config.DisableCursor {
		go c.cursorFlusher(ctx, genDone)
	}
	go c.processEvents(ctx, client)

	runErr := client.Run(ctx)
	close(genDone)

	// Always stop the client so its events channel closes and
	// processEvents (which ranges over it) can exit. Without this, a
	// Run() that returns an error would leave the goroutine stuck.
	client.Stop()

	return runErr
}

// Stop stops the consumer. Safe to call multiple times.
func (c *Consumer) Stop() {
	c.stopOnce.Do(func() {
		c.clientMu.Lock()
		if c.ctxCancel != nil {
			c.ctxCancel()
		}
		client := c.client
		c.client = nil
		c.clientMu.Unlock()

		if client != nil {
			client.Stop()
		}
	})
}

// LabelerDID returns the DID this consumer is subscribed to.
func (c *Consumer) LabelerDID() string {
	return c.config.LabelerDID
}

// resolveLabelerHost resolves the labeler's DID and returns the host we
// should connect to for subscribeLabels / queryLabels. Per the ATProto
// spec, labelers advertise an AtprotoLabeler service entry; we prefer that
// and fall back to AtprotoPersonalDataServer for moderation services that
// co-locate labeler and PDS on the same host.
func (c *Consumer) resolveLabelerHost() (string, error) {
	doc, err := c.resolver.ResolveDID(c.config.LabelerDID)
	if err != nil {
		return "", err
	}
	if host := doc.GetLabelerEndpoint(); host != "" {
		return host, nil
	}
	if host := doc.GetPDSEndpoint(); host != "" {
		slog.Warn("Labeler DID has no AtprotoLabeler service, falling back to PDS endpoint",
			"did", c.config.LabelerDID)
		return host, nil
	}
	return "", fmt.Errorf("no AtprotoLabeler or AtprotoPersonalDataServer endpoint on %s", c.config.LabelerDID)
}

// runBackfill fetches every existing label from queryLabels and upserts.
// backfillIfNeeded runs the initial queryLabels backfill when the
// stored cursor is zero (either the first start for a labeler, or
// after OutdatedCursor recovery). A non-zero cursor means we already
// have a complete view up to that seq and can go straight to the
// subscription stream.
func (c *Consumer) backfillIfNeeded() {
	if c.config.SkipBackfill || c.config.DisableCursor {
		return
	}
	existing, err := c.loadCursor(c.ctx)
	if err != nil {
		slog.Debug("No stored labeler cursor, will run backfill",
			"did", c.config.LabelerDID)
		existing = 0
	}
	if existing != 0 {
		return
	}
	slog.Info("Running labeler backfill", "did", c.config.LabelerDID)
	if err := c.runBackfill(c.ctx); err != nil {
		slog.Error("Labeler backfill failed",
			"did", c.config.LabelerDID, "error", err)
		// Fall through: start the live subscription anyway.
	}
}

// backfillCursorKey is the config-table key used to checkpoint the
// queryLabels pagination cursor during a backfill. It's distinct from
// the subscription seq cursor (cursorKey) because the two come from
// different spaces — the backfill cursor is the opaque string returned
// by the labeler's queryLabels response, while the subscription cursor
// is an int64 seq.
func (c *Consumer) backfillCursorKey() string {
	return "labeler_backfill_cursor:" + c.config.LabelerDID
}

func (c *Consumer) loadBackfillCursor(ctx context.Context) string {
	v, err := c.cfgRepo.Get(ctx, c.backfillCursorKey())
	if err != nil {
		return ""
	}
	return v
}

func (c *Consumer) saveBackfillCursor(ctx context.Context, cursor string) {
	if err := c.cfgRepo.Set(ctx, c.backfillCursorKey(), cursor); err != nil {
		slog.Warn("Failed to checkpoint backfill cursor",
			"did", c.config.LabelerDID, "error", err)
	}
}

func (c *Consumer) clearBackfillCursor(ctx context.Context) {
	if err := c.cfgRepo.Delete(ctx, c.backfillCursorKey()); err != nil {
		slog.Debug("Failed to clear backfill cursor on completion",
			"did", c.config.LabelerDID, "error", err)
	}
}

func (c *Consumer) runBackfill(ctx context.Context) error {
	bf := NewBackfillClient(c.config.PDSHost)
	var total, totalRejected int
	lastLogged := 0
	pagesSinceCheckpoint := 0

	// Resume from the last checkpointed page so a crashed backfill
	// doesn't replay completed work on restart. Combined with the
	// partial unique indexes from migration 007 this makes backfill
	// fully idempotent — both correct AND efficient.
	resumeFrom := c.loadBackfillCursor(ctx)
	if resumeFrom != "" {
		slog.Info("Resuming labeler backfill from checkpoint",
			"did", c.config.LabelerDID)
	}

	err := bf.Fetch(ctx, []string{c.config.LabelerDID}, resumeFrom, func(ctx context.Context, labels []protoLabel, nextCursor string) error {
		rejected := c.upsertLabels(ctx, labels)
		total += len(labels)
		totalRejected += rejected

		c.statsAdd(func(s *Stats) {
			s.LabelsReceived += int64(len(labels))
			s.LabelsPersisted += int64(len(labels) - rejected)
			s.LabelsRejected += int64(rejected)
		})

		if total-lastLogged >= BackfillProgressInterval {
			slog.Info("Labeler backfill progress",
				"did", c.config.LabelerDID,
				"received", total,
				"rejected", totalRejected)
			lastLogged = total
		}

		// Checkpoint the queryLabels pagination cursor every N pages
		// so an interrupted run can resume without re-fetching the
		// completed prefix. We deliberately don't checkpoint every
		// page — excess DB writes — but we also don't wait for the
		// whole run, which could be hours on a large labeler.
		pagesSinceCheckpoint++
		if nextCursor != "" && pagesSinceCheckpoint >= BackfillCheckpointInterval {
			c.saveBackfillCursor(ctx, nextCursor)
			pagesSinceCheckpoint = 0
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("Labeler backfill complete",
		"did", c.config.LabelerDID,
		"received", total,
		"rejected", totalRejected)

	// Backfill done — drop the checkpoint key and persist the
	// subscription sentinel so the next start goes straight to the
	// live stream.
	c.clearBackfillCursor(ctx)
	if total > 0 && !c.config.DisableCursor {
		// Use a sentinel (1) so loadCursor returns non-zero and we
		// won't re-run backfill on restart. The subscription starts
		// from "live" because the labeler's real seq numbers are
		// much larger.
		if err := c.saveCursor(ctx, 1); err != nil {
			slog.Warn("Failed to persist backfill sentinel",
				"did", c.config.LabelerDID, "error", err)
		}
	}
	return nil
}

// processEvents drains decoded #labels frames from a specific client.
// The client and ctx are passed explicitly (not read from the Consumer)
// so that a stale goroutine from a previous reconnect cycle never touches
// the current generation's state. The select on ctx.Done() guarantees
// exit even if Client.Stop is racing with the range loop.
func (c *Consumer) processEvents(ctx context.Context, client *Client) {
	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-events:
			if !ok {
				return
			}
			c.handleLabelMessage(ctx, msg)
		}
	}
}

// handleLabelMessage persists a single #labels frame's worth of labels
// and updates the cursor + stats. upsertLabels logs its own batch-level
// summary on failures; we only record stats here.
func (c *Consumer) handleLabelMessage(ctx context.Context, msg *LabelMessage) {
	rejected := c.upsertLabels(ctx, msg.Labels)

	c.statsAdd(func(s *Stats) {
		s.LabelsReceived += int64(len(msg.Labels))
		s.LabelsPersisted += int64(len(msg.Labels) - rejected)
		s.LabelsRejected += int64(rejected)
		s.EventsReceived++
		s.LastSeq = msg.Seq
	})

	c.cursorMu.Lock()
	if msg.Seq > c.cursor {
		c.cursor = msg.Seq
	}
	c.cursorMu.Unlock()
}

// upsertLabels inserts every label in a batch, ensuring the label_definition
// row exists first. Individual label failures are rolled up into a single
// log line per batch (with the first failing label as a sample) so that
// sustained DB issues don't produce O(batch-size) log spam. Context
// cancellation is treated as expected shutdown, not an error.
//
// Returns the count of labels that were rejected rather than successfully
// persisted. A rejected count is not treated as an error here; callers log
// the summary via the batch-level metrics.
func (c *Consumer) upsertLabels(ctx context.Context, labels []protoLabel) (rejected int) {
	var firstErr error
	var firstErrURI, firstErrVal string

	record := func(err error, l *protoLabel) {
		rejected++
		if firstErr == nil {
			firstErr = err
			firstErrURI = l.URI
			firstErrVal = l.Val
		}
	}

	var accountLevelSkipped int
	for i := range labels {
		l := &labels[i]
		if ctx.Err() != nil {
			rejected++
			continue
		}
		// Track the account-level case separately so operators can
		// see at a glance whether a labeler is mostly sending
		// unreachable labels (rather than just grepping debug logs).
		if l != nil && l.URI != "" && !strings.HasPrefix(l.URI, "at://") {
			accountLevelSkipped++
		}
		if !c.validateLabel(l) {
			rejected++
			continue
		}
		if err := c.ensureDefinition(ctx, l.Src, l.Val); err != nil {
			record(err, l)
			continue
		}

		cts := parseLabelTime(l.Cts)

		if l.Neg {
			if _, err := c.labels.InsertNegation(ctx, l.Src, l.URI, l.Val, cts); err != nil {
				record(err, l)
			}
			continue
		}

		var cidPtr *string
		if l.CID != "" {
			cid := l.CID
			cidPtr = &cid
		}
		expPtr := parseLabelTime(l.Exp)

		if _, err := c.labels.Insert(ctx, l.Src, l.URI, cidPtr, l.Val, cts, expPtr); err != nil {
			record(err, l)
		}
	}

	if firstErr != nil && !errors.Is(firstErr, context.Canceled) {
		slog.Warn("Labeler: batch upsert had failures",
			"did", c.config.LabelerDID,
			"rejected", rejected,
			"total", len(labels),
			"first_err_uri", firstErrURI,
			"first_err_val", firstErrVal,
			"first_err", firstErr)
	}
	if accountLevelSkipped > 0 {
		c.statsAdd(func(s *Stats) {
			s.AccountLevelSkipped += int64(accountLevelSkipped)
		})
	}
	return rejected
}

// validateLabel rejects protocol-invalid labels before they touch the DB.
//
// Account-level labels (uri = "did:plc:...") are stored in the DB but
// cannot be reached by record-level queries (the record JOIN only
// matches at:// URIs), so we reject them here with a debug log. If the
// data model gains account-level label support in the future, this
// check can be relaxed.
//
// Each stringy field also has a byte-length cap so a malicious labeler
// can't bloat our DB with multi-megabyte values on any of src/uri/cid/val.
func (c *Consumer) validateLabel(l *protoLabel) bool {
	if l == nil || l.Src == "" || l.URI == "" || l.Val == "" {
		return false
	}
	if len(l.Src) > MaxLabelSrcLen {
		slog.Warn("Labeler: skipping label with oversized src",
			"did", c.config.LabelerDID, "src_len", len(l.Src))
		return false
	}
	if !strings.HasPrefix(l.URI, "at://") || len(l.URI) <= len("at://") {
		slog.Debug("Labeler: skipping label with non-record URI",
			"did", c.config.LabelerDID, "uri", l.URI)
		return false
	}
	if len(l.URI) > MaxLabelURILen {
		slog.Warn("Labeler: skipping label with oversized uri",
			"did", c.config.LabelerDID, "uri_len", len(l.URI))
		return false
	}
	if l.CID != "" && len(l.CID) > MaxLabelCIDLen {
		slog.Warn("Labeler: skipping label with oversized cid",
			"did", c.config.LabelerDID, "cid_len", len(l.CID))
		return false
	}
	if len(l.Val) > MaxLabelValLen {
		slog.Warn("Labeler: skipping label with oversized val",
			"did", c.config.LabelerDID, "val_len", len(l.Val))
		return false
	}
	return true
}

// parseLabelTime parses an ATProto-formatted timestamp string. Returns nil
// if the string is empty or malformed; callers use nil to mean "fall back
// to the DB default / leave NULL".
func parseLabelTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	return nil
}

// ensureDefinition upserts a label_definition row for a specific
// (labeler src, val) pair. Post-issue-#2 the label_definition PK is
// composite, so we no longer silently discard a second labeler's
// semantics when two labelers happen to emit the same val — each
// gets its own row keyed by (src, val).
//
// Results are memoised per-process via a FIFO cache bounded at
// MaxKnownVals: when full, the oldest entry is evicted. This keeps
// the fast path active even when a labeler emits more than
// MaxKnownVals distinct values, rather than the old "overflow =
// permanent slow path" behaviour.
func (c *Consumer) ensureDefinition(ctx context.Context, src, val string) error {
	cacheKey := src + "\x00" + val

	c.knownValsMu.Lock()
	if _, ok := c.knownVals[cacheKey]; ok {
		c.knownValsMu.Unlock()
		return nil
	}
	c.knownValsMu.Unlock()

	exists, err := c.labelDef.Exists(ctx, src, val)
	if err != nil {
		return err
	}
	if !exists {
		if err := c.labelDef.Insert(ctx, src, val, "", repositories.SeverityInform, repositories.VisibilityWarn); err != nil {
			// Another racing insert could have created it; tolerate that.
			if existsAfter, checkErr := c.labelDef.Exists(ctx, src, val); checkErr != nil || !existsAfter {
				return err
			}
		}
	}

	c.knownValsMu.Lock()
	defer c.knownValsMu.Unlock()
	if _, ok := c.knownVals[cacheKey]; ok {
		return nil
	}
	if len(c.knownVals) >= MaxKnownVals && len(c.knownValsOrder) > 0 {
		oldest := c.knownValsOrder[0]
		c.knownValsOrder = c.knownValsOrder[1:]
		delete(c.knownVals, oldest)
	}
	c.knownVals[cacheKey] = struct{}{}
	c.knownValsOrder = append(c.knownValsOrder, cacheKey)
	return nil
}

// cursorFlusher writes the current cursor to the config table every tick
// until either the parent ctx is cancelled or genDone is closed (signaling
// that this generation's client has exited and we should let the next
// generation's flusher take over). The final flush uses a bounded context
// so it cannot hang forever if the DB is stuck during shutdown.
func (c *Consumer) cursorFlusher(ctx context.Context, genDone <-chan struct{}) {
	ticker := time.NewTicker(c.config.CursorFlushInterval)
	defer ticker.Stop()

	var lastFlushed int64
	for {
		select {
		case <-ctx.Done():
			c.finalFlush()
			return
		case <-genDone:
			c.finalFlush()
			return
		case <-ticker.C:
			c.cursorMu.Lock()
			cursor := c.cursor
			c.cursorMu.Unlock()
			if cursor > lastFlushed {
				if err := c.saveCursor(ctx, cursor); err != nil {
					slog.Warn("Failed to save labeler cursor",
						"did", c.config.LabelerDID, "error", err)
				} else {
					lastFlushed = cursor
				}
			}
		}
	}
}

// finalFlush writes the current cursor with a bounded timeout. Called from
// shutdown paths after the parent context has been cancelled; uses a fresh
// context so the write isn't pre-empted by the cancellation.
func (c *Consumer) finalFlush() {
	c.cursorMu.Lock()
	cursor := c.cursor
	c.cursorMu.Unlock()
	if cursor <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.saveCursor(ctx, cursor); err != nil {
		slog.Warn("Failed to flush labeler cursor on shutdown",
			"did", c.config.LabelerDID, "error", err)
	}
}

func (c *Consumer) loadCursor(ctx context.Context) (int64, error) {
	value, err := c.cfgRepo.Get(ctx, c.cursorKey())
	if err != nil {
		return 0, err
	}
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse labeler cursor %q: %w", value, err)
	}
	return n, nil
}

func (c *Consumer) saveCursor(ctx context.Context, cursor int64) error {
	return c.cfgRepo.Set(ctx, c.cursorKey(), strconv.FormatInt(cursor, 10))
}

// clearCursor deletes the cursor row for this labeler so the next
// Start detects "no cursor" and re-runs backfill. Used by the
// OutdatedCursor recovery path and by the admin reset mutation.
func (c *Consumer) clearCursor(ctx context.Context) error {
	return c.cfgRepo.Delete(ctx, c.cursorKey())
}
