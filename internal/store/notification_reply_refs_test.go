package store

import (
	"context"
	"errors"
	"testing"
)

func TestContractNotificationReplyTokenStoreRefs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) { testNotificationReplyRefs(t, b.ReplyTokens, uniq("nrr-")) })
}

func testNotificationReplyRefs(t *testing.T, s NotificationReplyTokenStore, notifier string) {
	ctx := context.Background()
	hash := replyHash
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
	// Pruning tokens prunes the references expired at beforeMs too (alice's
	// expires at 500: expired at 500, live at 499).
	if _, err := s.PruneNotificationReplyTokens(ctx, 499, 100); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetNotificationReplyRef(ctx, notifier, alice.Ref, "+11"); !ok {
		t.Fatal("a reference live at beforeMs was pruned")
	}
	if _, err := s.PruneNotificationReplyTokens(ctx, 500, 100); err != nil {
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
