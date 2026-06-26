// Package hub is the engine of the chat server: a single actor goroutine owns the
// rooms→clients map and serializes every mutation through channels, so the broadcast
// path needs no mutexes and is race-free by construction (NFR-C2, architecture §6).
// Slow clients are isolated by a non-blocking broadcast (NFR-R2).
package hub

import (
	"context"
	"log/slog"

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

// Hub is the coordinator. Everything that mutates rooms/clients flows through its
// channels and is handled on the single Run goroutine.
type Hub struct {
	// register adds a newly connected client (no room yet).
	register chan *Client
	// unregister removes a client and pulls it from every room it was in.
	unregister chan *Client
	// inbound carries join/leave/message commands from clients' read pumps.
	inbound chan command
	// rooms maps room name to its membership set; owned by Run, never locked.
	rooms map[string]*room
	// store serves recent history on join (FR-7); read on the actor goroutine.
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
		rooms:        make(map[string]*room),
		store:        st,
		persist:      persist,
		historyLimit: historyLimit,
		log:          log,
	}
}

// Run is the actor loop. It owns all shared state and processes one event at a time
// until ctx is cancelled, at which point it returns so the process can shut down
// cleanly (FR-12, NFR-C2). It is started exactly once from main.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			_ = c
			// TODO(arfa): track the connected client (rooms are joined via inbound).
		case c := <-h.unregister:
			h.removeFromAllRooms(c)
		case cmd := <-h.inbound:
			h.handle(ctx, cmd)
		}
	}
}

// Register enqueues a newly connected client. Called by web once the WS upgrade and
// session resolution succeed.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister enqueues a client for removal. Called by the client's readPump when the
// connection ends, so the hub drops it from every room and broadcasts the leave.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// Submit hands a client command to the actor loop.
func (h *Hub) Submit(cmd command) {
	h.inbound <- cmd
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

// join adds the client to a room (created on first join, FR-3), then notifies the
// room of the new presence (FR-9). Sending recent history to the joining client is
// still stubbed.
func (h *Hub) join(cmd command) {
	r, ok := h.rooms[cmd.room]
	if !ok {
		r = newRoom(cmd.room)
		h.rooms[cmd.room] = r
	}
	r.add(cmd.client)
	// TODO(arfa): h.store.RecentByRoom(ctx, cmd.room, h.historyLimit) → send a
	// TypeHistory frame to cmd.client only, before announcing presence (FR-7).
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

// broadcastPresence notifies every member of r that user joined or left, attaching
// the current member list (FR-9). It uses the same non-blocking send as messages so
// a slow client cannot stall the announcement (NFR-R2).
func (h *Hub) broadcastPresence(r *room, user string, joined bool) {
	frame := message.Envelope{
		Type:     message.TypePresence,
		Room:     r.name,
		Presence: &message.Presence{User: user, Joined: joined, Members: r.names()},
	}
	for c := range r.members {
		if !c.enqueue(frame) {
			h.log.Warn("hub: dropping slow client", "user", c.Name, "room", r.name)
			r.remove(c)
		}
	}
}

// broadcast delivers a message to every member of its room on the HOT PATH using a
// non-blocking send: a client whose buffer is full is dropped and unregistered so it
// cannot stall the room (NFR-R2). It also hands the message to the persister on the
// COLD PATH (NFR-P1).
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
	}
	frame := message.Envelope{Type: message.TypeMessage, Room: cmd.room, Message: &m}
	for c := range r.members {
		if c == cmd.client {
			continue
		}
		if !c.enqueue(frame) {
			// Slow client: drop it rather than block everyone else (NFR-R2).
			h.log.Warn("hub: dropping slow client", "user", c.Name, "room", cmd.room)
			r.remove(c)
			// TODO(arfa): close the client's connection and unregister it fully.
		}
	}
	// COLD PATH — non-blocking hand-off so a full persist queue never stalls delivery.
	select {
	case h.persist <- m:
	case <-ctx.Done():
	default:
		h.log.Warn("hub: persist queue full, message not enqueued", "room", cmd.room)
	}
}

// removeFromAllRooms drops c from every room it belongs to, announces each leave
// (FR-9), and reclaims now-empty rooms. Runs on the actor goroutine (no locking).
func (h *Hub) removeFromAllRooms(c *Client) {
	for name, r := range h.rooms {
		if _, ok := r.members[c]; !ok {
			continue
		}
		r.remove(c)
		h.broadcastPresence(r, c.Name, false)
		if r.empty() {
			delete(h.rooms, name)
		}
	}
}
