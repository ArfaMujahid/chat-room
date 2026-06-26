package web

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"

	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/message"
)

// wsConn adapts a coder/websocket connection to the hub.Conn interface, translating
// between JSON-encoded text frames on the wire and decoded message.Envelope values
// the hub works with. This adapter is the only place the WebSocket library leaks into
// the codebase, keeping the hub library-agnostic and unit-testable (NFR-M1).
type wsConn struct {
	conn *websocket.Conn
}

// compile-time check that wsConn satisfies the hub's connection seam.
var _ hub.Conn = (*wsConn)(nil)

// Read blocks for the next text frame and decodes it into an Envelope. A malformed
// frame returns an error, which the read pump treats as fatal — garbage input drops
// the connection rather than being silently tolerated (NFR-X2).
func (c *wsConn) Read(ctx context.Context) (message.Envelope, error) {
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return message.Envelope{}, err
	}
	var env message.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return message.Envelope{}, fmt.Errorf("decoding inbound frame: %w", err)
	}
	return env, nil
}

// Write encodes env as JSON and sends it as a single text frame. Only the hub's write
// pump calls this, so there is exactly one writer per connection (NFR-S1).
func (c *wsConn) Write(ctx context.Context, env message.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encoding outbound frame: %w", err)
	}
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// Ping sends a WebSocket ping and waits for the matching pong (FR-13, NFR-S3).
func (c *wsConn) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}

// Close closes the connection with a normal-closure status.
func (c *wsConn) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}
