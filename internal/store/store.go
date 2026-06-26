// Package store persists chat messages and serves recent history. The MessageStore
// interface is defined here, on the consumer side (the hub and persister depend on
// it), so the Postgres details stay swappable and tests can use a fake (NFR-M2,
// CODING-STANDARDS §5).
package store

import (
	"context"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// MessageStore durably stores accepted messages and returns recent history for a
// room. Implementations must be safe for concurrent use by multiple goroutines.
type MessageStore interface {
	// SaveMessage persists one message and returns it with its assigned ID set.
	SaveMessage(ctx context.Context, m message.Message) (message.Message, error)
	// RecentByRoom returns up to limit most-recent messages for room, in send order
	// (oldest first) so a joining client can render them top to bottom (FR-7).
	RecentByRoom(ctx context.Context, room string, limit int) ([]message.Message, error)
}
