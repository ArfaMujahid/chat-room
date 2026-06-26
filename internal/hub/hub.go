// Package hub is the engine of the chat server: a single actor goroutine owns the
// rooms→clients map and serializes every mutation through channels, so the broadcast
// path needs no mutexes and is race-free by construction (NFR-C2, architecture §6).
// Slow clients are isolated by a non-blocking broadcast (NFR-R2).
package hub

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/store"
)

// command is a request submitted to the hub goroutine. join/leave/message are
// expressed as small command structs sent over inbound so the actor processes them
// one at a time on its own goroutine.
type command struct {
	// kind selects which membership/broadcast action to take.
	kind message.Type
	// client is the originator of the command.
	client *Client
	// room is the target room for join/leave/message.
	room string
	// text is the message body for a message command.
	text string
}

// RoomInfo is a snapshot of one room for the REST API and metrics (FR-10).
type RoomInfo struct {
	// Name is the room's name.
	Name string `json:"name"`
	// Members is how many clients are currently in the room.
	Members int `json:"members"`
}

// Stats is a point-in-time snapshot of hub state for the REST API and observability
// (FR-10, NFR-O1). It is produced on the actor goroutine, so it never races the maps.
type Stats struct {
	// Connections is the number of currently connected clients.
	Connections int `json:"connections"`
	// Rooms lists active rooms with their member counts.
	Rooms []RoomInfo `json:"rooms"`
}

// Hub is the coordinator. Everything that mutates rooms/clients flows through its
// channels and is handled on the single Run goroutine.
type Hub struct {
	// register adds a newly connected client (not yet in any room).
	register chan *Client
	// unregister removes a client and pulls it from every room it was in.
	unregister chan *Client
	// inbound carries join/leave/message commands from clients' read pumps.
	inbound chan command
	// query carries snapshot requests; the actor replies on the supplied channel.
	query chan chan Stats
	// done is closed when Run returns, so senders never block after shutdown.
	done chan struct{}

	// clients is the set of all connected clients; owned by Run, never locked.
	clients map[*Client]struct{}
	// rooms maps room name to its membership set; owned by Run, never locked.
	rooms map[string]*room

	// store serves recent history on join (FR-7); read off the actor goroutine.
	store store.MessageStore
	// persist is the cold-path hand-off: accepted messages go here for async
	// durability so delivery never waits on the DB (NFR-P1).
	persist chan<- message.Message
	// historyLimit is how many recent messages a joining client receives.
	historyLimit int
	// log is the structured logger (NFR-U3).
	log *slog.Logger
}

// New constructs a Hub. The persist channel and store are injected so the hub codes
// against interfaces and tests can supply fakes (CODING-STANDARDS §7).
func New(st store.MessageStore, persist chan<- message.Message, historyLimit int, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		register:     make(chan *Client),
		unregister:   make(chan *Client),
		inbound:      make(chan command),
		query:        make(chan chan Stats),
		done:         make(chan struct{}),
		clients:      make(map[*Client]struct{}),
		rooms:        make(map[string]*room),
		store:        st,
		persist:      persist,
		historyLimit: historyLimit,
		log:          log,
	}
}

// Run is the actor loop. It owns all shared state and processes one event at a time
// until ctx is cancelled, at which point it returns so the process can shut down
// cleanly (FR-12, NFR-C2). It is started exactly once from main. Closing done on exit
// releases any client goroutine still trying to register/submit/unregister.
func (h *Hub) Run(ctx context.Context) {
	defer close(h.done)
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.clients[c] = struct{}{}
		case c := <-h.unregister:
			h.disconnect(c)
		case cmd := <-h.inbound:
			h.handle(ctx, cmd)
		case reply := <-h.query:
			reply <- h.snapshot()
		}
	}
}

// Register enqueues a newly connected client. Called by web once the WS upgrade and
// session resolution succeed. It returns without blocking if the hub has shut down.
func (h *Hub) Register(c *Client) {
	select {
	case h.register <- c:
	case <-h.done:
	}
}

// Unregister enqueues a client for removal. Called by a client's readPump when the
// connection ends. It returns without blocking if the hub has shut down.
func (h *Hub) Unregister(c *Client) {
	select {
	case h.unregister <- c:
	case <-h.done:
	}
}

// Submit hands a client command to the actor loop, returning without blocking if the
// hub has shut down.
func (h *Hub) Submit(cmd command) {
	select {
	case h.inbound <- cmd:
	case <-h.done:
	}
}

// Snapshot returns a consistent view of rooms and connection count, produced on the
// actor goroutine so it never races the maps (FR-10, NFR-O1). It returns the zero
// value if the hub has shut down or ctx is cancelled first.
func (h *Hub) Snapshot(ctx context.Context) Stats {
	reply := make(chan Stats, 1)
	select {
	case h.query <- reply:
	case <-ctx.Done():
		return Stats{}
	case <-h.done:
		return Stats{}
	}
	select {
	case s := <-reply:
		return s
	case <-ctx.Done():
		return Stats{}
	}
}

