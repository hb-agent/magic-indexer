package labeler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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

// Consumer runs one labeler subscription end-to-end: resolve DID, run the
// initial backfill, then stream updates with cursor-based resumption and
// exponential backoff on disconnect.
type Consumer struct {
	config   ConsumerConfig
	labels   *repositories.LabelsRepository
	labelDef *repositories.LabelDefinitionsRepository
	cfgRepo  *repositories.ConfigRepository
	resolver *oauth.DIDResolver

	client *Client

	cursor     int64
	cursorMu   sync.Mutex
	cursorDone chan struct{}

	// Auto-upsert bookkeeping so we don't hammer the DB with Exists checks
	// for label vals we've already seen in this process.
	knownValsMu sync.Mutex
	knownVals   map[string]struct{}

	ctx       context.Context
	ctxCancel context.CancelFunc
	clientMu  sync.Mutex
	running   bool
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
		config:     config,
		labels:     labels,
		labelDef:   labelDef,
		cfgRepo:    cfgRepo,
		resolver:   resolver,
		cursorDone: make(chan struct{}),
		knownVals:  make(map[string]struct{}),
	}
}

// cursorKey is the config-table key used to persist this labeler's seq.
func (c *Consumer) cursorKey() string {
	return "labeler_cursor:" + c.config.LabelerDID
}

// Start runs the consumer: resolve PDS, backfill if needed, then stream
// with auto-reconnect until ctx is cancelled.
func (c *Consumer) Start(ctx context.Context) error {
	c.clientMu.Lock()
	c.ctx, c.ctxCancel = context.WithCancel(ctx)
	c.running = true
	c.clientMu.Unlock()

	// Resolve PDS host if not explicitly configured.
	if c.config.PDSHost == "" {
		host, err := c.resolvePDS(c.ctx)
		if err != nil {
			return fmt.Errorf("resolve labeler %s: %w", c.config.LabelerDID, err)
		}
		c.config.PDSHost = host
	}
	slog.Info("Labeler PDS resolved",
		"did", c.config.LabelerDID,
		"host", c.config.PDSHost,
	)

	// One-time backfill if we have no stored cursor for this labeler.
	if !c.config.SkipBackfill && !c.config.DisableCursor {
		existing, err := c.loadCursor(c.ctx)
		if err != nil {
			slog.Warn("Failed to load labeler cursor, running backfill",
				"did", c.config.LabelerDID, "error", err)
			existing = 0
		}
		if existing == 0 {
			slog.Info("Running labeler backfill", "did", c.config.LabelerDID)
			if err := c.runBackfill(c.ctx); err != nil {
				slog.Error("Labeler backfill failed",
					"did", c.config.LabelerDID, "error", err)
				// Fall through and try subscription anyway.
			}
		}
	}

	backoff := time.Second
	maxBackoff := 2 * time.Minute

	for {
		err := c.runOnce(c.ctx)

		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
		}

		if err != nil {
			slog.Error("Labeler subscription lost, will reconnect",
				"did", c.config.LabelerDID, "error", err, "backoff", backoff)
		} else {
			slog.Warn("Labeler subscription closed, will reconnect",
				"did", c.config.LabelerDID, "backoff", backoff)
		}

		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		c.cursorDone = make(chan struct{})
		slog.Info("Reconnecting to labeler", "did", c.config.LabelerDID)
	}
}

// runOnce opens one connection and processes events until it disconnects.
func (c *Consumer) runOnce(ctx context.Context) error {
	c.clientMu.Lock()
	if c.client != nil {
		c.client.Stop()
		c.client = nil
	}
	c.clientMu.Unlock()

	cursor, err := c.loadCursor(ctx)
	if err != nil {
		slog.Debug("No stored labeler cursor, starting live",
			"did", c.config.LabelerDID, "error", err)
	} else if cursor > 0 {
		slog.Info("Resuming labeler from cursor",
			"did", c.config.LabelerDID, "cursor", cursor)
	}

	c.clientMu.Lock()
	c.client = NewClient(ClientConfig{
		PDSHost: c.config.PDSHost,
		Cursor:  cursor,
	})
	c.clientMu.Unlock()

	if err := c.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	if !c.config.DisableCursor {
		go c.cursorFlusher(ctx)
	}
	go c.processEvents(ctx)

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
	default:
		close(c.cursorDone)
	}
	if c.client != nil {
		c.client.Stop()
	}
}

// LabelerDID returns the DID this consumer is subscribed to.
func (c *Consumer) LabelerDID() string {
	return c.config.LabelerDID
}

func (c *Consumer) resolvePDS(_ context.Context) (string, error) {
	doc, err := c.resolver.ResolveDID(c.config.LabelerDID)
	if err != nil {
		return "", err
	}
	host := doc.GetPDSEndpoint()
	if host == "" {
		return "", fmt.Errorf("no AtprotoPersonalDataServer endpoint on %s", c.config.LabelerDID)
	}
	return host, nil
}

