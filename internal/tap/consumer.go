package tap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"

	"github.com/GainForest/hypergoat/internal/consumer"
)

// EventHandler processes Tap events.
type EventHandler interface {
	HandleRecord(ctx context.Context, event *RecordEvent) error
	HandleIdentity(ctx context.Context, event *IdentityEvent) error
}

// ConsumerConfig configures the Tap consumer.
type ConsumerConfig struct {
	// TapURL is the Tap WebSocket URL (e.g., "wss://tap:2480").
	TapURL string
	// DisableAcks disables ack-based delivery (fire-and-forget).
	DisableAcks bool
	// MaxRetries is the maximum number of retries per event (default 3).
	MaxRetries int
}

// Consumer connects to a Tap sidecar and processes events.
type Consumer struct {
	config  ConsumerConfig
	handler EventHandler
	dialer  Dialer
}

// NewConsumer creates a new Tap consumer.
func NewConsumer(config ConsumerConfig, handler EventHandler) *Consumer {
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	return &Consumer{
		config:  config,
		handler: handler,
		dialer:  &DefaultDialer{},
	}
}

// NewConsumerWithDialer creates a Tap consumer with a custom dialer (for testing).
func NewConsumerWithDialer(config ConsumerConfig, handler EventHandler, dialer Dialer) *Consumer {
	c := NewConsumer(config, handler)
	c.dialer = dialer
	return c
}

// Start begins consuming events from Tap with automatic reconnection.
func (c *Consumer) Start(ctx context.Context) error {
	return consumer.RunWithReconnect(ctx, c.runOnce, consumer.BackoffOpts{})
}

// runOnce connects to Tap, processes events until disconnection, then returns.
func (c *Consumer) runOnce(ctx context.Context) error {
	url := c.config.TapURL + "/channel"
	slog.Info("Connecting to Tap", "url", url)

	conn, err := c.dialer.Dial(ctx, url)
	if err != nil {
		return fmt.Errorf("tap connect: %w", err)
	}
	defer conn.Close()

	slog.Info("Connected to Tap", "url", url)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Set read deadline.
		if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}

		event, err := ParseEvent(data)
		if err != nil {
			slog.Warn("Failed to parse Tap event, skipping", "error", err)
			continue
		}

		// Dispatch with panic recovery.
		if err := c.dispatch(ctx, event); err != nil {
			slog.Warn("Failed to process Tap event",
				"event_id", event.ID,
				"type", event.Type,
				"error", err,
			)
			// Don't ack failed events — they'll be redelivered on reconnect.
			continue
		}

		// Ack the event.
		if !c.config.DisableAcks {
			ackMsg := fmt.Sprintf("%d", event.ID)
			if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return fmt.Errorf("set write deadline: %w", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(ackMsg)); err != nil {
				return fmt.Errorf("write ack: %w", err)
			}
		}
	}
}

// dispatch processes a single event with panic recovery and retry.
func (c *Consumer) dispatch(ctx context.Context, event *Event) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in Tap event handler (recovered)",
				"event_id", event.ID,
				"panic", r,
			)
			retErr = fmt.Errorf("handler panic: %v", r)
		}
	}()

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second // 1s, 2s, 4s
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		eventCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := c.handleEvent(eventCtx, event)
		cancel()

		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("Tap event handler failed, retrying",
			"event_id", event.ID,
			"attempt", attempt+1,
			"max_retries", c.config.MaxRetries,
			"error", err,
		)
	}

	slog.Error("Tap event handler exhausted retries, skipping",
		"event_id", event.ID,
		"error", lastErr,
	)
	return lastErr
}

// handleEvent dispatches to the appropriate handler method.
func (c *Consumer) handleEvent(ctx context.Context, event *Event) error {
	switch event.Type {
	case EventTypeRecord:
		if event.Record == nil {
			return nil
		}
		return c.handler.HandleRecord(ctx, event.Record)
	case EventTypeIdentity:
		if event.Identity == nil {
			return nil
		}
		return c.handler.HandleIdentity(ctx, event.Identity)
	default:
		slog.Debug("Unknown Tap event type, skipping", "type", event.Type)
		return nil
	}
}
