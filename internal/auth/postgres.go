package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolation is the Postgres SQLSTATE for a unique-constraint conflict, used to
// turn a duplicate username insert into ErrUsernameTaken.
const uniqueViolation = "23505"

// Postgres is a Store backed by Postgres. It shares the application's connection
// pool (owned and closed elsewhere), so it does not open or close the pool itself.
type Postgres struct {
	pool *pgxpool.Pool
}

// compile-time check that Postgres satisfies the consumer-side Store interface.
var _ Store = (*Postgres)(nil)

// NewPostgres returns an auth Store over an existing pgx pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// CreateUser inserts a new account, mapping a unique-constraint conflict to
// ErrUsernameTaken so the caller can return a clean 409.
func (p *Postgres) CreateUser(ctx context.Context, username, passwordHash, displayName string) (User, error) {
	const q = `INSERT INTO users (username, password_hash, display_name)
	           VALUES ($1, $2, $3)
	           RETURNING id`
	u := User{Username: username, DisplayName: displayName}
	if err := p.pool.QueryRow(ctx, q, username, passwordHash, displayName).Scan(&u.ID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("creating user %q: %w", username, err)
	}
	return u, nil
}

// CredentialsByUsername returns the user and stored password hash, or
// ErrInvalidCredentials if the username does not exist.
func (p *Postgres) CredentialsByUsername(ctx context.Context, username string) (User, string, error) {
	const q = `SELECT id, username, display_name, password_hash
	           FROM users
	           WHERE username = $1`
	var u User
	var hash string
	if err := p.pool.QueryRow(ctx, q, username).Scan(&u.ID, &u.Username, &u.DisplayName, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, "", ErrInvalidCredentials
		}
		return User{}, "", fmt.Errorf("looking up user %q: %w", username, err)
	}
	return u, hash, nil
}

// CreateSession stores a session token hash for a user with an expiry.
func (p *Postgres) CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	const q = `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`
	if _, err := p.pool.Exec(ctx, q, tokenHash, userID, expiresAt); err != nil {
		return fmt.Errorf("storing session: %w", err)
	}
	return nil
}

// UserBySessionToken returns the user for a non-expired session, or ErrNoSession.
func (p *Postgres) UserBySessionToken(ctx context.Context, tokenHash string) (User, error) {
	const q = `SELECT u.id, u.username, u.display_name
	           FROM sessions s
	           JOIN users u ON u.id = s.user_id
	           WHERE s.token_hash = $1 AND s.expires_at > now()`
	var u User
	if err := p.pool.QueryRow(ctx, q, tokenHash).Scan(&u.ID, &u.Username, &u.DisplayName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNoSession
		}
		return User{}, fmt.Errorf("resolving session: %w", err)
	}
	return u, nil
}

// DeleteSession removes a session by token hash; deleting a missing one is fine.
func (p *Postgres) DeleteSession(ctx context.Context, tokenHash string) error {
	const q = `DELETE FROM sessions WHERE token_hash = $1`
	if _, err := p.pool.Exec(ctx, q, tokenHash); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}
