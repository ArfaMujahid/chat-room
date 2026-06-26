package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ArfaMujahid/chat-room/internal/auth"
	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/message"
	"github.com/ArfaMujahid/chat-room/internal/store"
)

// fakeAuthStore is an in-memory auth.Store for testing the web layer without a
// database.
type fakeAuthStore struct {
	mu       sync.Mutex
	users    map[string]userRow
	sessions map[string]sessRow
	nextID   int64
}

type userRow struct {
	user auth.User
	hash string
}

type sessRow struct {
	userID  int64
	expires time.Time
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{users: map[string]userRow{}, sessions: map[string]sessRow{}}
}

func (s *fakeAuthStore) CreateUser(_ context.Context, username, hash, display string) (auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; ok {
		return auth.User{}, auth.ErrUsernameTaken
	}
	s.nextID++
	u := auth.User{ID: s.nextID, Username: username, DisplayName: display}
	s.users[username] = userRow{user: u, hash: hash}
	return u, nil
}

func (s *fakeAuthStore) CredentialsByUsername(_ context.Context, username string) (auth.User, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.users[username]
	if !ok {
		return auth.User{}, "", auth.ErrInvalidCredentials
	}
	return row.user, row.hash, nil
}

func (s *fakeAuthStore) CreateSession(_ context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tokenHash] = sessRow{userID: userID, expires: expiresAt}
	return nil
}

func (s *fakeAuthStore) UserBySessionToken(_ context.Context, tokenHash string) (auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tokenHash]
	if !ok || !sess.expires.After(time.Now()) {
		return auth.User{}, auth.ErrNoSession
	}
	for _, row := range s.users {
		if row.user.ID == sess.userID {
			return row.user, nil
		}
	}
	return auth.User{}, auth.ErrNoSession
}

func (s *fakeAuthStore) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

// fakeHistoryStore is a MessageStore serving canned history (for the history test).
type fakeHistoryStore struct {
	history []message.Message
}

func (s *fakeHistoryStore) SaveMessage(_ context.Context, m message.Message) (message.Message, error) {
	return m, nil
}

func (s *fakeHistoryStore) RecentByRoom(_ context.Context, _ string, _ int) ([]message.Message, error) {
	return s.history, nil
}

// newTestServer builds a Server (running hub, no message store) plus an httptest
// server, tearing both down on cleanup.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	return newTestServerWithStore(t, nil)
}

// newTestServerWithStore is newTestServer with an injectable message store.
func newTestServerWithStore(t *testing.T, msgStore store.MessageStore) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := hub.New(msgStore, make(chan message.Message, 16), cfg.HistoryLimit, slog.Default())
	go h.Run(ctx)

	authSvc := auth.NewService(newFakeAuthStore(), cfg.SessionTTL)
	srv, err := New(ctx, cfg, h, authSvc, slog.Default())
	if err != nil {
		t.Fatalf("New: got error %v; want nil", err)
	}
	ts := httptest.NewServer(srv.http.Handler)
	t.Cleanup(ts.Close)
	return srv, ts
}

// register creates an account through the API and returns its session token.
func register(t *testing.T, ts *httptest.Server, username, password, display string) string {
	t.Helper()
	body, _ := json.Marshal(credentials{Username: username, Password: password, DisplayName: display})
	resp, err := http.Post(ts.URL+"/api/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register %s: got error %v; want nil", username, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s status: got %d; want 201", username, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	t.Fatalf("register %s: no session cookie set", username)
	return ""
}

// dial opens an authenticated WebSocket connection using the session token.
func dial(t *testing.T, ctx context.Context, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{sessionCookie + "=" + token}},
	}
	c, resp, err := websocket.Dial(ctx, wsURL, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: got error %v; want nil", err)
	}
	return c
}

// authedGet performs a GET carrying the session cookie.
func authedGet(t *testing.T, ts *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: got error %v; want nil", path, err)
	}
	return resp
}

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

// TestRoomsRequiresAuth checks /api/rooms is 401 without a session and 200 with one.
func TestRoomsRequiresAuth(t *testing.T) {
	_, ts := newTestServer(t)

	resp := authedGet(t, ts, "/api/rooms", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/rooms: got %d; want 401", resp.StatusCode)
	}

	token := register(t, ts, "alice", "password123", "Alice")
	resp = authedGet(t, ts, "/api/rooms", token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /api/rooms: got %d; want 200", resp.StatusCode)
	}
	var stats hub.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: got error %v; want valid JSON", err)
	}
}

// TestMeReflectsAuth checks /api/me is a 200 probe: authenticated=false without a
// session, and the account once registered.
func TestMeReflectsAuth(t *testing.T) {
	_, ts := newTestServer(t)

	resp := authedGet(t, ts, "/api/me", "")
	var anon meResponse
	if err := json.NewDecoder(resp.Body).Decode(&anon); err != nil {
		t.Fatalf("decode anon me: got error %v; want valid JSON", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || anon.Authenticated {
		t.Fatalf("unauthenticated /api/me: got status %d authenticated=%v; want 200 false", resp.StatusCode, anon.Authenticated)
	}

	token := register(t, ts, "alice", "password123", "Alice")
	resp = authedGet(t, ts, "/api/me", token)
	defer resp.Body.Close()
	var me meResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decode me: got error %v; want valid JSON", err)
	}
	if !me.Authenticated || me.Username != "alice" || me.DisplayName != "Alice" {
		t.Fatalf("me: got %+v; want authenticated alice/Alice", me)
	}
}

// TestWebSocketRequiresAuth checks an unauthenticated /ws upgrade is rejected.
func TestWebSocketRequiresAuth(t *testing.T) {
	_, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("unauthenticated /ws upgrade succeeded; want rejection")
	}
}

// TestWebSocketBroadcast checks a message from one authenticated client reaches
// another in the same room with sender and content intact (FR-1/3/5/6).
func TestWebSocketBroadcast(t *testing.T) {
	srv, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	aliceTok := register(t, ts, "alice", "password123", "Alice")
	bobTok := register(t, ts, "bob", "password123", "Bob")

	alice := dial(t, ctx, ts, aliceTok)
	defer alice.Close(websocket.StatusNormalClosure, "")
	bob := dial(t, ctx, ts, bobTok)
	defer bob.Close(websocket.StatusNormalClosure, "")

	writeEnv(t, ctx, alice, message.Envelope{Type: message.TypeJoin, Room: "r"})
	writeEnv(t, ctx, bob, message.Envelope{Type: message.TypeJoin, Room: "r"})
	waitMembers(t, ctx, srv.hub, "r", 2)

	writeEnv(t, ctx, alice, message.Envelope{Type: message.TypeMessage, Room: "r", Text: "hello"})

	got := readUntil(t, ctx, bob, message.TypeMessage)
	if got.Message == nil || got.Message.Content != "hello" {
		t.Fatalf("message: got %+v; want content hello", got.Message)
	}
	if got.Message.SenderName != "Alice" {
		t.Fatalf("sender: got %q; want Alice", got.Message.SenderName)
	}
}

// TestWebSocketHistoryOnJoin checks a joining client receives recent history through
// the full stack (FR-7), with an injected store and a real authenticated session.
func TestWebSocketHistoryOnJoin(t *testing.T) {
	st := &fakeHistoryStore{history: []message.Message{
		{Room: "r", SenderName: "old", Content: "first"},
		{Room: "r", SenderName: "old", Content: "second"},
	}}
	srv, ts := newTestServerWithStore(t, st)
	_ = srv
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := register(t, ts, "alice", "password123", "Alice")
	c := dial(t, ctx, ts, token)
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
