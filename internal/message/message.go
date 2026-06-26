// Package message defines the wire protocol exchanged between clients and the
// server: the typed Envelope frames sent over the WebSocket and the persisted
// Message record. Keeping the protocol in one leaf package avoids import cycles
// (CODING-STANDARDS §7) since hub, web, store, and persist all depend on it.
package message

import "time"

// Type enumerates the kinds of frame that travel over a connection. It is the
// discriminator in an Envelope, so a receiver knows which payload to expect.
type Type string

// The frame types exchanged in both directions. join/leave/message originate from
// the client; history/presence/error originate from the server.
const (
	// TypeJoin is a client request to join a room (FR-3).
	TypeJoin Type = "join"
	// TypeLeave is a client request to leave a room (FR-4).
	TypeLeave Type = "leave"
	// TypeMessage carries chat text to or from a room (FR-5, FR-6).
	TypeMessage Type = "message"
	// TypeHistory carries recent messages sent to a client on join (FR-7).
	TypeHistory Type = "history"
	// TypePresence notifies a room that a user joined or left (FR-9).
	TypePresence Type = "presence"
	// TypeError reports a protocol or server error back to a single client.
	TypeError Type = "error"
)

// Envelope is the single frame type carried over the WebSocket in both directions.
// Type selects which of the optional fields are meaningful; unused fields are
// omitted from the JSON so frames stay small.
type Envelope struct {
	// Type discriminates the frame (see the Type constants).
	Type Type `json:"type"`
	// Room is the target/affected room for join/leave/message/presence frames.
	Room string `json:"room,omitempty"`
	// Text is the chat content for a message frame.
	Text string `json:"text,omitempty"`
	// Message is the populated record echoed to room members and used in history.
	Message *Message `json:"message,omitempty"`
	// History is the batch of recent messages sent on join (TypeHistory).
	History []Message `json:"history,omitempty"`
	// Presence describes a join/leave and the current member list (TypePresence).
	Presence *Presence `json:"presence,omitempty"`
	// Error is a human-readable reason for a TypeError frame.
	Error string `json:"error,omitempty"`
}

// Message is a single chat message as delivered to clients and persisted to
// Postgres. The same shape is stored, fetched as history, and broadcast live so
// there is one canonical representation (NFR-R5 ordering relies on CreatedAt + ID).
type Message struct {
	// ID is the database identity, set once the message is persisted.
	ID int64 `json:"id,omitempty"`
	// Room is the room the message belongs to.
	Room string `json:"room"`
	// SenderID is the stable user identity of the author (from session).
	SenderID string `json:"sender_id"`
	// SenderName is the author's chosen display name (FR-2).
	SenderName string `json:"sender_name"`
	// Content is the message body.
	Content string `json:"content"`
	// CreatedAt is when the server accepted the message.
	CreatedAt time.Time `json:"created_at"`
}

// Presence describes a membership change in a room and the resulting member list,
// so clients can render "X joined/left" and a live presence panel (FR-9).
type Presence struct {
	// User is the display name whose membership changed.
	User string `json:"user"`
	// Joined is true for a join, false for a leave.
	Joined bool `json:"joined"`
	// Members is the current list of display names in the room.
	Members []string `json:"members"`
}
