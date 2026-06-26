package bus

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// TestLocalBusDeliversToSubscriber checks a published message reaches a subscriber.
func TestLocalBusDeliversToSubscriber(t *testing.T) {
	b := NewLocal(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: got error %v; want nil", err)
	}
	if err := b.Publish(ctx, message.Message{Room: "r", Content: "hi"}); err != nil {
		t.Fatalf("Publish: got error %v; want nil", err)
	}

	select {
	case got := <-ch:
		if got.Content != "hi" {
			t.Fatalf("delivered content: got %q; want %q", got.Content, "hi")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery")
	}
}

// TestLocalBusFanOut checks every subscriber receives each published message.
func TestLocalBusFanOut(t *testing.T) {
	b := NewLocal(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, _ := b.Subscribe(ctx)
	c, _ := b.Subscribe(ctx)
	if err := b.Publish(ctx, message.Message{Content: "x"}); err != nil {
		t.Fatalf("Publish: got error %v; want nil", err)
	}

	for i, ch := range []<-chan message.Message{a, c} {
		select {
		case got := <-ch:
			if got.Content != "x" {
				t.Fatalf("subscriber %d content: got %q; want %q", i, got.Content, "x")
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

// TestLocalBusClosesOnCancel checks cancelling the subscribe context closes the
// channel, which is how a delivery loop knows to stop (NFR-R1).
func TestLocalBusClosesOnCancel(t *testing.T) {
	b := NewLocal(1)
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := b.Subscribe(ctx)

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel delivered a value; want closed")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after context cancel")
	}
}

// TestLocalBusPublishNeverBlocks checks Publish drops rather than blocking when a
// subscriber's buffer is full (the slow-consumer rule, NFR-R2).
func TestLocalBusPublishNeverBlocks(t *testing.T) {
	b := NewLocal(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := b.Subscribe(ctx); err != nil {
		t.Fatalf("Subscribe: got error %v; want nil", err)
	}

	done := make(chan struct{})
	go func() {
		for i := range 100 {
			_ = b.Publish(ctx, message.Message{Content: strconv.Itoa(i)})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
}
