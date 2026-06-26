package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/session"
	"github.com/ArfaMujahid/chat-room/internal/store"
)

// errFakeClosed is returned by a closed fakeConn's Read/Write, standing in for the
// real connection-closed error.
var errFakeClosed = errors.New("fake conn closed")

// fakeConn is an in-memory Conn for testing the hub without real sockets (NFR-M1).
// Frames pushed to in are returned by Read; frames passed to Write land on out. An
// unbuffered out with no reader models a stalled (slow) client.
type fakeConn struct {
	in     chan message.Envelope
	out    chan message.Envelope
	closed chan struct{}
	once   sync.Once
}

// newFakeConn builds a fakeConn whose out channel is buffered to outBuf (0 = a slow
// client that blocks on every write).
func newFakeConn(outBuf int) *fakeConn {
	return &fakeConn{
		in:     make(chan message.Envelope, 8),
		out:    make(chan message.Envelope, outBuf),
		closed: make(chan struct{}),
	}
}

// Read returns the next pushed frame, erroring when the conn is closed or ctx ends.
func (f *fakeConn) Read(ctx context.Context) (message.Envelope, error) {
	select {
	case e := <-f.in:
		return e, nil
	case <-f.closed:
		return message.Envelope{}, errFakeClosed
	case <-ctx.Done():
		return message.Envelope{}, ctx.Err()
	}
}

