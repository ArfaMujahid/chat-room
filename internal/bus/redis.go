package bus

import (
	"context"
	"errors"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// RedisBus is the v2 MessageBus backed by Redis Pub/Sub, enabling horizontal
// scaling across server processes without changing the hub (NFR-C3). It is the
// documented scaling seam and is intentionally not built in v1 (architecture §10).
type RedisBus struct {
	// addr is the Redis endpoint; a *redis.Client (github.com/redis/go-redis/v9)
	// and a channel name are wired in once v2 is implemented.
	addr string
	// channel is the Pub/Sub channel messages are published to and read from.
	channel string
}

// compile-time check that RedisBus satisfies the interface.
var _ MessageBus = (*RedisBus)(nil)

// NewRedis returns a RedisBus targeting addr and the given Pub/Sub channel.
// v2 only — see architecture §10.
func NewRedis(addr, channel string) *RedisBus {
	return &RedisBus{addr: addr, channel: channel}
}

// Publish marshals m and PUBLISHes it to the Redis channel. (v2)
func (b *RedisBus) Publish(ctx context.Context, m message.Message) error {
	// TODO(arfa, v2): json.Marshal(m) then client.Publish(ctx, b.channel, payload).
	_ = ctx
	_ = m
	return errors.New("bus: RedisBus is a v2 seam and is not implemented")
}

// Subscribe SUBSCRIBEs to the Redis channel and yields decoded messages until ctx
// is cancelled. (v2)
func (b *RedisBus) Subscribe(ctx context.Context) (<-chan message.Message, error) {
	// TODO(arfa, v2): client.Subscribe(ctx, b.channel); decode each payload onto ch.
	_ = ctx
	return nil, errors.New("bus: RedisBus is a v2 seam and is not implemented")
}
