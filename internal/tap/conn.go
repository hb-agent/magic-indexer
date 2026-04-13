package tap

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// Connection abstracts a WebSocket connection for testability.
type Connection interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// Dialer abstracts WebSocket connection establishment for testability.
type Dialer interface {
	Dial(ctx context.Context, url string) (Connection, error)
}

// DefaultDialer uses gorilla/websocket to establish connections.
type DefaultDialer struct{}

// Dial connects to the given WebSocket URL.
func (d *DefaultDialer) Dial(ctx context.Context, url string) (Connection, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, url, http.Header{})
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}
