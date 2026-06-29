package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArfaMujahid/chat-room/internal/message"
)

// TestSQLiteSaveAndRecentByRoom verifies the embedded store persists messages with an
// assigned ID and returns them oldest-first within the limit. It uses a temp-dir
// database, so it runs anywhere (including CI) with no external services.
func TestSQLiteSaveAndRecentByRoom(t *testing.T) {
	ctx := context.Background()
	st, err := NewSQLite(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: got error %v; want nil", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const room = "r"
	for i := range 3 {
		saved, err := st.SaveMessage(ctx, message.Message{
			Room: room, SenderID: "u1", SenderName: "Tester",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond).UTC(),
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
	if got[0].Content != "msg-1" || got[1].Content != "msg-2" {
		t.Fatalf("order: got [%q, %q]; want [msg-1, msg-2]", got[0].Content, got[1].Content)
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt not restored from the database")
	}
}
