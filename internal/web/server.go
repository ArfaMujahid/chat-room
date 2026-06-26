// Package web is the HTTP edge of the chat server. It serves the embedded UI, lists
// rooms over a small REST API, and upgrades GET /ws to a WebSocket whose lifecycle
// is handed to the hub. The HTTP server is configured with explicit timeouts and the
// UI is embedded so the whole thing ships as a single binary (CODING-STANDARDS §6,
// NFR-D1).
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/session"
)

// staticFS embeds the chat UI (HTML/CSS/JS) into the binary (go:embed, NFR-D1).
//
//go:embed static
var staticFS embed.FS

// Server wires the HTTP handlers to the hub and session manager and owns the
// underlying *http.Server so it can be shut down gracefully (FR-12).
type Server struct {
	cfg      config.Config
	hub      *hub.Hub
	sessions *session.Manager
	log      *slog.Logger
	http     *http.Server
}

// New builds a Server with all routes registered and timeouts set. It does not start
// listening; call Serve for that. The hub must already be running.
func New(cfg config.Config, h *hub.Hub, sessions *session.Manager, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, hub: h, sessions: sessions, log: log}

	ui, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, errors.New("web: locating embedded static assets") // unreachable unless embed misconfigured
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/rooms", s.handleRooms)
	mux.HandleFunc("GET /ws", s.handleWS)

	// Explicit server with timeouts — never the default ListenAndServe (§6, NFR-S3).
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.sessions.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
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

// handleRooms returns the active rooms as JSON for the UI's room list (FR-10).
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	// TODO(arfa): ask the hub for its active room names + member counts.
	rooms := []string{}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rooms); err != nil {
		s.log.Error("web: encoding rooms response", "err", err)
	}
}

// handleWS validates the Origin (NFR-S4), resolves the session, upgrades the
// connection to a WebSocket, wraps it as a hub.Client, and starts its pumps (FR-1).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); !ok {
		http.Error(w, "no session", http.StatusUnauthorized)
		return
	}
	// TODO(arfa): check Origin against cfg.AllowedOrigins (NFR-S4); upgrade with the
	// chosen WebSocket lib; wrap the conn as a hub.Conn; build a hub.Client and run
	// it. Until then, signal the endpoint is not yet implemented.
	http.Error(w, "websocket endpoint not implemented", http.StatusNotImplemented)
}
