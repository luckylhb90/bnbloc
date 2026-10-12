package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func TestContractNotificationReplyTokenStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) { testNotificationReplyTokens(t, b.ReplyTokens, uniq("nrt-")) })
}

// replyHash is a 64-character token hash unique per seed.
func replyHash(seed string) string {
	return (seed + "0000000000000000000000000000000000000000000000000000000000000000")[:64]
}

// Pruning at beforeMs removes the tokens expired then (expires <= beforeMs:
// live strictly before expiry), and a prune with no limit removes at most
// NotificationPruneBatch on both backends.
func TestContractNotificationReplyTokenStorePrune(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.ReplyTokens
		ctx := context.Background()
		notifier := uniq("nrt-prune-")
		edge := NotificationReplyToken{TokenHash: replyHash(notifier + "e"), TenantID: "acme", Notifier: notifier, EventID: "e", RoomID: "r", UserID: "maya", CreatedAtMs: 1, ExpiresAtMs: 500}
		live := edge
		live.TokenHash, live.ExpiresAtMs = replyHash(notifier+"l"), 501
		for _, tok := range []NotificationReplyToken{edge, live} {
			if err := s.PutNotificationReplyToken(ctx, tok); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.PruneNotificationReplyTokens(ctx, 500, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := must2(s.GetNotificationReplyToken(ctx, edge.TokenHash))(t); ok {
			t.Fatal("a token expiring at beforeMs survived the prune")
		}
		if _, ok := must2(s.GetNotificationReplyToken(ctx, live.TokenHash))(t); !ok {
			t.Fatal("a token live at beforeMs was pruned")
		}
		// limit <= 0 is one bounded batch, not "everything".
		if testing.Short() {
			return
		}
		many := NotificationPruneBatch + 5
		for i := 0; i < many; i++ {
			tok := edge
			tok.TokenHash = replyHash(notifier + "m" + strconv.Itoa(100000+i))
			if err := s.PutNotificationReplyToken(ctx, tok); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := s.PruneNotificationReplyTokens(ctx, 500, 0); err != nil || n != NotificationPruneBatch {
			t.Fatalf("prune without a limit = %d, %v; want %d", n, err, NotificationPruneBatch)
		}
		if n, err := s.PruneNotificationReplyTokens(ctx, 500, 1<<20); err != nil || n != 5 {
			t.Fatalf("second prune = %d, %v; want the 5 left", n, err)
		}
	})
}

func testNotificationReplyTokens(t *testing.T, s NotificationReplyTokenStore, notifier string) {
	ctx := context.Background()
	hash := replyHash
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
	dup := room
	dup.UserID = "mallory"
	if err := s.PutNotificationReplyToken(ctx, dup); !errors.Is(err, ErrNotificationReplyTokenExists) {
		t.Fatalf("second token under one hash = %v, want ErrNotificationReplyTokenExists", err)
	}
	if got, _ := must2(s.GetNotificationReplyToken(ctx, room.TokenHash))(t); got != room {
		t.Fatalf("a refused put changed the token: %+v", got)
	}
	got, ok, err := s.GetNotificationReplyToken(ctx, agent.TokenHash)
	if err != nil || !ok || got != agent {
		t.Fatalf("get agent = %+v %v %v", got, ok, err)
	}
	if _, ok, err := s.GetNotificationReplyToken(ctx, hash(notifier+"missing")); err != nil || ok {
		t.Fatalf("get missing = %v %v", ok, err)
	}
	// Prune removes only expired rows (other tests' expired rows may go too).
	if n, err := s.PruneNotificationReplyTokens(ctx, 1000, 100); err != nil || n < 1 {
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
