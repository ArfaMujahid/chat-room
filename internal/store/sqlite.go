package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// sqliteSchema is applied (idempotently) when the database is opened. SQLite uses
// INTEGER primary keys and stores timestamps as Unix nanoseconds, which sidesteps any
// driver date-format ambiguity. The users/sessions tables live here too so the auth
// store, which shares this database, has its schema ready.
var sqliteSchema = []string{
	`CREATE TABLE IF NOT EXISTS messages (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		room        TEXT    NOT NULL,
		sender_id   TEXT    NOT NULL,
		sender_name TEXT    NOT NULL,
		content     TEXT    NOT NULL,
		created_at  INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_room_created_at ON messages (room, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		display_name  TEXT    NOT NULL,
		created_at    INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at)`,
}

// SQLite is a MessageStore backed by an embedded SQLite database, so the binary runs
// with no external services. It owns the *sql.DB and shares it (via DB) with the auth
// store, mirroring how the Postgres store shares its pool.
type SQLite struct {
	db *sql.DB
}

// compile-time check that SQLite satisfies the consumer-side interface.
var _ MessageStore = (*SQLite)(nil)

// NewSQLite opens (creating if needed) the SQLite database at path, applies the
// schema, and returns a ready store. WAL mode plus a busy timeout let the persister
// and HTTP handlers write concurrently without "database is locked" errors.
func NewSQLite(ctx context.Context, path string) (*SQLite, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite %q: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening sqlite %q: %w", path, err)
	}
	for _, stmt := range sqliteSchema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("applying sqlite schema: %w", err)
		}
	}
	return &SQLite{db: db}, nil
}

// DB exposes the shared database handle so the auth store can use the same database.
func (s *SQLite) DB() *sql.DB {
	return s.db
}

// Close closes the database. It is safe to call more than once.
func (s *SQLite) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// SaveMessage inserts m and returns it with the assigned ID set, stamping the time if
// the caller left it zero so ordering is well defined (NFR-R5).
func (s *SQLite) SaveMessage(ctx context.Context, m message.Message) (message.Message, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (room, sender_id, sender_name, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.Room, m.SenderID, m.SenderName, m.Content, m.CreatedAt.UnixNano())
	if err != nil {
		return message.Message{}, fmt.Errorf("saving message to room %q: %w", m.Room, err)
	}
	if id, err := res.LastInsertId(); err == nil {
		m.ID = id
	}
	return m, nil
}

// RecentByRoom returns up to limit most-recent messages for room, oldest first so a
// joining client renders them top to bottom (FR-7).
func (s *SQLite) RecentByRoom(ctx context.Context, room string, limit int) ([]message.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, room, sender_id, sender_name, content, created_at
		 FROM messages
		 WHERE room = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, room, limit)
	if err != nil {
		return nil, fmt.Errorf("querying recent messages for room %q: %w", room, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]message.Message, 0, max(limit, 0))
	for rows.Next() {
		var m message.Message
		var createdNanos int64
		if err := rows.Scan(&m.ID, &m.Room, &m.SenderID, &m.SenderName, &m.Content, &createdNanos); err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		m.CreatedAt = time.Unix(0, createdNanos).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating message rows: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}
