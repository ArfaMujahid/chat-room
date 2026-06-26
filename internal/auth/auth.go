// Package auth provides username/password authentication: account registration,
// login, logout, and session validation. It is the real-identity implementation of
// the seam the earlier projects left as an unverified cookie (architecture NFR-X1).
// Passwords are bcrypt-hashed; sessions are server-side and revocable, identified by
// a random token whose hash is stored so a database leak cannot reuse live tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Validation bounds for credentials. They are deliberately modest — this is an
// organizational identity, not a high-security boundary (NFR-X1).
const (
	minUsername    = 3
	maxUsername    = 32
	minPassword    = 8
	maxPassword    = 128 // bcrypt only hashes the first 72 bytes; cap well above that.
	maxDisplayName = 32
)

// Sentinel errors callers inspect with errors.Is to map failures to HTTP responses.
var (
	// ErrUsernameTaken means the chosen username already exists.
	ErrUsernameTaken = errors.New("auth: username already taken")
	// ErrInvalidCredentials means the username/password pair did not match. The same
	// error is returned whether the user is missing or the password is wrong, so the
	// response does not reveal which usernames exist.
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	// ErrInvalidInput means a credential failed validation (length, emptiness).
	ErrInvalidInput = errors.New("auth: invalid input")
	// ErrNoSession means no valid, unexpired session matched the token.
	ErrNoSession = errors.New("auth: no valid session")
)

// User is an authenticated account as the rest of the system sees it. The password
// hash never leaves the store, so it is not a field here.
type User struct {
	// ID is the account's stable database identity, used as the chat sender ID.
	ID int64
	// Username is the unique login handle.
	Username string
	// DisplayName is the name shown on the user's messages (FR-2).
	DisplayName string
}

// Store persists accounts and sessions. It is defined here, on the consumer side, so
// the Service depends on an interface and tests use a fake (CODING-STANDARDS §5).
type Store interface {
	// CreateUser inserts a new account, returning ErrUsernameTaken if the username
	// is already in use.
	CreateUser(ctx context.Context, username, passwordHash, displayName string) (User, error)
	// CredentialsByUsername returns the user and stored password hash for username,
	// or ErrInvalidCredentials if no such user exists.
	CredentialsByUsername(ctx context.Context, username string) (User, string, error)
	// CreateSession stores a session token hash for a user with an expiry.
	CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error
	// UserBySessionToken returns the user for a non-expired session token hash, or
	// ErrNoSession if none matches.
	UserBySessionToken(ctx context.Context, tokenHash string) (User, error)
	// DeleteSession removes a session by its token hash (logout). Deleting a missing
	// session is not an error.
	DeleteSession(ctx context.Context, tokenHash string) error
}

// Service implements the authentication operations over a Store. It is safe for
// concurrent use as long as the Store is.
type Service struct {
	store      Store
	sessionTTL time.Duration
}

// NewService returns a Service whose sessions live for sessionTTL.
func NewService(store Store, sessionTTL time.Duration) *Service {
	return &Service{store: store, sessionTTL: sessionTTL}
}

// Register validates the input, creates the account with a bcrypt-hashed password,
// and opens a session for it, returning the user and the raw session token to set as
// a cookie. It returns ErrUsernameTaken if the username exists or ErrInvalidInput on
// a validation failure.
func (s *Service) Register(ctx context.Context, username, password, displayName string) (User, string, error) {
	username = strings.TrimSpace(username)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if err := validateCredentials(username, password, displayName); err != nil {
		return User{}, "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, "", fmt.Errorf("hashing password: %w", err)
	}
	user, err := s.store.CreateUser(ctx, username, string(hash), displayName)
	if err != nil {
		return User{}, "", err // ErrUsernameTaken or a wrapped store error
	}
	token, err := s.openSession(ctx, user.ID)
	if err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// Login verifies the password for username and opens a session, returning the user
// and the raw session token. It returns ErrInvalidCredentials for both an unknown
// user and a wrong password, without distinguishing them.
func (s *Service) Login(ctx context.Context, username, password string) (User, string, error) {
	username = strings.TrimSpace(username)
	user, hash, err := s.store.CredentialsByUsername(ctx, username)
	if err != nil {
		// Run a dummy compare to keep timing roughly constant whether or not the
		// user exists, so response time does not leak account existence.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidin"), []byte(password))
		return User{}, "", ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return User{}, "", ErrInvalidCredentials
	}
	token, err := s.openSession(ctx, user.ID)
	if err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// Logout invalidates the session identified by the raw token. An empty or unknown
// token is a no-op.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, hashToken(token))
}

// Authenticate resolves the user for a raw session token, or ErrNoSession if the
// token is empty, unknown, or expired.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	return s.store.UserBySessionToken(ctx, hashToken(token))
}

// SessionTTL reports how long a new session lasts, so callers can set the cookie
// expiry to match.
func (s *Service) SessionTTL() time.Duration {
	return s.sessionTTL
}

// openSession mints a random token, stores its hash with an expiry, and returns the
// raw token for the caller to hand to the client as a cookie.
func (s *Service) openSession(ctx context.Context, userID int64) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(s.sessionTTL)
	if err := s.store.CreateSession(ctx, hashToken(token), userID, expires); err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	return token, nil
}

// validateCredentials enforces the length/emptiness rules, wrapping ErrInvalidInput
// with a specific reason for the caller to surface.
func validateCredentials(username, password, displayName string) error {
	switch {
	case len(username) < minUsername || len(username) > maxUsername:
		return fmt.Errorf("%w: username must be %d-%d characters", ErrInvalidInput, minUsername, maxUsername)
	case len(password) < minPassword || len(password) > maxPassword:
		return fmt.Errorf("%w: password must be %d-%d characters", ErrInvalidInput, minPassword, maxPassword)
	case displayName == "" || len(displayName) > maxDisplayName:
		return fmt.Errorf("%w: display name must be 1-%d characters", ErrInvalidInput, maxDisplayName)
	}
	return nil
}

// newToken returns a 256-bit cryptographically random session token, URL-safe.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// hashToken returns the hex-encoded SHA-256 of a raw token. Only the hash is stored,
// so a leaked sessions table cannot be used to impersonate live sessions.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
