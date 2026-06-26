package store

import (
	"context"
	"errors"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// errNotImplemented marks the scaffold methods that still need their pgx-backed
// bodies. It is wrapped with operation context at each call site (CODING-STANDARDS §3).
var errNotImplemented = errors.New("store: not implemented")

// Postgres is a MessageStore backed by Postgres. The driver/pool (github.com/jackc/
// pgx/v5) is wired in New once implemented; the field is kept unexported and the
// type satisfies MessageStore so the rest of the system codes against the interface.
type Postgres struct {
	// dsn is the connection string; the pool replaces it once pgx is wired in.
	dsn string
	// TODO(arfa): hold a *pgxpool.Pool here and close it in Close().
}

// compile-time check that Postgres satisfies the consumer-side interface.
var _ MessageStore = (*Postgres)(nil)

// NewPostgres connects to Postgres using dsn and returns a ready store. It returns
// an error if the connection or ping fails, so misconfiguration surfaces at startup.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	// TODO(arfa): open a pgxpool with the connection string, ping it, and return
	// the wrapped pool. Wrap failures as fmt.Errorf("connecting to postgres: %w", err).
	_ = ctx
	return &Postgres{dsn: dsn}, nil
}

// Close releases the connection pool. It is safe to call on a partially built store.
func (p *Postgres) Close() error {
	// TODO(arfa): close the pgx pool.
	return nil
}

// SaveMessage inserts m and returns it with the database-assigned ID and timestamp.
func (p *Postgres) SaveMessage(ctx context.Context, m message.Message) (message.Message, error) {
	// TODO(arfa): INSERT INTO messages (...) RETURNING id, created_at; scan back.
	_ = ctx
	return m, errors.Join(errNotImplemented, errors.New("SaveMessage"))
}

// RecentByRoom returns up to limit most-recent messages for room, oldest first.
func (p *Postgres) RecentByRoom(ctx context.Context, room string, limit int) ([]message.Message, error) {
	// TODO(arfa): SELECT ... WHERE room=$1 ORDER BY created_at DESC LIMIT $2, then
	// reverse so the slice is oldest-first for top-to-bottom rendering.
	_ = ctx
	_ = room
	_ = limit
	return nil, errors.Join(errNotImplemented, errors.New("RecentByRoom"))
}
