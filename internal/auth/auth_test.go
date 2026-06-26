package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memStore is an in-memory Store for testing the Service without a database.
type memStore struct {
	users    map[string]userRow // by username
	sessions map[string]sessRow // by token hash
	nextID   int64
}

type userRow struct {
	user User
	hash string
}

type sessRow struct {
	userID  int64
	expires time.Time
}

func newMemStore() *memStore {
	return &memStore{users: map[string]userRow{}, sessions: map[string]sessRow{}}
}

func (m *memStore) CreateUser(_ context.Context, username, passwordHash, displayName string) (User, error) {
	if _, ok := m.users[username]; ok {
		return User{}, ErrUsernameTaken
	}
	m.nextID++
	u := User{ID: m.nextID, Username: username, DisplayName: displayName}
	m.users[username] = userRow{user: u, hash: passwordHash}
	return u, nil
}

func (m *memStore) CredentialsByUsername(_ context.Context, username string) (User, string, error) {
	row, ok := m.users[username]
	if !ok {
		return User{}, "", ErrInvalidCredentials
	}
	return row.user, row.hash, nil
}

func (m *memStore) CreateSession(_ context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	m.sessions[tokenHash] = sessRow{userID: userID, expires: expiresAt}
	return nil
}

func (m *memStore) UserBySessionToken(_ context.Context, tokenHash string) (User, error) {
	s, ok := m.sessions[tokenHash]
	if !ok || !s.expires.After(time.Now()) {
		return User{}, ErrNoSession
	}
	for _, row := range m.users {
		if row.user.ID == s.userID {
			return row.user, nil
		}
	}
	return User{}, ErrNoSession
}

func (m *memStore) DeleteSession(_ context.Context, tokenHash string) error {
	delete(m.sessions, tokenHash)
	return nil
}

// TestRegisterThenAuthenticate covers the happy path: a registered user's session
// token resolves back to that user.
func TestRegisterThenAuthenticate(t *testing.T) {
	svc := NewService(newMemStore(), time.Hour)
	ctx := context.Background()

	user, token, err := svc.Register(ctx, "alice", "password123", "Alice")
	if err != nil {
		t.Fatalf("Register: got error %v; want nil", err)
	}
	if user.Username != "alice" || user.DisplayName != "Alice" || user.ID == 0 {
		t.Fatalf("Register user: got %+v; want populated alice/Alice", user)
	}
	got, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: got error %v; want nil", err)
	}
	if got.ID != user.ID {
		t.Fatalf("Authenticate id: got %d; want %d", got.ID, user.ID)
	}
}

// TestRegisterDuplicateUsername checks the second registration is rejected.
func TestRegisterDuplicateUsername(t *testing.T) {
	svc := NewService(newMemStore(), time.Hour)
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, "alice", "password123", "Alice"); err != nil {
		t.Fatalf("first Register: got error %v; want nil", err)
	}
	_, _, err := svc.Register(ctx, "alice", "password456", "Alice2")
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate Register: got %v; want ErrUsernameTaken", err)
	}
}

// TestLoginWrongAndUnknown checks both wrong password and unknown user fail the same
// way, without revealing which usernames exist.
func TestLoginWrongAndUnknown(t *testing.T) {
	svc := NewService(newMemStore(), time.Hour)
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, "alice", "password123", "Alice"); err != nil {
		t.Fatalf("Register: got error %v; want nil", err)
	}

	if _, _, err := svc.Login(ctx, "alice", "wrongpass1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v; want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, "nobody", "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v; want ErrInvalidCredentials", err)
	}

	user, token, err := svc.Login(ctx, "alice", "password123")
	if err != nil {
		t.Fatalf("correct login: got error %v; want nil", err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate after login: got error %v; want nil", err)
	}
	_ = user
}

// TestLogoutInvalidatesSession checks a logged-out token no longer authenticates.
func TestLogoutInvalidatesSession(t *testing.T) {
	svc := NewService(newMemStore(), time.Hour)
	ctx := context.Background()
	_, token, err := svc.Register(ctx, "alice", "password123", "Alice")
	if err != nil {
		t.Fatalf("Register: got error %v; want nil", err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: got error %v; want nil", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Authenticate after logout: got %v; want ErrNoSession", err)
	}
}

// TestExpiredSession checks an expired token does not authenticate.
func TestExpiredSession(t *testing.T) {
	svc := NewService(newMemStore(), -time.Second) // sessions are born expired
	ctx := context.Background()
	_, token, err := svc.Register(ctx, "alice", "password123", "Alice")
	if err != nil {
		t.Fatalf("Register: got error %v; want nil", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Authenticate expired: got %v; want ErrNoSession", err)
	}
}

// TestRegisterValidation checks short credentials are rejected before any storage.
func TestRegisterValidation(t *testing.T) {
	svc := NewService(newMemStore(), time.Hour)
	ctx := context.Background()
	cases := []struct {
		name, user, pass, display string
	}{
		{"short username", "ab", "password123", "A"},
		{"short password", "alice", "short", "Alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := svc.Register(ctx, tc.user, tc.pass, tc.display); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v; want ErrInvalidInput", err)
			}
		})
	}
}
