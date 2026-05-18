package jetstream

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/GainForest/hypergoat/internal/metrics"
)

const (
	// DefaultJetstreamURL is the default Jetstream endpoint.
	DefaultJetstreamURL = "wss://jetstream2.us-west.bsky.network/subscribe"

	// EventChannelBufferSize is the buffer size for the event channel between
	// the WebSocket reader and the consumer. Larger buffers absorb short bursts
	// but delay backpressure detection.
	EventChannelBufferSize = 1000

	// Default timeouts
	defaultWriteTimeout = 10 * time.Second
	defaultPongWait     = 60 * time.Second
	defaultPingPeriod   = 50 * time.Second
)

// ClientConfig configures the Jetstream client.
type ClientConfig struct {
	// URL is the Jetstream WebSocket endpoint.
	URL string

	// Collections to subscribe to (NSIDs).
	Collections []string

	// Cursor to resume from (microseconds timestamp).
	Cursor int64

	// DisableCursor disables cursor tracking (for development).
	DisableCursor bool
}

// Client connects to Jetstream and receives events.
type Client struct {
	config ClientConfig
	conn   *websocket.Conn
	mu     sync.Mutex

	// Event channel
	events chan *Event

	// Control channels
	done     chan struct{}
	stopOnce sync.Once
}

// NewClient creates a new Jetstream client.
func NewClient(config ClientConfig) *Client {
	if config.URL == "" {
		config.URL = DefaultJetstreamURL
	}

	// Publish the channel capacity once so operators can graph
	// utilisation as depth/capacity (Track 4 observability).
	metrics.JetstreamEventBufferCapacity(EventChannelBufferSize)

	return &Client{
		config: config,
		events: make(chan *Event, EventChannelBufferSize),
		done:   make(chan struct{}),
	}
}

// Events returns the channel of received events.
func (c *Client) Events() <-chan *Event {
	return c.events
}

// Connect establishes the WebSocket connection to Jetstream.
func (c *Client) Connect(ctx context.Context) error {
	u, err := c.buildURL()
	if err != nil {
		return fmt.Errorf("failed to build URL: %w", err)
	}

	slog.Info("Connecting to Jetstream", "url", u.String())

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Bound per-frame memory so a hostile Jetstream server can't
	// send a single multi-GB frame and exhaust heap before the
	// parser gets a chance to reject it. Jetstream events are
	// tiny in practice; 8 MiB is a comfortable ceiling.
	conn.SetReadLimit(maxJetstreamFrameSize)

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	slog.Info("Connected to Jetstream")
	return nil
}

// maxJetstreamFrameSize caps any single binary websocket frame.
const maxJetstreamFrameSize = 8 << 20

// buildURL constructs the Jetstream URL with query parameters.
func (c *Client) buildURL() (*url.URL, error) {
	u, err := url.Parse(c.config.URL)
	if err != nil {
		return nil, err
	}

	q := u.Query()

	// Add collection filters
	for _, col := range c.config.Collections {
		q.Add("wantedCollections", col)
	}

	// Add cursor if resuming
	if c.config.Cursor > 0 && !c.config.DisableCursor {
		q.Set("cursor", fmt.Sprintf("%d", c.config.Cursor))
	}

	u.RawQuery = q.Encode()
	return u, nil
}

// Run starts receiving events. Blocks until stopped or error.
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Set up ping/pong
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(defaultPongWait))
	})

	// Start ping sender
	go c.pingLoop(ctx)

	// Periodically publish the events-channel depth so operators
	// can see how close we are to backpressure (Track 4). 5s is
	// fine-grained enough to catch a producer surge before the
	// channel fills, coarse enough to skip in tight benchmarks.
	go c.bufferDepthLoop(ctx)

	// Read loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		default:
		}

		// Set read deadline
		if err := conn.SetReadDeadline(time.Now().Add(defaultPongWait)); err != nil {
			return fmt.Errorf("failed to set read deadline: %w", err)
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		event, err := ParseEvent(message)
		if err != nil {
			slog.Warn("Failed to parse event", "error", err)
			continue
		}

		// Send to event channel (blocking with context)
		select {
		case c.events <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// bufferDepthLoop emits the events-channel depth metric on a
// 5-second ticker. Stops on ctx.Done() or c.done.
func (c *Client) bufferDepthLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
			metrics.JetstreamEventBufferDepth(len(c.events))
		}
	}
}

// pingLoop sends periodic ping messages.
func (c *Client) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(defaultPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			conn := c.conn
			if conn == nil {
				c.mu.Unlock()
				return
			}

			err1 := conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
			var err2 error
			if err1 == nil {
				err2 = conn.WriteMessage(websocket.PingMessage, nil)
			}
			c.mu.Unlock()

			if err1 != nil {
				slog.Warn("Failed to set write deadline", "error", err1)
				return
			}
			if err2 != nil {
				slog.Warn("Failed to send ping", "error", err2)
				return
			}
		}
	}
}

// Stop closes the connection and stops receiving events.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		close(c.done)

		c.mu.Lock()
		conn := c.conn
		c.conn = nil
		if conn != nil {
			_ = conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			)
			_ = conn.Close()
		}
		c.mu.Unlock()

		close(c.events)
	})
}

// UpdateCursor updates the cursor for the next reconnection.
func (c *Client) UpdateCursor(cursor int64) {
	c.mu.Lock()
	c.config.Cursor = cursor
	c.mu.Unlock()
}
