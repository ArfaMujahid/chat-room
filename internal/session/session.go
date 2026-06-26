// Package session resolves a stable per-browser identity from a session cookie,
// minting one on first visit. Identity is an unverified bearer token, not a
// security boundary (NFR-X1) — real auth slots in at this same seam later.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// cookieName is the cookie that carries the session identity across reconnects.
const cookieName = "chat_session"

// UserID is a stable, opaque identifier for one browser/session (FR-2).
type UserID string

// ctxKey is an unexported context key type so values set here can't collide with
// keys from other packages (the standard context.WithValue discipline).
type ctxKey struct{}

// Manager mints and resolves session identities. It is constructed once and shared;
// the zero value is not usable because it needs no state today but keeps a seam for
// a signing secret when real auth arrives.
type Manager struct{}

// New returns a session Manager.
func New() *Manager {
	return &Manager{}
}

// Middleware ensures every request carries a session cookie, minting one on first
// visit, and stores the resolved UserID in the request context for handlers.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.resolve(w, r)
		if err != nil {
			http.Error(w, "could not establish session", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolve reads the session cookie or mints a new identity, setting the cookie on
// the response when it had to create one.
func (m *Manager) resolve(w http.ResponseWriter, r *http.Request) (UserID, error) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return UserID(c.Value), nil
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    string(id),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return id, nil
}

// FromContext returns the UserID stored by Middleware, and false if none is set
// (which means the handler ran outside the middleware — a wiring bug).
func FromContext(ctx context.Context) (UserID, bool) {
	id, ok := ctx.Value(ctxKey{}).(UserID)
	return id, ok
}

// newID generates a random 128-bit session identifier, hex-encoded. It uses
// crypto/rand so identifiers are unguessable even though they are not a security
// boundary today (NFR-X1).
func newID() (UserID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return UserID(hex.EncodeToString(b[:])), nil
}
