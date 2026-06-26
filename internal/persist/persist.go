// Package persist is the cold-path persistence worker. It drains accepted messages
// off a channel and writes them to the MessageStore, so message delivery (the hot
// path) is never blocked by database latency (NFR-P1, architecture §3). On graceful
// shutdown it drains its queue before exiting so accepted messages are not lost
// (NFR-R4).
package persist

import (
	"context"
	"log/slog"

	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/store"
)

// Persister is the async worker that moves messages from the hub to durable storage.
type Persister struct {
	// in is the queue the hub publishes accepted messages to (cold path).
	in chan message.Message
	// store is where messages are durably written.
	store store.MessageStore
	// log is the structured logger (NFR-U3).
	log *slog.Logger
}

// New constructs a Persister with an inbound queue of the given depth. The buffer
// absorbs DB latency spikes so the hub's non-blocking hand-off rarely drops.
func New(st store.MessageStore, queueDepth int, log *slog.Logger) *Persister {
	if log == nil {
		log = slog.Default()
	}
	if queueDepth <= 0 {
		queueDepth = 1
	}
	return &Persister{
		in:    make(chan message.Message, queueDepth),
		store: st,
		log:   log,
	}
}

// Inbox returns the send side of the queue for the hub to publish to. Exposing only
// the send direction makes the data-flow direction explicit and prevents the hub
// from accidentally receiving (CODING-STANDARDS §4).
func (p *Persister) Inbox() chan<- message.Message {
	return p.in
}

// Run drains the queue to the store until ctx is cancelled, then drains whatever
// remains buffered so no accepted message is lost on shutdown (NFR-R4). It returns
// when the queue is empty after cancellation.
func (p *Persister) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			p.drain()
			return
		case m := <-p.in:
			p.save(context.WithoutCancel(ctx), m)
		}
	}
}

// drain writes every message still buffered in the queue, used during shutdown.
func (p *Persister) drain() {
	for {
		select {
		case m := <-p.in:
			p.save(context.Background(), m)
		default:
			return
		}
	}
}

// save persists one message, logging (once) on failure since this is the boundary
// where the error is finally handled (CODING-STANDARDS §3, §9).
func (p *Persister) save(ctx context.Context, m message.Message) {
	if _, err := p.store.SaveMessage(ctx, m); err != nil {
		p.log.Error("persist: saving message failed", "room", m.Room, "err", err)
	}
}
