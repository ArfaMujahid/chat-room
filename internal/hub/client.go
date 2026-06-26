package hub

import (
	"context"

	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/session"
)

// Conn abstracts the one WebSocket connection a Client wraps. It is defined here, on
// the consumer side, so the hub can be unit-tested with a fake connection and never
// touches a real socket (NFR-M1, CODING-STANDARDS §5). The real implementation lives
// in the web package over the chosen WebSocket library.
type Conn interface {
	// Read blocks until the next inbound frame arrives or ctx/connection ends.
	Read(ctx context.Context) (message.Envelope, error)
	// Write sends one frame to the peer. Only writePump calls this, guaranteeing a
	// single writer per connection (NFR-S1).
	Write(ctx context.Context, e message.Envelope) error
	// Close terminates the connection, unblocking any in-flight Read.
	Close() error
}

// Client is one connected user: a Conn plus a buffered send channel and the two
// pump goroutines that own it. The hub only ever touches the send channel, never the
// socket directly, which keeps writes single-threaded (NFR-S1).
type Client struct {
	// ID is the stable session identity of the user (FR-2).
	ID session.UserID
	// Name is the chosen display name shown on this client's messages.
	Name string
	// conn is the underlying connection, written only by writePump.
	conn Conn
	// send is the per-client outbound queue. It is buffered and bounded; when it is
	// full the hub drops the client rather than block the room (NFR-R2, NFR-C4).
	send chan message.Envelope
}

// NewClient wraps conn as a Client for user id/name with a send buffer of the given
// depth. The buffer bound is what makes slow-client isolation possible.
func NewClient(id session.UserID, name string, conn Conn, sendBuffer int) *Client {
	return &Client{
		ID:   id,
		Name: name,
		conn: conn,
		send: make(chan message.Envelope, sendBuffer),
	}
}

// enqueue attempts a non-blocking send to the client's buffer. It returns false if
// the buffer is full, which the hub treats as "this client is too slow, drop it"
// (the defining chat-server correctness property, NFR-R2).
func (c *Client) enqueue(e message.Envelope) bool {
	select {
	case c.send <- e:
		return true
	default:
		return false
	}
}

// readPump reads frames from the connection and forwards them to the hub until the
// connection errors or ctx is cancelled, then it unregisters the client. This is the
// only goroutine that reads the connection (NFR-R1).
func (c *Client) readPump(ctx context.Context, h *Hub) {
	// TODO(arfa): loop Read(ctx); on each frame send to h.inbound; on error or
	// ctx.Done() call h.unregister(c) and return so the goroutine always exits.
	_ = ctx
	_ = h
}

// writePump drains the send channel to the connection until the channel is closed
// (by the hub on unregister) or ctx is cancelled. It is the single writer (NFR-S1)
// and also emits periodic pings for dead-connection detection (FR-13).
func (c *Client) writePump(ctx context.Context) {
	// TODO(arfa): for e := range c.send { c.conn.Write(ctx, e) }; ping on a ticker;
	// stop the ticker on exit so it does not leak (CODING-STANDARDS §6).
	_ = ctx
}

// Run starts the client's read and write pumps and blocks until both have exited,
// guaranteeing no goroutine outlives the connection (NFR-R1). main/web calls this
// once per accepted connection.
func (c *Client) Run(ctx context.Context, h *Hub) {
	// TODO(arfa): start writePump in a goroutine, run readPump inline (or use an
	// errgroup), then close(c.send) ownership stays with the hub on unregister.
	_ = ctx
	_ = h
}
