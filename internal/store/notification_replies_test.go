package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMemoryNotificationReplyTokens(t *testing.T) {
	testNotificationReplyTokens(t, NewMemoryNotificationReplies(), "nrt-mem")
}

// TestMySQLNotificationReplyTokens runs the same contract against MySQL
// (gated on BNBLOC_TEST_MYSQL_DSN) under a unique notifier it removes.
func TestMySQLNotificationReplyTokens(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	notifier := "nrt-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM notification_reply_tokens WHERE notifier LIKE ?`, notifier+"%")
		_ = b.DB.Close()
	})
	testNotificationReplyTokens(t, NewMySQLNotificationReplies(b.DB), notifier)
}

func testNotificationReplyTokens(t *testing.T, s NotificationReplyTokenStore, notifier string) {
	ctx := context.Background()
	hash := func(s string) string { // 64 hex chars, unique per notifier
		out := []byte(s + "0000000000000000000000000000000000000000000000000000000000000000")
		return string(out[:64])
	}
	agent := NotificationReplyToken{
		TokenHash: hash(notifier + "a"), TenantID: "acme", Notifier: notifier, EventID: "ntf_1",
		RoomID: "design", ChannelID: "ch-1", UserID: "alice", ReplyOwnerID: "carol", ReplyProvider: "codex",
		ReplyCore: "local", CreatedAtMs: 100, ExpiresAtMs: 500,
	}
	room := NotificationReplyToken{
		TokenHash: hash(notifier + "b"), TenantID: "acme", Notifier: notifier, EventID: "ntf_2",
		RoomID: "design", UserID: "bob", CreatedAtMs: 100, ExpiresAtMs: 1 << 50,
	}
	other := room
	other.TokenHash, other.Notifier = hash(notifier+"c"), notifier+"-other"
	for _, tok := range []NotificationReplyToken{agent, room, other} {
		if err := s.PutNotificationReplyToken(ctx, tok); err != nil {
			t.Fatalf("put %s: %v", tok.TokenHash, err)
		}
	}
	if err := s.PutNotificationReplyToken(ctx, NotificationReplyToken{}); err == nil {
		t.Fatal("put without hash succeeded")
	}
	got, ok, err := s.GetNotificationReplyToken(ctx, agent.TokenHash)
	if err != nil || !ok || got != agent {
		t.Fatalf("get agent = %+v %v %v", got, ok, err)
	}
	if _, ok, err := s.GetNotificationReplyToken(ctx, hash(notifier+"missing")); err != nil || ok {
		t.Fatalf("get missing = %v %v", ok, err)
	}
	// Prune removes only expired rows.
	if n, err := s.PruneNotificationReplyTokens(ctx, 1000, 100); err != nil || n != 1 {
		t.Fatalf("prune = %d %v", n, err)
	}
	if _, ok, _ := s.GetNotificationReplyToken(ctx, agent.TokenHash); ok {
		t.Fatal("expired token survived prune")
	}
	// Uninstall removes one notifier's tokens only.
	if err := s.DeleteNotificationReplyTokens(ctx, notifier); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetNotificationReplyToken(ctx, room.TokenHash); ok {
		t.Fatal("token survived its notifier's uninstall")
	}
	if _, ok, _ := s.GetNotificationReplyToken(ctx, other.TokenHash); !ok {
		t.Fatal("another notifier's token was deleted")
	}
}
