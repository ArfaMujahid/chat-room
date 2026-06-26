package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ArfaMujahid/chat-room/internal/auth"
)

// sessionCookie holds the opaque login session token. It is HttpOnly so page scripts
// cannot read it, and SameSite=Lax to blunt CSRF on the unsafe endpoints.
const sessionCookie = "chat_session"

// maxBodyBytes caps a JSON request body so a client cannot send an unbounded one.
const maxBodyBytes = 4 << 10

// userCtxKey is the unexported context key under which the authenticated user is
// stored, so it cannot collide with keys from other packages.
type userCtxKey struct{}

// credentials is the JSON body for register/login. DisplayName is ignored by login.
type credentials struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// userResponse is the public view of an account returned to the client; it never
// includes the password hash or internal ID.
type userResponse struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// meResponse answers the session probe. It always returns 200 so an unauthenticated
// first visit is a normal state, not a logged console error; Authenticated tells the
// client which view to show.
type meResponse struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
}

// authMiddleware resolves an optional authenticated user from the session cookie and
// stores it in the request context. It never rejects — individual handlers decide
// whether a route requires authentication.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if user, err := s.auth.Authenticate(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, user))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// userFromContext returns the authenticated user set by authMiddleware, and false if
// the request is unauthenticated.
func userFromContext(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(auth.User)
	return u, ok
}

// handleRegister creates an account, opens a session, and sets the session cookie.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	user, token, err := s.auth.Register(r.Context(), c.Username, c.Password, c.DisplayName)
	switch {
	case errors.Is(err, auth.ErrUsernameTaken):
		http.Error(w, "username already taken", http.StatusConflict)
		return
	case errors.Is(err, auth.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		s.log.Error("web: register failed", "err", err)
		http.Error(w, "could not register", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, token)
	s.writeJSON(w, http.StatusCreated, userResponse{Username: user.Username, DisplayName: user.DisplayName})
}

// handleLogin verifies credentials, opens a session, and sets the session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	user, token, err := s.auth.Login(r.Context(), c.Username, c.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	case err != nil:
		s.log.Error("web: login failed", "err", err)
		http.Error(w, "could not log in", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, token)
	s.writeJSON(w, http.StatusOK, userResponse{Username: user.Username, DisplayName: user.DisplayName})
}

// handleLogout invalidates the current session (if any) and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			s.log.Error("web: logout failed", "err", err)
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe is the session probe the UI calls on load to decide whether to show the
// chat or the login screen. It always returns 200 (no session is a normal state).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		s.writeJSON(w, http.StatusOK, meResponse{Authenticated: false})
		return
	}
	s.writeJSON(w, http.StatusOK, meResponse{Authenticated: true, Username: user.Username, DisplayName: user.DisplayName})
}

// setSessionCookie writes the session cookie with an expiry matching the session TTL.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookies,
		MaxAge:   int(s.auth.SessionTTL().Seconds()),
	})
}

// clearSessionCookie expires the session cookie on the client (logout).
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookies,
		MaxAge:   -1,
	})
}

// decodeJSON reads a bounded JSON body into dst, writing a 400 and returning false on
// failure so the caller can simply return.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

// writeJSON encodes v as a JSON response with the given status, logging on failure.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("web: encoding response", "err", err)
	}
}
