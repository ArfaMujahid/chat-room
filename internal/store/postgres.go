package store

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/migrations"
)

// Postgres is a MessageStore backed by Postgres via a pgx connection pool. The pool
// is safe for concurrent use, so the type satisfies MessageStore's concurrency
// contract without additional locking.
type Postgres struct {
	// pool is the pgx connection pool; nil only on a failed/closed store.
	pool *pgxpool.Pool
}

// compile-time check that Postgres satisfies the consumer-side interface.
var _ MessageStore = (*Postgres)(nil)

// NewPostgres connects to Postgres using dsn, verifies the connection, applies any
// pending schema migrations, and returns a ready store. On any failure it closes the
// partial pool and returns a wrapped error, so misconfiguration fails fast at
// startup (FR-14).
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the connection pool. It is safe to call on a partially built store
// and safe to call more than once.
func (p *Postgres) Close() error {
	if p.pool != nil {
		p.pool.Close()
		p.pool = nil
	}
	return nil
}

// migrate applies every embedded .sql migration in filename order. Each migration is
// idempotent (CREATE ... IF NOT EXISTS), so running them on every startup is safe and
// keeps the binary self-provisioning.
func (p *Postgres) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		stmt, err := migrations.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}
		if _, err := p.pool.Exec(ctx, string(stmt)); err != nil {
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
	}
	return nil
}

// SaveMessage inserts m and returns it with the database-assigned ID set. If the
// message carries no timestamp, the server's current UTC time is used so room
// ordering is always well defined (NFR-R5).
func (p *Postgres) SaveMessage(ctx context.Context, m message.Message) (message.Message, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	const q = `INSERT INTO messages (room, sender_id, sender_name, content, created_at)
	           VALUES ($1, $2, $3, $4, $5)
	           RETURNING id`
	if err := p.pool.QueryRow(ctx, q, m.Room, m.SenderID, m.SenderName, m.Content, m.CreatedAt).Scan(&m.ID); err != nil {
		return message.Message{}, fmt.Errorf("saving message to room %q: %w", m.Room, err)
	}
	return m, nil
}

// RecentByRoom returns up to limit most-recent messages for room, oldest first so a
// joining client renders them top to bottom (FR-7). It selects newest-first using the
// indexed ordering, then reverses in memory.
func (p *Postgres) RecentByRoom(ctx context.Context, room string, limit int) ([]message.Message, error) {
	const q = `SELECT id, room, sender_id, sender_name, content, created_at
	           FROM messages
	           WHERE room = $1
	           ORDER BY created_at DESC, id DESC
	           LIMIT $2`
	rows, err := p.pool.Query(ctx, q, room, limit)
	if err != nil {
		return nil, fmt.Errorf("querying recent messages for room %q: %w", room, err)
	}
	defer rows.Close()

	out := make([]message.Message, 0, max(limit, 0))
	for rows.Next() {
		var m message.Message
		if err := rows.Scan(&m.ID, &m.Room, &m.SenderID, &m.SenderName, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating message rows: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}
