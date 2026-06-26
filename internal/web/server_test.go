package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/session"
	"github.com/ArfaMujahid/chat-room/internal/store"
)

// newTestServer builds a Server with a running hub and no store (no history).
func newTestServer(t *testing.T) *Server {
	return newTestServerWithStore(t, nil)
}

// newTestServerWithStore builds a Server with a running hub backed by st, for
// handler and WebSocket tests. The hub and context are torn down on cleanup
// (CODING-STANDARDS §8).
func newTestServerWithStore(t *testing.T, st store.MessageStore) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.DBURL = "postgres://test"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := hub.New(st, make(chan message.Message, 16), cfg.HistoryLimit, slog.Default())
	go h.Run(ctx)

	srv, err := New(ctx, cfg, h, session.New(), slog.Default())
	if err != nil {
		t.Fatalf("New: got error %v; want nil", err)
	}
	return srv
}

// fakeHistoryStore is a MessageStore that serves canned history, so the history-on-
// join path can be tested through the full WebSocket stack without a database.
type fakeHistoryStore struct {
	history []message.Message
}

// SaveMessage accepts and echoes the message; persistence is not under test here.
func (s *fakeHistoryStore) SaveMessage(_ context.Context, m message.Message) (message.Message, error) {
	return m, nil
}

// RecentByRoom returns the canned history regardless of room/limit.
func (s *fakeHistoryStore) RecentByRoom(_ context.Context, _ string, _ int) ([]message.Message, error) {
	return s.history, nil
}

// TestRoomsEndpoint checks GET /api/rooms returns a 200 with a well-formed snapshot
// (FR-10), exercising routing and the session middleware end to end.
func TestRoomsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/rooms", nil)

	srv.http.Handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d; want %d", got, want)
	}
	var stats hub.Stats
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decoding body: got error %v; want valid Stats JSON", err)
	}
	if stats.Connections != 0 || len(stats.Rooms) != 0 {
		t.Fatalf("fresh hub snapshot: got %+v; want zero connections and rooms", stats)
	}
}

// TestWebSocketBroadcast dials two real WebSocket clients, joins them to a room, and
// verifies a message from one reaches the other with sender and content intact
// (FR-1, FR-3, FR-5, FR-6).
func TestWebSocketBroadcast(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.http.Handler)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alice := dial(t, ctx, wsURL+"?name=alice")
	defer alice.Close(websocket.StatusNormalClosure, "")
	bob := dial(t, ctx, wsURL+"?name=bob")
	defer bob.Close(websocket.StatusNormalClosure, "")

	writeEnv(t, ctx, alice, message.Envelope{Type: message.TypeJoin, Room: "r"})
	writeEnv(t, ctx, bob, message.Envelope{Type: message.TypeJoin, Room: "r"})
	waitMembers(t, ctx, srv.hub, "r", 2)

	writeEnv(t, ctx, alice, message.Envelope{Type: message.TypeMessage, Room: "r", Text: "hello"})

	got := readUntil(t, ctx, bob, message.TypeMessage)
	if got.Message == nil {
		t.Fatal("message frame had nil Message")
	}
	if got.Message.Content != "hello" {
		t.Fatalf("content: got %q; want %q", got.Message.Content, "hello")
	}
	if got.Message.SenderName != "alice" {
		t.Fatalf("sender: got %q; want %q", got.Message.SenderName, "alice")
	}
}

// TestWebSocketHistoryOnJoin verifies a joining client receives the room's recent
// history through the full WebSocket stack (web → hub → store), proving FR-7 without
// a live database by injecting a fake store.
func TestWebSocketHistoryOnJoin(t *testing.T) {
	st := &fakeHistoryStore{history: []message.Message{
		{Room: "r", SenderName: "old", Content: "first"},
		{Room: "r", SenderName: "old", Content: "second"},
	}}
	srv := newTestServerWithStore(t, st)
	ts := httptest.NewServer(srv.http.Handler)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := dial(t, ctx, wsURL+"?name=alice")
	defer c.Close(websocket.StatusNormalClosure, "")

	writeEnv(t, ctx, c, message.Envelope{Type: message.TypeJoin, Room: "r"})

	got := readUntil(t, ctx, c, message.TypeHistory)
	if len(got.History) != 2 {
		t.Fatalf("history length: got %d; want 2", len(got.History))
	}
	if got.History[0].Content != "first" || got.History[1].Content != "second" {
		t.Fatalf("history order: got [%q, %q]; want [first, second]",
			got.History[0].Content, got.History[1].Content)
	}
}

// dial opens a WebSocket client connection or fails the test. The handshake response
// body is closed immediately (it carries no payload on a successful upgrade).
func dial(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial %s: got error %v; want nil", url, err)
	}
	return c
}

// writeEnv JSON-encodes and sends one envelope.
func writeEnv(t *testing.T, ctx context.Context, c *websocket.Conn, env message.Envelope) {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: got error %v; want nil", err)
	}
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write envelope: got error %v; want nil", err)
	}
}

// readUntil reads frames until one of the wanted type arrives or ctx expires, so
// presence/history frames don't obscure the frame under test.
func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, typ message.Type) message.Envelope {
	t.Helper()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: got error %v; want a %q frame", err, typ)
		}
		var env message.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("unmarshal frame: got error %v; want nil", err)
		}
		if env.Type == typ {
			return env
		}
	}
}

// waitMembers polls the hub snapshot until room reports want members or ctx expires.
func waitMembers(t *testing.T, ctx context.Context, h *hub.Hub, room string, want int) {
	t.Helper()
	for {
		stats := h.Snapshot(ctx)
		for _, r := range stats.Rooms {
			if r.Name == room && r.Members == want {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d members in %q; last: %+v", want, room, stats)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
