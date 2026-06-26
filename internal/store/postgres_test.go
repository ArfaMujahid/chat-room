package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// requireStore connects to the Postgres named by CHAT_TEST_DB_URL, skipping the test
// when it is unset so unit CI (which has no database) stays green. It registers
// cleanup to close the pool (CODING-STANDARDS §8). Set the env var to run it, e.g.
// CHAT_TEST_DB_URL="postgres://localhost:5432/chat_test?sslmode=disable".
func requireStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DB_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DB_URL not set; skipping Postgres integration test")
	}
	st, err := NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPostgres: got error %v; want nil", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestSaveAndRecentByRoom verifies messages persist with an assigned ID and come back
// oldest-first, bounded by the requested limit (FR-7, FR-8, NFR-R5).
func TestSaveAndRecentByRoom(t *testing.T) {
	st := requireStore(t)
	ctx := context.Background()
	room := fmt.Sprintf("itest_%d", time.Now().UnixNano())

	for i := range 3 {
		saved, err := st.SaveMessage(ctx, message.Message{
			Room:       room,
			SenderID:   "u1",
			SenderName: "Tester",
			Content:    fmt.Sprintf("msg-%d", i),
			CreatedAt:  time.Now().Add(time.Duration(i) * time.Millisecond).UTC(),
		})
		if err != nil {
			t.Fatalf("SaveMessage[%d]: got error %v; want nil", i, err)
		}
		if saved.ID == 0 {
			t.Fatalf("SaveMessage[%d]: ID not populated", i)
		}
	}

	got, err := st.RecentByRoom(ctx, room, 2)
	if err != nil {
		t.Fatalf("RecentByRoom: got error %v; want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("RecentByRoom len: got %d; want 2", len(got))
	}
	// Most-recent 2, returned oldest-first → msg-1 then msg-2.
	if got[0].Content != "msg-1" || got[1].Content != "msg-2" {
		t.Fatalf("order: got [%q, %q]; want [msg-1, msg-2]", got[0].Content, got[1].Content)
	}
}
