// Package bus is the broadcast seam that lets the chat scale beyond one process.
// The hub publishes every message through a MessageBus and delivers to local
// members from a Subscribe loop. In v1 LocalBus is an in-memory loopback; in v2 a
// Redis Pub/Sub implementation drops in with no hub changes (NFR-C3).
package bus

import (
	"context"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// MessageBus carries messages between the publishing path and the delivery loop.
// Implementations must be safe for concurrent Publish calls.
type MessageBus interface {
	// Publish hands a message to the bus for delivery to subscribers.
	Publish(ctx context.Context, m message.Message) error
	// Subscribe returns a channel of messages to deliver locally. The channel is
	// closed when ctx is cancelled, which is how the delivery loop stops (NFR-R1).
	Subscribe(ctx context.Context) (<-chan message.Message, error)
}

// LocalBus is the single-process MessageBus: Publish loops straight back to local
// subscribers over buffered channels. It is the v1 default.
type LocalBus struct {
	// buffer is the depth of each subscriber channel, so a brief delivery stall does
	// not block a publisher (leak-avoidance buffering, CODING-STANDARDS §4).
	buffer int
	// subscribers receive every published message; guarded by mu.
	// TODO(arfa): add `mu sync.Mutex` + `subscribers []chan message.Message` and
	// register/unregister channels in Subscribe so multi-room fan-out works.
}

// compile-time check that LocalBus satisfies the interface.
var _ MessageBus = (*LocalBus)(nil)

// NewLocal returns a LocalBus whose subscriber channels are buffered to depth.
func NewLocal(buffer int) *LocalBus {
	if buffer <= 0 {
		buffer = 1
	}
	return &LocalBus{buffer: buffer}
}

// Publish delivers m to every current local subscriber without blocking on a slow
// one (the slow-consumer rule applies here too, NFR-R2).
func (b *LocalBus) Publish(ctx context.Context, m message.Message) error {
	// TODO(arfa): non-blocking send to each registered subscriber channel.
	_ = ctx
	_ = m
	return nil
}

// Subscribe returns a buffered channel that yields published messages until ctx is
// cancelled, at which point the channel is closed and the subscriber deregistered.
func (b *LocalBus) Subscribe(ctx context.Context) (<-chan message.Message, error) {
	ch := make(chan message.Message, b.buffer)
	// TODO(arfa): register ch, then `go func(){ <-ctx.Done(); deregister; close(ch) }()`.
	_ = ctx
	return ch, nil
}
