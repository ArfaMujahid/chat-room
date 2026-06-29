package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newSQLiteAuth opens a temp SQLite database with the auth schema and returns a store
// over it, so the auth queries are exercised against a real database in CI.
func newSQLiteAuth(t *testing.T) *SQLite {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "auth.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: got error %v; want nil", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL, display_name TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: got error %v; want nil", err)
		}
	}
	return NewSQLite(db)
}

// TestSQLiteAuthLifecycle exercises the full account/session lifecycle against a real
// SQLite database: create, duplicate rejection, lookup, session create/resolve/delete,
// and expiry.
func TestSQLiteAuthLifecycle(t *testing.T) {
	st := newSQLiteAuth(t)
	ctx := context.Background()

	u, err := st.CreateUser(ctx, "alice", "hash", "Alice")
	if err != nil {
		t.Fatalf("CreateUser: got error %v; want nil", err)
	}
	if u.ID == 0 {
		t.Fatal("CreateUser: ID not populated")
	}

	if _, err := st.CreateUser(ctx, "alice", "other", "Alice2"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate user: got %v; want ErrUsernameTaken", err)
	}

	gotUser, hash, err := st.CredentialsByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("CredentialsByUsername: got error %v; want nil", err)
	}
	if hash != "hash" || gotUser.ID != u.ID {
		t.Fatalf("credentials: got user %+v hash %q; want id %d hash \"hash\"", gotUser, hash, u.ID)
	}
	if _, _, err := st.CredentialsByUsername(ctx, "nobody"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v; want ErrInvalidCredentials", err)
	}

	if err := st.CreateSession(ctx, "tok1", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: got error %v; want nil", err)
	}
	if su, err := st.UserBySessionToken(ctx, "tok1"); err != nil || su.ID != u.ID {
		t.Fatalf("UserBySessionToken: got user %+v err %v; want id %d", su, err, u.ID)
	}

	if err := st.DeleteSession(ctx, "tok1"); err != nil {
		t.Fatalf("DeleteSession: got error %v; want nil", err)
	}
	if _, err := st.UserBySessionToken(ctx, "tok1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after delete: got %v; want ErrNoSession", err)
	}

	if err := st.CreateSession(ctx, "tok2", u.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateSession (expired): got error %v; want nil", err)
	}
	if _, err := st.UserBySessionToken(ctx, "tok2"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session: got %v; want ErrNoSession", err)
	}
}
