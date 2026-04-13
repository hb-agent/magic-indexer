package labeler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// (Package-wide defaults live in defaults.go.)

// errOutdatedCursor is returned from Client.Run when the labeler sends a
// #info frame with name "OutdatedCursor". The consumer catches this
// specific error, clears its stored cursor, and re-runs backfill on the
// next reconnect so nothing is silently lost.
var errOutdatedCursor = errors.New("labeler reported outdated cursor")

// ClientConfig configures a subscribeLabels websocket client.
type ClientConfig struct {
	// PDSHost is the host (e.g. https://mod.example.com) whose
	// /xrpc/com.atproto.label.subscribeLabels endpoint we connect to.
	// Both http(s) and ws(s) URLs are accepted; http(s) is converted.
	PDSHost string

	// Cursor is the subscribeLabels seq to resume from. Zero means "from now".
	Cursor int64
}

// LabelMessage is a decoded #labels frame ready for the consumer to process.
type LabelMessage struct {
	Seq    int64
	Labels []protoLabel
}

// Client opens a websocket to a labeler and streams decoded #labels frames.
type Client struct {
	config ClientConfig
	conn   *websocket.Conn
	mu     sync.Mutex

	events chan *LabelMessage

	done     chan struct{}
	stopOnce sync.Once
}

// NewClient creates a new labeler subscription client.
func NewClient(config ClientConfig) *Client {
	return &Client{
		config: config,
		events: make(chan *LabelMessage, EventChannelBufferSize),
		done:   make(chan struct{}),
	}
}

// Events returns the channel of decoded label messages.
func (c *Client) Events() <-chan *LabelMessage {
	return c.events
}

// Connect dials the labeler's subscribeLabels endpoint.
func (c *Client) Connect(ctx context.Context) error {
	u, err := c.buildURL()
	if err != nil {
		return fmt.Errorf("build URL: %w", err)
	}

	slog.Info("Connecting to labeler", "url", u.String())

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("dial labeler: %w", err)
	}

	// Bound per-frame memory to protect against malicious labelers.
	conn.SetReadLimit(MaxFrameSize)

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	slog.Info("Connected to labeler")
	return nil
}

// buildURL converts PDSHost to a wss:// subscribeLabels URL with cursor.
func (c *Client) buildURL() (*url.URL, error) {
	raw := c.config.PDSHost
	if raw == "" {
		return nil, fmt.Errorf("PDSHost is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https", "":
		u.Scheme = "wss"
	case "ws", "wss":
		// keep as-is
	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	u.Path = "/xrpc/com.atproto.label.subscribeLabels"

	q := u.Query()
	if c.config.Cursor > 0 {
		q.Set("cursor", fmt.Sprintf("%d", c.config.Cursor))
	}
	u.RawQuery = q.Encode()

	return u, nil
}

// Run reads frames until the connection closes or ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(defaultPongWait))
	})

	go c.pingLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(defaultPongWait)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}

		msgType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			// Surface non-normal close codes so operators can
			// distinguish a transient server bounce from a policy
			// violation, protocol error, or oversized frame.
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				slog.Warn("Labeler websocket closed with non-normal code",
					"code", closeErr.Code, "text", closeErr.Text)
			}
			return fmt.Errorf("read: %w", err)
		}

		// subscribeLabels uses binary frames.
		if msgType != websocket.BinaryMessage {
			slog.Debug("Skipping non-binary labeler frame", "type", msgType)
			continue
		}

		hdr, body, err := decodeFrame(message)
		if err != nil {
			slog.Warn("Failed to decode labeler frame", "error", err)
			continue
		}

		switch {
		case hdr.Op == 1 && hdr.T == "#labels":
			// A #labels frame with no body would decode into a
			// zero-value struct (Seq=0, Labels=nil), which could
			// quietly rewind our cursor. Reject it explicitly.
			if len(body) == 0 {
				slog.Warn("Labeler sent #labels frame with empty body; skipping")
				continue
			}
			lb, err := decodeLabelsBody(body)
			if err != nil {
				slog.Warn("Failed to decode labels body", "error", err)
				continue
			}
			select {
			case c.events <- &LabelMessage{Seq: lb.Seq, Labels: lb.Labels}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case hdr.Op == 1 && hdr.T == "#info":
			ib, decodeErr := decodeInfoBody(body)
			if decodeErr != nil {
				// Surface this at Warn: a corrupted #info frame can
				// mask an OutdatedCursor signal we depend on to
				// re-backfill. Operators need to see it.
				slog.Warn("Labeler info frame (undecoded)", "error", decodeErr)
				continue
			}
			slog.Info("Labeler info frame",
				"name", ib.Name, "message", ib.Message)
			// OutdatedCursor signals that our stored cursor is older
			// than anything the labeler can still serve. Treat it as
			// a hard signal to reset: return a sentinel error so the
			// consumer can clear the cursor and re-run backfill.
			if ib.Name == "OutdatedCursor" {
				return errOutdatedCursor
			}
		case hdr.Op == -1:
			if eb, err := decodeErrorBody(body); err == nil {
				slog.Warn("Labeler error frame",
					"code", eb.Error, "message", eb.Message)
				// The stream is effectively over after an error frame.
				return fmt.Errorf("labeler error: %s: %s", eb.Error, eb.Message)
			}
			return fmt.Errorf("labeler sent unrecoverable error frame")
		default:
			slog.Debug("Ignoring unknown labeler frame type", "op", hdr.Op, "t", hdr.T)
		}
	}
}

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
				slog.Warn("Labeler set write deadline", "error", err1)
				return
			}
			if err2 != nil {
				slog.Warn("Labeler ping failed", "error", err2)
				return
			}
		}
	}
}

// Stop closes the connection and the events channel.
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

// UpdateCursor updates the cursor used for the next reconnection.
func (c *Client) UpdateCursor(cursor int64) {
	c.mu.Lock()
	c.config.Cursor = cursor
	c.mu.Unlock()
}
