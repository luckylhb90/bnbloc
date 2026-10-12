package store

import (
	"testing"
	"time"
)

func TestMemoryWebPushStore(t *testing.T) {
	testWebPushStore(t, NewMemoryWebPush(), "wp-mem")
}

// TestMySQLWebPushStore runs the same contract against MySQL (gated on
// BNBLOC_TEST_MYSQL_DSN) under a unique tenant it removes afterwards.
func TestMySQLWebPushStore(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	tid := "wp-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM web_push_subscriptions WHERE tenant_id LIKE ?`, tid+"%")
		_ = b.DB.Close()
	})
	testWebPushStore(t, NewMySQLWebPush(b.DB), tid)
}

func testWebPushStore(t *testing.T, s WebPushSubscriptionStore, tid string) {
	ctx := tenantCtx(tid)
	other := tenantCtx(tid + "-other")
	sub := func(id, user string, at int64) WebPushSubscription {
		return WebPushSubscription{ID: id, UserID: user, Endpoint: "https://push.example/" + id, P256dh: "pk-" + id, Auth: "au-" + id, UserAgent: "ua", CreatedAtMs: at, UpdatedAtMs: at}
	}
	if got, err := s.ListWebPushSubscriptions(ctx, "alice"); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	for _, x := range []WebPushSubscription{sub("a1", "alice", 10), sub("a2", "alice", 5), sub("b1", "bob", 7)} {
		if err := s.PutWebPushSubscription(ctx, x); err != nil {
			t.Fatalf("put %s: %v", x.ID, err)
		}
	}
	got, err := s.ListWebPushSubscriptions(ctx, "alice")
	if err != nil || len(got) != 2 || got[0].ID != "a2" || got[1].ID != "a1" || got[1].Endpoint != "https://push.example/a1" || got[1].P256dh != "pk-a1" || got[1].Auth != "au-a1" {
		t.Fatalf("alice subs = %+v, %v", got, err)
	}
	// Tenant isolation.
	if got, _ := s.ListWebPushSubscriptions(other, "alice"); len(got) != 0 {
		t.Fatalf("other tenant sees %+v", got)
	}
	// Re-subscribing the same endpoint under another member moves it and
	// keeps its creation time.
	moved := sub("a1", "bob", 20)
	if err := s.PutWebPushSubscription(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWebPushSubscriptions(ctx, "alice"); len(got) != 1 || got[0].ID != "a2" {
		t.Fatalf("alice after move = %+v", got)
	}
	bob, _ := s.ListWebPushSubscriptions(ctx, "bob")
	if len(bob) != 2 || bob[1].ID != "a1" || bob[1].CreatedAtMs != 10 || bob[1].UpdatedAtMs != 20 {
		t.Fatalf("bob after move = %+v", bob)
	}
	// Delete is owner-scoped.
	if err := s.DeleteWebPushSubscription(ctx, "alice", "b1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWebPushSubscriptions(ctx, "bob"); len(got) != 2 {
		t.Fatalf("foreign delete removed bob's row: %+v", got)
	}
	if err := s.DeleteWebPushSubscription(ctx, "bob", "b1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWebPushSubscriptions(ctx, "bob"); len(got) != 1 || got[0].ID != "a1" {
		t.Fatalf("bob after delete = %+v", got)
	}
	if err := s.DeleteWebPushSubscriptionsForUser(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWebPushSubscriptions(ctx, "bob"); len(got) != 0 {
		t.Fatalf("bob after delete-all = %+v", got)
	}
	if got, _ := s.ListWebPushSubscriptions(ctx, "alice"); len(got) != 1 {
		t.Fatalf("alice lost rows: %+v", got)
	}
}