// Write records a frame on out, blocking until out has room (or the conn/ctx ends).
func (f *fakeConn) Write(ctx context.Context, e message.Envelope) error {
	select {
	case f.out <- e:
		return nil
	case <-f.closed:
		return errFakeClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Ping succeeds unless the conn is closed.
func (f *fakeConn) Ping(_ context.Context) error {
	select {
	case <-f.closed:
		return errFakeClosed
	default:
		return nil
	}
}

// Close marks the conn closed exactly once.
func (f *fakeConn) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// fakeStore is an in-memory MessageStore returning canned history per room.
type fakeStore struct {
	mu      sync.Mutex
	history map[string][]message.Message
}

// newFakeStore builds an empty fakeStore.
func newFakeStore() *fakeStore {
	return &fakeStore{history: make(map[string][]message.Message)}
}

// SaveMessage records nothing meaningful; it just assigns an ID to satisfy the API.
func (s *fakeStore) SaveMessage(_ context.Context, m message.Message) (message.Message, error) {
	m.ID = 1
	return m, nil
}

// RecentByRoom returns the canned history for room.
func (s *fakeStore) RecentByRoom(_ context.Context, room string, _ int) ([]message.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history[room], nil
}

// discardLogger returns a logger that throws output away, to keep test logs quiet.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startHub runs a hub for the test and cancels it on cleanup, returning the hub and
// its context. Pass a nil store for the no-history tests. The persist channel is
// buffered so the cold path never blocks tests.
func startHub(t *testing.T, st store.MessageStore) (*Hub, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	persist := make(chan message.Message, 64)
	h := New(st, persist, 50, discardLogger())
	go h.Run(ctx)
	return h, ctx
}

// startClient registers and runs a client backed by a fakeConn, returning both. A
// huge ping interval keeps the heartbeat from firing mid-test.
func startClient(t *testing.T, ctx context.Context, h *Hub, name string, sendBuf, outBuf int) (*Client, *fakeConn) {
	t.Helper()
	fc := newFakeConn(outBuf)
	c := NewClient(session.UserID(name), name, fc, sendBuf, time.Hour)
	h.Register(c)
	go c.Run(ctx, h)
	return c, fc
}

// joinAndWait pushes a join frame and waits until the room reports the wanted member
// count, so a test can sequence "everyone is in the room" before sending.
func joinAndWait(t *testing.T, ctx context.Context, h *Hub, fc *fakeConn, room string, wantMembers int) {
	t.Helper()
	fc.in <- message.Envelope{Type: message.TypeJoin, Room: room}
	waitFor(t, ctx, h, func(s Stats) bool {
		for _, r := range s.Rooms {
			if r.Name == room && r.Members == wantMembers {
				return true
			}
		}
		return false
	}, "room membership")
}

// waitFor polls the hub snapshot until cond holds or it times out.
func waitFor(t *testing.T, ctx context.Context, h *Hub, cond func(Stats) bool, what string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond(h.Snapshot(ctx)) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last snapshot: %+v", what, h.Snapshot(ctx))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// readUntilType reads frames from fc until one of the wanted type arrives or it times
// out, so presence/history frames don't obscure the frame under test.
func readUntilType(t *testing.T, fc *fakeConn, typ message.Type) message.Envelope {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-fc.out:
			if e.Type == typ {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q frame", typ)
		}
	}
}

// TestBroadcastReachesOtherMembers checks a message is delivered to other room
// members with sender and content intact (FR-5, FR-6).
func TestBroadcastReachesOtherMembers(t *testing.T) {
	h, ctx := startHub(t, nil)
	_, alice := startClient(t, ctx, h, "alice", 16, 16)
	_, bob := startClient(t, ctx, h, "bob", 16, 16)

	joinAndWait(t, ctx, h, alice, "r", 1)
	joinAndWait(t, ctx, h, bob, "r", 2)

	alice.in <- message.Envelope{Type: message.TypeMessage, Room: "r", Text: "hello"}

	got := readUntilType(t, bob, message.TypeMessage)
	if got.Message == nil {
		t.Fatal("message frame had nil Message")
	}
	if got.Message.Content != "hello" {
		t.Fatalf("content: got %q; want %q", got.Message.Content, "hello")
	}
	if got.Message.SenderName != "alice" {
		t.Fatalf("sender: got %q; want %q", got.Message.SenderName, "alice")
	}
}

// TestHistoryOnJoin checks a joining client receives the room's recent history (FR-7).
func TestHistoryOnJoin(t *testing.T) {
	st := newFakeStore()
	st.history["r"] = []message.Message{
		{Room: "r", SenderName: "old", Content: "first"},
		{Room: "r", SenderName: "old", Content: "second"},
	}
	h, ctx := startHub(t, st)
	_, c := startClient(t, ctx, h, "newcomer", 16, 16)

	c.in <- message.Envelope{Type: message.TypeJoin, Room: "r"}

	got := readUntilType(t, c, message.TypeHistory)
	if len(got.History) != 2 {
		t.Fatalf("history length: got %d; want 2", len(got.History))
	}
	if got.History[0].Content != "first" || got.History[1].Content != "second" {
		t.Fatalf("history order: got [%q, %q]; want [first, second]",
			got.History[0].Content, got.History[1].Content)
	}
}

// TestSlowClientIsDropped checks that a client whose send buffer fills is removed from
// the room without blocking delivery to others — the defining correctness property
// (NFR-R2). The slow client's conn never drains its writes.
func TestSlowClientIsDropped(t *testing.T) {
	h, ctx := startHub(t, nil)
	_, sender := startClient(t, ctx, h, "sender", 16, 64)
	// victim: send buffer of 1, unbuffered out with no reader → stalls immediately.
	_, victim := startClient(t, ctx, h, "victim", 1, 0)

	joinAndWait(t, ctx, h, sender, "r", 1)
	joinAndWait(t, ctx, h, victim, "r", 2)

	// Flood the room; deliveries target the victim (the sender is excluded), so its
	// buffer fills and the hub drops it.
	for range 10 {
		sender.in <- message.Envelope{Type: message.TypeMessage, Room: "r", Text: "flood"}
	}

	waitFor(t, ctx, h, func(s Stats) bool {
		for _, r := range s.Rooms {
			if r.Name == "r" {
				return r.Members == 1
			}
		}
		return false
	}, "victim to be dropped from the room")

	if s := h.Snapshot(ctx); s.Connections != 1 {
		t.Fatalf("connections after drop: got %d; want 1", s.Connections)
	}
}

// TestSendersUnblockAfterShutdown checks Register/Submit/Unregister return promptly
// once Run has exited, so client goroutines never leak on shutdown (NFR-R1).
func TestSendersUnblockAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := New(nil, make(chan message.Message, 1), 0, discardLogger())
	go h.Run(ctx)

	cancel()
	<-h.done // wait for Run to exit

	finished := make(chan struct{})
	go func() {
		h.Register(&Client{})
		h.Submit(command{kind: message.TypeMessage})
		h.Unregister(&Client{})
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("hub methods blocked after shutdown")
	}
}
