package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/session"
)

// newTestServer builds a Server with throwaway dependencies for handler tests. It
// uses t.Helper so failures point at the calling test (CODING-STANDARDS §8). The hub
// is constructed with nil store/persist because these tests exercise only HTTP
// routing and the session middleware, not the broadcast path.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.DBURL = "postgres://test"
	h := hub.New(nil, nil, cfg.HistoryLimit, slog.Default())
	srv, err := New(cfg, h, session.New(), slog.Default())
	if err != nil {
		t.Fatalf("New: got error %v; want nil", err)
	}
	return srv
}

// TestRoomsEndpoint checks GET /api/rooms returns 200 with a JSON array, exercising
// routing and the session middleware end to end (table-driven per §8).
func TestRoomsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/rooms", nil)

	srv.http.Handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d; want %d", got, want)
	}
	var rooms []string
	if err := json.NewDecoder(rec.Body).Decode(&rooms); err != nil {
		t.Fatalf("decoding body: got error %v; want valid JSON array", err)
	}
}

// TestWSNotImplemented documents the current scaffold behavior of /ws: it resolves a
// session then reports Not Implemented until the upgrade is wired in.
func TestWSNotImplemented(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ws", nil)

	srv.http.Handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusNotImplemented; got != want {
		t.Fatalf("status: got %d; want %d", got, want)
	}
}