// runBackfill fetches every existing label from queryLabels and upserts.
func (c *Consumer) runBackfill(ctx context.Context) error {
	bf := NewBackfillClient(c.config.PDSHost)
	var total int
	err := bf.Fetch(ctx, []string{c.config.LabelerDID}, func(ctx context.Context, labels []ProtoLabel) error {
		if err := c.upsertLabels(ctx, labels); err != nil {
			return err
		}
		total += len(labels)
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("Labeler backfill complete",
		"did", c.config.LabelerDID, "count", total)

	// Persist a cursor so subsequent restarts skip the backfill. The
	// subscription seq and the backfill are disjoint number spaces, so we
	// only save a sentinel (-1) if backfill got no labels AND the cursor is
	// still unset; otherwise start the subscription from 0 (live) which the
	// labeler will happily accept.
	if total > 0 && !c.config.DisableCursor {
		// Use a sentinel (1) so loadCursor returns non-zero and we won't
		// re-run backfill on restart. The subscription starts from "live"
		// because the labeler's real seq numbers are much larger.
		if err := c.saveCursor(ctx, 1); err != nil {
			slog.Warn("Failed to persist backfill sentinel",
				"did", c.config.LabelerDID, "error", err)
		}
	}
	return nil
}

// processEvents drains decoded #labels frames from the client.
func (c *Consumer) processEvents(ctx context.Context) {
	for msg := range c.client.Events() {
		if err := c.upsertLabels(ctx, msg.Labels); err != nil {
			slog.Warn("Failed to persist labeler batch",
				"did", c.config.LabelerDID, "seq", msg.Seq, "error", err)
			continue
		}

		c.cursorMu.Lock()
		if msg.Seq > c.cursor {
			c.cursor = msg.Seq
		}
		c.cursorMu.Unlock()
	}
}

// upsertLabels inserts each label, ensuring the label_definition row exists first.
func (c *Consumer) upsertLabels(ctx context.Context, labels []ProtoLabel) error {
	for i := range labels {
		l := &labels[i]
		if l.Src == "" || l.URI == "" || l.Val == "" {
			continue
		}
		if err := c.ensureDefinition(ctx, l.Val); err != nil {
			return fmt.Errorf("ensure definition %q: %w", l.Val, err)
		}

		if l.Neg {
			if _, err := c.labels.InsertNegation(ctx, l.Src, l.URI, l.Val); err != nil {
				return fmt.Errorf("insert negation: %w", err)
			}
			continue
		}

		var cidPtr *string
		if l.CID != "" {
			cid := l.CID
			cidPtr = &cid
		}
		var expPtr *time.Time
		if l.Exp != "" {
			if t, err := time.Parse(time.RFC3339Nano, l.Exp); err == nil {
				expPtr = &t
			} else if t, err := time.Parse(time.RFC3339, l.Exp); err == nil {
				expPtr = &t
			}
		}

		if _, err := c.labels.Insert(ctx, l.Src, l.URI, cidPtr, l.Val, expPtr); err != nil {
			return fmt.Errorf("insert label: %w", err)
		}
	}
	return nil
}

// ensureDefinition upserts a label_definition row so the foreign key on
// label.val is always satisfied. Results are memoised per-process.
func (c *Consumer) ensureDefinition(ctx context.Context, val string) error {
	c.knownValsMu.Lock()
	if _, ok := c.knownVals[val]; ok {
		c.knownValsMu.Unlock()
		return nil
	}
	c.knownValsMu.Unlock()

	exists, err := c.labelDef.Exists(ctx, val)
	if err != nil {
		return err
	}
	if !exists {
		if err := c.labelDef.Insert(ctx, val, "", repositories.SeverityInform, repositories.VisibilityWarn); err != nil {
			// Another racing insert could have created it; tolerate that.
			if existsAfter, checkErr := c.labelDef.Exists(ctx, val); checkErr != nil || !existsAfter {
				return err
			}
		}
	}

	c.knownValsMu.Lock()
	c.knownVals[val] = struct{}{}
	c.knownValsMu.Unlock()
	return nil
}

// cursorFlusher writes the current cursor to the config table every tick.
func (c *Consumer) cursorFlusher(ctx context.Context) {
	ticker := time.NewTicker(c.config.CursorFlushInterval)
	defer ticker.Stop()

	var lastFlushed int64
	for {
		select {
		case <-ctx.Done():
			c.flushCursor(context.Background())
			return
		case <-c.cursorDone:
			c.flushCursor(context.Background())
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

func (c *Consumer) flushCursor(ctx context.Context) {
	c.cursorMu.Lock()
	cursor := c.cursor
	c.cursorMu.Unlock()
	if cursor > 0 {
		if err := c.saveCursor(ctx, cursor); err != nil {
			slog.Warn("Failed to flush labeler cursor",
				"did", c.config.LabelerDID, "error", err)
		}
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

// ErrNoPDS is returned when a labeler DID doesn't advertise a PDS endpoint.
var ErrNoPDS = errors.New("labeler has no PDS endpoint")