// History returns the recent messages a joining client should receive (FR-7). It is
// called off the actor goroutine (from a client's read pump), so a slow database
// query never stalls the hub (NFR-P1).
func (h *Hub) History(ctx context.Context, room string) ([]message.Message, error) {
	if h.store == nil || h.historyLimit <= 0 {
		return nil, nil
	}
	msgs, err := h.store.RecentByRoom(ctx, room, h.historyLimit)
	if err != nil {
		return nil, fmt.Errorf("loading history for room %q: %w", room, err)
	}
	return msgs, nil
}

// handle dispatches one command on the actor goroutine.
func (h *Hub) handle(ctx context.Context, cmd command) {
	switch cmd.kind {
	case message.TypeJoin:
		h.join(cmd)
	case message.TypeLeave:
		h.leave(cmd)
	case message.TypeMessage:
		h.broadcast(ctx, cmd)
	default:
		h.log.Warn("hub: unknown command", "kind", cmd.kind)
	}
}

// join adds the client to a room (created on first join, FR-3) and announces the new
// presence to the room (FR-9). Recent history is sent separately by the client's read
// pump before the join is submitted.
func (h *Hub) join(cmd command) {
	r, ok := h.rooms[cmd.room]
	if !ok {
		r = newRoom(cmd.room)
		h.rooms[cmd.room] = r
	}
	r.add(cmd.client)
	h.broadcastPresence(r, cmd.client.Name, true)
}

// leave removes the client from a room, announces the departure (FR-4/9), and
// reclaims the room once it is empty.
func (h *Hub) leave(cmd command) {
	r, ok := h.rooms[cmd.room]
	if !ok {
		return
	}
	r.remove(cmd.client)
	h.broadcastPresence(r, cmd.client.Name, false)
	if r.empty() {
		delete(h.rooms, r.name)
	}
}

// broadcast delivers a message to every other member of its room on the HOT PATH and
// hands it to the persister on the COLD PATH. Delivery is non-blocking: a member whose
// buffer is full is dropped so it cannot stall the room (NFR-R2). The server stamps
// the accept time once, so the broadcast and the stored copy share an ordering key
// (NFR-R5).
func (h *Hub) broadcast(ctx context.Context, cmd command) {
	r, ok := h.rooms[cmd.room]
	if !ok {
		return
	}
	m := message.Message{
		Room:       cmd.room,
		SenderID:   string(cmd.client.ID),
		SenderName: cmd.client.Name,
		Content:    cmd.text,
		CreatedAt:  time.Now().UTC(),
	}
	frame := message.Envelope{Type: message.TypeMessage, Room: cmd.room, Message: &m}
	h.deliverMessage(r, frame, cmd.client)

	// COLD PATH — non-blocking hand-off so a full persist queue never stalls delivery.
	select {
	case h.persist <- m:
	case <-ctx.Done():
	default:
		h.log.Warn("hub: persist queue full, message dropped from persistence", "room", cmd.room)
	}
}

// deliverMessage enqueues frame to every member of r except the sender, then fully
// disconnects any member whose buffer was full (NFR-R2). Slow clients are collected
// first and dropped after the loop, so the membership map is not mutated mid-range by
// a re-entrant presence broadcast.
func (h *Hub) deliverMessage(r *room, frame message.Envelope, except *Client) {
	var slow []*Client
	for c := range r.members {
		if c == except {
			continue
		}
		if !c.enqueue(frame) {
			slow = append(slow, c)
		}
	}
	for _, c := range slow {
		h.log.Warn("hub: dropping slow client", "user", c.Name, "room", r.name)
		h.disconnect(c)
	}
}

// broadcastPresence notifies every member of r that user joined or left, attaching the
// current member list (FR-9). It is best-effort: a member whose buffer is full simply
// misses this frame (and will be dropped on its next failed message delivery), which
// keeps presence broadcasts from recursing into disconnect.
func (h *Hub) broadcastPresence(r *room, user string, joined bool) {
	frame := message.Envelope{
		Type:     message.TypePresence,
		Room:     r.name,
		Presence: &message.Presence{User: user, Joined: joined, Members: r.names()},
	}
	for c := range r.members {
		c.enqueue(frame)
	}
}

// disconnect fully removes c: from the global client set, from every room (announcing
// each leave), and finally closes its send channel so its write pump exits (NFR-R1).
// It is idempotent via the client-set membership guard, so the slow-client path and
// the read pump's unregister can both call it without a double close (CODING-STANDARDS
// §4). Runs only on the actor goroutine.
func (h *Hub) disconnect(c *Client) {
	if _, ok := h.clients[c]; !ok {
		return // already removed
	}
	delete(h.clients, c)
	for name, r := range h.rooms {
		if _, in := r.members[c]; !in {
			continue
		}
		r.remove(c)
		h.broadcastPresence(r, c.Name, false)
		if r.empty() {
			delete(h.rooms, name)
		}
	}
	close(c.send)
}

// snapshot builds a Stats view of the current rooms and connection count. Runs on the
// actor goroutine (no locking).
func (h *Hub) snapshot() Stats {
	rooms := make([]RoomInfo, 0, len(h.rooms))
	for name, r := range h.rooms {
		rooms = append(rooms, RoomInfo{Name: name, Members: len(r.members)})
	}
	return Stats{Connections: len(h.clients), Rooms: rooms}
}
