package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlite "modernc.org/sqlite"
)

// sqliteUniqueViolation is SQLITE_CONSTRAINT_UNIQUE, the extended result code for a
// unique-constraint conflict, used to map a duplicate username to ErrUsernameTaken.
const sqliteUniqueViolation = 2067

// SQLite is an auth.Store backed by an embedded SQLite database. It shares the
// *sql.DB opened (and closed) by the message store, so it does not own the handle.
// Timestamps are stored as Unix nanoseconds, matching the SQLite message store.
type SQLite struct {
	db *sql.DB
}

// compile-time check that SQLite satisfies the consumer-side Store interface.
var _ Store = (*SQLite)(nil)

// NewSQLite returns an auth Store over an existing SQLite database handle.
func NewSQLite(db *sql.DB) *SQLite {
	return &SQLite{db: db}
}

// CreateUser inserts a new account, mapping a unique-constraint conflict to
// ErrUsernameTaken.
func (s *SQLite) CreateUser(ctx context.Context, username, passwordHash, displayName string) (User, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, display_name, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, displayName, time.Now().UnixNano())
	if err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code() == sqliteUniqueViolation {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("creating user %q: %w", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("creating user %q: %w", username, err)
	}
	return User{ID: id, Username: username, DisplayName: displayName}, nil
}

// CredentialsByUsername returns the user and stored password hash, or
// ErrInvalidCredentials if the username does not exist.
func (s *SQLite) CredentialsByUsername(ctx context.Context, username string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, display_name, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.DisplayName, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return User{}, "", fmt.Errorf("looking up user %q: %w", username, err)
	}
	return u, hash, nil
}

// CreateSession stores a session token hash for a user with an expiry.
func (s *SQLite) CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, time.Now().UnixNano(), expiresAt.UnixNano())
	if err != nil {
		return fmt.Errorf("storing session: %w", err)
	}
	return nil
}

// UserBySessionToken returns the user for a non-expired session, or ErrNoSession.
func (s *SQLite) UserBySessionToken(ctx context.Context, tokenHash string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.display_name
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, time.Now().UnixNano()).
		Scan(&u.ID, &u.Username, &u.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("resolving session: %w", err)
	}
	return u, nil
}

// DeleteSession removes a session by token hash; deleting a missing one is fine.
func (s *SQLite) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}
