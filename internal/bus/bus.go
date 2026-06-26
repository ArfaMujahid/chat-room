// Package bus is the broadcast seam that lets the chat scale beyond one process.
// The hub publishes every message through a MessageBus and delivers to local
// members from a Subscribe loop. In v1 LocalBus is an in-memory loopback; in v2 a
// Redis Pub/Sub implementation drops in with no hub changes (NFR-C3).
package bus

import (
	"context"
	"sync"

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

// LocalBus is the single-process MessageBus: Publish fans straight back to every
// local subscriber over buffered channels. It is the v1 default and is safe for
// concurrent use.
type LocalBus struct {
	// buffer is the depth of each subscriber channel, so a brief delivery stall does
	// not immediately drop messages (leak-avoidance buffering, CODING-STANDARDS §4).
	buffer int
	// mu guards subscribers against concurrent Publish/Subscribe/cancel.
	mu sync.Mutex
	// subscribers is the set of active delivery channels. The bus is the sole sender
	// to these channels, so it is also the only closer of them (§4).
	subscribers map[chan message.Message]struct{}
}

// compile-time check that LocalBus satisfies the interface.
var _ MessageBus = (*LocalBus)(nil)

// NewLocal returns a LocalBus whose subscriber channels are buffered to depth.
func NewLocal(buffer int) *LocalBus {
	if buffer <= 0 {
		buffer = 1
	}
	return &LocalBus{
		buffer:      buffer,
		subscribers: make(map[chan message.Message]struct{}),
	}
}

// Publish delivers m to every current subscriber with a non-blocking send, so a slow
// subscriber's full buffer drops this message for that subscriber rather than
// stalling the publisher or the other subscribers (the slow-consumer rule, NFR-R2).
func (b *LocalBus) Publish(_ context.Context, m message.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- m:
		default:
			// Subscriber is behind; skip it for this message. The hub's per-client
			// drop policy is the authoritative backpressure handler.
		}
	}
	return nil
}

// Subscribe registers a new buffered delivery channel and returns its receive end.
// A goroutine deregisters and closes the channel when ctx is cancelled, guaranteeing
// the subscriber's delivery loop terminates and nothing leaks (NFR-R1).
func (b *LocalBus) Subscribe(ctx context.Context) (<-chan message.Message, error) {
	ch := make(chan message.Message, b.buffer)

	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		// Remove before closing, under the same lock Publish holds, so no send can
		// race with the close (sends and close are serialized by mu).
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}
