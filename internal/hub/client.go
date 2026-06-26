package hub

import (
	"context"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// writeWait bounds a single write/ping to the connection. It is an internal protocol
// timeout (not a user-facing limit): without it, one stuck TCP write would pin the
// writePump indefinitely and defeat the slow-client drop (NFR-R2/S1).
const writeWait = 10 * time.Second

// Conn abstracts the one WebSocket connection a Client wraps, in terms of decoded
// protocol frames rather than bytes. It is defined here, on the consumer side, so the
// hub can be unit-tested with a fake connection and never touches a real socket
// (NFR-M1, CODING-STANDARDS §5). The web package adapts the chosen WebSocket library
// to this interface.
type Conn interface {
	// Read blocks until the next inbound frame arrives, or ctx/the connection ends.
	Read(ctx context.Context) (message.Envelope, error)
	// Write sends one frame to the peer. Only writePump calls this, guaranteeing a
	// single writer per connection (NFR-S1).
	Write(ctx context.Context, e message.Envelope) error
	// Ping sends a heartbeat and waits for the pong, for dead-connection detection
	// (FR-13, NFR-S3).
	Ping(ctx context.Context) error
	// Close terminates the connection, unblocking any in-flight Read.
	Close() error
}

// Client is one connected user: a Conn plus a buffered send channel and the two pump
// goroutines that own it. The hub only ever touches the send channel, never the
// socket directly, which keeps writes single-threaded (NFR-S1).
type Client struct {
	// ID is the stable identity of the user (the authenticated account ID). It is an
	// opaque string to the hub, which keeps the hub independent of the auth package.
	ID string
	// Name is the display name shown on this client's messages.
	Name string
	// conn is the underlying connection, read by readPump and written by writePump.
	conn Conn
	// send is the per-client outbound queue. It is buffered and bounded; when it is
	// full the hub drops the client rather than block the room (NFR-R2, NFR-C4). The
	// hub is the sole sender and the sole closer of this channel (CODING-STANDARDS §4).
	send chan message.Envelope
	// pingInterval is how often writePump sends a heartbeat ping (FR-13).
	pingInterval time.Duration
}

// NewClient wraps conn as a Client for user id/name, with a send buffer of the given
// depth and the given heartbeat interval. The buffer bound is what makes slow-client
// isolation possible.
func NewClient(id, name string, conn Conn, sendBuffer int, pingInterval time.Duration) *Client {
	return &Client{
		ID:           id,
		Name:         name,
		conn:         conn,
		send:         make(chan message.Envelope, sendBuffer),
		pingInterval: pingInterval,
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

// Run starts the write pump in its own goroutine and runs the read pump inline,
// returning only when the connection has ended. The hub closes the send channel on
// unregister, which is what ends the write pump; closing the connection ends the read
// pump — so neither goroutine outlives the connection (NFR-R1). web calls this once
// per accepted connection.
func (c *Client) Run(ctx context.Context, h *Hub) {
	go c.writePump(ctx)
	c.readPump(ctx, h)
}

// readPump reads frames from the connection and turns them into hub commands until
// the connection errors or ctx is cancelled, then it unregisters the client. It is
// the only goroutine that reads the connection (NFR-R1).
func (c *Client) readPump(ctx context.Context, h *Hub) {
	defer h.Unregister(c)
	for {
		env, err := c.conn.Read(ctx)
		if err != nil {
			return // connection closed or context cancelled — defer cleans up.
		}
		switch env.Type {
		case message.TypeJoin:
			c.handleJoin(ctx, h, env.Room)
		case message.TypeLeave:
			if env.Room != "" {
				h.Submit(command{kind: message.TypeLeave, client: c, room: env.Room})
			}
		case message.TypeMessage:
			if env.Room != "" && env.Text != "" {
				h.Submit(command{kind: message.TypeMessage, client: c, room: env.Room, text: env.Text})
			}
		default:
			// Unknown frame types from a client are ignored rather than fatal.
		}
	}
}

// handleJoin loads recent history for room, sends it to this client, then asks the
// hub to add the client to the room (FR-3, FR-7). History is fetched on this
// per-connection goroutine so the single hub goroutine never blocks on the database
// (NFR-P1). The history frame is enqueued before the join is submitted, so the client
// sees context before any live message.
func (c *Client) handleJoin(ctx context.Context, h *Hub, room string) {
	if room == "" {
		return
	}
	history, err := h.History(ctx, room)
	if err != nil {
		h.log.Error("hub: loading history", "room", room, "err", err)
		c.enqueue(message.Envelope{Type: message.TypeError, Room: room, Error: "could not load history"})
	} else if len(history) > 0 {
		c.enqueue(message.Envelope{Type: message.TypeHistory, Room: room, History: history})
	}
	h.Submit(command{kind: message.TypeJoin, client: c, room: room})
}

// writePump drains the send channel to the connection until the channel is closed (by
// the hub on unregister) or ctx is cancelled. It is the single writer (NFR-S1) and
// also emits periodic pings so dead connections are detected and reaped (FR-13). On
// exit it closes the connection, which unblocks readPump.
func (c *Client) writePump(ctx context.Context) {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	defer c.conn.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-c.send:
			if !ok {
				return // hub closed the channel — this client was removed.
			}
			if err := c.writeWithDeadline(ctx, env); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.pingWithDeadline(ctx); err != nil {
				return
			}
		}
	}
}

// writeWithDeadline writes one frame under writeWait, so a stalled connection cannot
// pin the write pump forever (NFR-R2).
func (c *Client) writeWithDeadline(ctx context.Context, env message.Envelope) error {
	wctx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	return c.conn.Write(wctx, env)
}

// pingWithDeadline sends a heartbeat under writeWait; a client that fails to pong in
// time is treated as dead and the write pump exits, tearing the connection down.
func (c *Client) pingWithDeadline(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	return c.conn.Ping(pctx)
}
