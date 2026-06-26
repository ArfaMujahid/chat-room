// Package web is the HTTP edge of the chat server. It serves the embedded UI, the
// authentication endpoints, a small rooms API, and upgrades GET /ws to a WebSocket
// whose lifecycle is handed to the hub. The HTTP server has explicit timeouts and the
// UI is embedded so the whole thing ships as a single binary (CODING-STANDARDS §6,
// NFR-D1). /ws and the rooms API require a valid login session.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/ArfaMujahid/chat-room/internal/auth"
	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
)

// staticFS embeds the chat UI (HTML/CSS/JS) into the binary (go:embed, NFR-D1).
//
//go:embed static
var staticFS embed.FS

// Server wires the HTTP handlers to the hub and auth service and owns the underlying
// *http.Server so it can be shut down gracefully (FR-12).
type Server struct {
	cfg  config.Config
	hub  *hub.Hub
	auth *auth.Service
	log  *slog.Logger
	http *http.Server
	// baseCtx is the application context; WebSocket connections derive from it, so
	// cancelling it on shutdown tears every live connection down (FR-12).
	baseCtx context.Context
}

// New builds a Server with all routes registered and timeouts set. It does not start
// listening; call Serve for that. The hub must already be running, and baseCtx must
// be the context that is cancelled on shutdown.
func New(baseCtx context.Context, cfg config.Config, h *hub.Hub, authSvc *auth.Service, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, hub: h, auth: authSvc, log: log, baseCtx: baseCtx}

	ui, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("locating embedded static assets: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("POST /api/register", s.handleRegister)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/rooms", s.handleRooms)
	mux.HandleFunc("GET /ws", s.handleWS)

	// Explicit server with timeouts — never the default ListenAndServe (§6, NFR-S3).
	// WriteTimeout is intentionally left zero: a WebSocket connection is long-lived,
	// and per-write deadlines are enforced inside the hub's write pump instead.
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.authMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s, nil
}

// Serve listens and serves until the server is shut down, returning nil on a clean
// shutdown rather than the sentinel http.ErrServerClosed.
func (s *Server) Serve() error {
	s.log.Info("web: listening", "addr", s.cfg.Addr)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting connections and waits for in-flight requests up to the
// context deadline (graceful shutdown, FR-12).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// handleRooms returns a snapshot of active rooms and the connection count as JSON for
// the UI's room list and live metrics (FR-10, NFR-O1). It requires authentication.
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if _, ok := userFromContext(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	s.writeJSON(w, http.StatusOK, s.hub.Snapshot(r.Context()))
}

// handleWS authenticates the request, validates the Origin (NFR-S4), upgrades the
// connection to a WebSocket with a bounded read size (NFR-S2), wraps it as a
// hub.Conn, and runs it as a Client until the connection ends (FR-1). The client's
// identity and display name come from the logged-in account (FR-2).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	opts := &websocket.AcceptOptions{}
	if len(s.cfg.AllowedOrigins) > 0 {
		opts.OriginPatterns = s.cfg.AllowedOrigins
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		// Accept has already written an error response to the client.
		s.log.Warn("web: websocket accept failed", "err", err)
		return
	}
	conn.SetReadLimit(s.cfg.MaxMessageSize) // reject oversized frames (NFR-S2)

	// Derive from baseCtx so server shutdown cancels the connection (FR-12).
	ctx, cancel := context.WithCancel(s.baseCtx)
	defer cancel()

	client := hub.NewClient(strconv.FormatInt(user.ID, 10), user.DisplayName, &wsConn{conn: conn}, s.cfg.SendBuffer, s.cfg.PingInterval)
	s.hub.Register(client)
	s.log.Info("web: client connected", "user", user.DisplayName, "id", user.ID)

	client.Run(ctx, s.hub) // blocks until the connection ends; readPump unregisters.

	s.log.Info("web: client disconnected", "user", user.DisplayName)
}
