package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryNotificationReplyRefs(t *testing.T) {
	testNotificationReplyRefs(t, NewMemoryNotificationReplies(), "nrr-mem")
}

// TestMySQLNotificationReplyRefs runs the same contract against MySQL (gated
// on BNBLOC_TEST_MYSQL_DSN) under a unique notifier it removes.
func TestMySQLNotificationReplyRefs(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	notifier := "nrr-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM notification_reply_tokens WHERE notifier LIKE ?`, notifier+"%")
		_, _ = b.DB.Exec(`DELETE FROM notification_reply_refs WHERE notifier LIKE ?`, notifier+"%")
		_ = b.DB.Close()
	})
	testNotificationReplyRefs(t, NewMySQLNotificationReplies(b.DB), notifier)
}

func testNotificationReplyRefs(t *testing.T, s NotificationReplyTokenStore, notifier string) {
	ctx := context.Background()
	hash := func(s string) string {
		out := []byte(s + "0000000000000000000000000000000000000000000000000000000000000000")
		return string(out[:64])
	}
	// One message sent to two accounts: one ref, a row per recipient.
	alice := NotificationReplyRef{Notifier: notifier, Ref: "v1:1:+900", Recipient: "+11", TokenHash: hash(notifier + "a"), CreatedAtMs: 100, ExpiresAtMs: 500}
	bob := NotificationReplyRef{Notifier: notifier, Ref: "v1:1:+900", Recipient: "+22", TokenHash: hash(notifier + "b"), CreatedAtMs: 100, ExpiresAtMs: 1 << 50}
	other := bob
	other.Notifier, other.TokenHash = notifier+"-other", hash(notifier+"c")
	for _, r := range []NotificationReplyRef{alice, bob, other} {
		if err := s.PutNotificationReplyRef(ctx, r); err != nil {
			t.Fatalf("put %+v: %v", r, err)
		}
	}
	// Same binding again: no-op; another token on the same key: conflict.
	if err := s.PutNotificationReplyRef(ctx, alice); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	moved := alice
	moved.TokenHash = hash(notifier + "z")
	if err := s.PutNotificationReplyRef(ctx, moved); !errors.Is(err, ErrNotificationReplyRefConflict) {
		t.Fatalf("conflicting put = %v", err)
	}
	if err := s.PutNotificationReplyRef(ctx, NotificationReplyRef{Notifier: notifier, Ref: "x"}); err == nil {
		t.Fatal("incomplete ref stored")
	}
	if got, ok, err := s.GetNotificationReplyRef(ctx, notifier, alice.Ref, "+11"); err != nil || !ok || got != alice {
		t.Fatalf("get alice = %+v %v %v", got, ok, err)
	}
	// Exact match: another account, another case, another notifier.
	for _, k := range [][3]string{{notifier, alice.Ref, "+33"}, {notifier, "V1:1:+900", "+11"}, {notifier + "-other", alice.Ref, "+11"}} {
		if _, ok, err := s.GetNotificationReplyRef(ctx, k[0], k[1], k[2]); err != nil || ok {
			t.Fatalf("get %v = %v %v", k, ok, err)
		}
	}
	// Pruning tokens prunes expired references too.
	if _, err := s.PruneNotificationReplyTokens(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetNotificationReplyRef(ctx, notifier, alice.Ref, "+11"); ok {
		t.Fatal("expired reference survived prune")
	}
	// Uninstall removes one notifier's references only.
	if err := s.DeleteNotificationReplyTokens(ctx, notifier); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetNotificationReplyRef(ctx, notifier, bob.Ref, "+22"); ok {
		t.Fatal("reference survived its notifier's uninstall")
	}
	if _, ok, _ := s.GetNotificationReplyRef(ctx, other.Notifier, other.Ref, "+22"); !ok {
		t.Fatal("another notifier's reference was deleted")
	}
}
