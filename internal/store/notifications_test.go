package store

import (
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

type notificationStores interface {
	NotificationPrefsStore
	NotificationOutboxStore
}

func TestMemoryNotificationStores(t *testing.T) {
	testNotificationStores(t, NewMemoryNotifications(), "ntf-mem")
}

// TestMySQLNotificationStores runs the same contract against MySQL (gated on
// BNBLOC_TEST_MYSQL_DSN). The outbox claim is cross-tenant, so the test only
// works with timestamps far in the past (no live row is due at NowMs<=10000)
// and removes its own tenant's rows afterwards.
func TestMySQLNotificationStores(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	tid := "ntf-it-" + itoaBase(time.Now().UnixNano())
	// Rows of earlier (crashed) runs would be due at the test's small clock.
	_, _ = b.DB.Exec(`DELETE FROM notification_outbox WHERE tenant_id LIKE 'ntf-it-%'`)
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM notification_outbox WHERE tenant_id=?`, tid)
		_, _ = b.DB.Exec(`DELETE FROM notification_prefs WHERE tenant_id LIKE ?`, tid+"%")
		_ = b.DB.Close()
	})
	testNotificationStores(t, NewMySQLNotifications(b.DB), tid)
}

func testNotificationStores(t *testing.T, s notificationStores, tid string) {
	ctx := tenantCtx(tid)
	other := tenantCtx(tid + "-other")

	// Prefs: absent -> found=false; round-trip; tenant-isolated.
	if _, ok, err := s.GetNotificationPrefs(ctx, "alice"); err != nil || ok {
		t.Fatalf("absent prefs = ok %v err %v", ok, err)
	}
	want := NotificationPrefs{Mentions: true, AgentFinished: false, AgentFailed: true, BorrowRequests: false, ContentShared: false, ContentReview: true, QuietRooms: []string{"r1", "r2"}, UpdatedAtMs: 42}
	if err := s.PutNotificationPrefs(ctx, "alice", want); err != nil {
		t.Fatalf("put prefs: %v", err)
	}
	got, ok, err := s.GetNotificationPrefs(ctx, "alice")
	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("prefs round-trip = %+v ok=%v err=%v, want %+v", got, ok, err, want)
	}
	if _, ok, _ := s.GetNotificationPrefs(other, "alice"); ok {
		t.Fatal("prefs leaked across tenants")
	}
	want.QuietRooms = nil
	if err := s.PutNotificationPrefs(ctx, "alice", want); err != nil {
		t.Fatalf("put prefs: %v", err)
	}
	if got, _, _ := s.GetNotificationPrefs(ctx, "alice"); len(got.QuietRooms) != 0 {
		t.Fatalf("quiet rooms not replaced: %v", got.QuietRooms)
	}

	// Outbox: enqueue is idempotent per (tenant, id, notifier).
	enq := func(id string, next int64) {
		t.Helper()
		ins, err := s.EnqueueNotification(ctx, NotificationOutboxItem{ID: id, Notifier: "n1", EventJSON: []byte(`{"id":"` + id + `"}`), NextAttemptAtMs: next, CreatedAtMs: 1})
		if err != nil || !ins {
			t.Fatalf("enqueue %s = %v %v", id, ins, err)
		}
	}
	enq(tid+"-a", 10)
	enq(tid+"-b", 20)
	enq(tid+"-c", 5000)
	if ins, err := s.EnqueueNotification(ctx, NotificationOutboxItem{ID: tid + "-a", Notifier: "n1", EventJSON: []byte(`{}`), NextAttemptAtMs: 1, CreatedAtMs: 1}); err != nil || ins {
		t.Fatalf("duplicate enqueue = %v %v, want no-op", ins, err)
	}
	// The same event for a second notifier is its own row (own retries and
	// outcome); due far in the future so the claims below never see it.
	if ins, err := s.EnqueueNotification(ctx, NotificationOutboxItem{ID: tid + "-a", Notifier: "n2", EventJSON: []byte(`{}`), NextAttemptAtMs: 1 << 40, CreatedAtMs: 1}); err != nil || !ins {
		t.Fatalf("second-notifier enqueue = %v %v, want inserted", ins, err)
	}

	ids := func(items []NotificationOutboxItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.ID)
		}
		sort.Strings(out)
		return out
	}
	claim := func(token string, now int64) []NotificationOutboxItem {
		t.Helper()
		items, err := s.ClaimNotifications(bg(), NotificationClaim{Token: token, NowMs: now, LeaseMs: 1000, Limit: 10})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return items
	}
	first := claim("t1", 100)
	if got := ids(first); !reflect.DeepEqual(got, []string{tid + "-a", tid + "-b"}) {
		t.Fatalf("first claim = %v", got)
	}
	if string(first[0].EventJSON) == "" || first[0].TenantID != tid {
		t.Fatalf("claimed item lost its body/tenant: %+v", first[0])
	}
	// Leased items are not claimable by another worker before the lease lapses.
	if got := claim("t2", 100); len(got) != 0 {
		t.Fatalf("second worker claimed leased items: %v", ids(got))
	}
	record := func(a NotificationAttempt) error {
		a.TenantID = tid
		if a.Notifier == "" {
			a.Notifier = "n1"
		}
		return s.RecordNotificationAttempt(bg(), a)
	}
	if err := record(NotificationAttempt{ID: tid + "-a", Token: "t1", Outcome: NotificationDelivered, AtMs: 150}); err != nil {
		t.Fatalf("deliver a: %v", err)
	}
	if err := record(NotificationAttempt{ID: tid + "-a", Token: "t1", Outcome: NotificationDelivered, AtMs: 151}); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("double finish = %v, want lease lost", err)
	}
	if err := record(NotificationAttempt{ID: tid + "-a", Notifier: "n2", Token: "t1", Outcome: NotificationDelivered, AtMs: 151}); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("finishing another notifier's row = %v, want lease lost", err)
	}
	if err := record(NotificationAttempt{ID: tid + "-b", Token: "wrong", Outcome: NotificationRetry, NextAttemptAtMs: 200}); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("foreign token = %v, want lease lost", err)
	}
	if err := record(NotificationAttempt{ID: tid + "-b", Token: "t1", Outcome: NotificationRetry, NextAttemptAtMs: 200, Error: "HTTP 503", EventJSON: []byte(`{"narrowed":true}`)}); err != nil {
		t.Fatalf("retry b: %v", err)
	}
	// b is due again at 200; a is finished; c not yet due.
	third := claim("t3", 1200)
	if got := ids(third); !reflect.DeepEqual(got, []string{tid + "-b"}) {
		t.Fatalf("third claim = %v", got)
	}
	if third[0].Attempts != 1 || third[0].LastError != "HTTP 503" || string(third[0].EventJSON) != `{"narrowed":true}` {
		t.Fatalf("retry bookkeeping = attempts %d err %q body %s", third[0].Attempts, third[0].LastError, third[0].EventJSON)
	}
	// t3 crashes: after the lease lapses another worker re-claims, and t3's
	// late report cannot clobber it.
	if got := ids(claim("t4", 2201)); !reflect.DeepEqual(got, []string{tid + "-b"}) {
		t.Fatalf("lease-lapse re-claim = %v", got)
	}
	if err := record(NotificationAttempt{ID: tid + "-b", Token: "t3", Outcome: NotificationDelivered, AtMs: 2300}); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("stale worker report = %v, want lease lost", err)
	}
	if err := record(NotificationAttempt{ID: tid + "-b", Token: "t4", Outcome: NotificationFailed, AtMs: 2300, Error: "gave up"}); err != nil {
		t.Fatalf("fail b: %v", err)
	}
	// Prune removes finished items only.
	if n, err := s.PruneNotifications(bg(), 2, 100); err != nil || n < 2 {
		t.Fatalf("prune = %d %v, want >= 2", n, err)
	}
	if got := ids(claim("t5", 6000)); !reflect.DeepEqual(got, []string{tid + "-c"}) {
		t.Fatalf("after prune = %v, want only the pending item", got)
	}
	if err := record(NotificationAttempt{ID: tid + "-c", Token: "t5", Outcome: NotificationDelivered, AtMs: 6001}); err != nil {
		t.Fatalf("deliver c: %v", err)
	}

	// Concurrent workers (replicas) never claim the same item twice.
	for i := 0; i < 20; i++ {
		enq(tid+"-p"+itoaBase(int64(i)), 7000)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for round := 0; round < 3; round++ {
				items, err := s.ClaimNotifications(bg(), NotificationClaim{Token: "w" + itoaBase(int64(w)) + "r" + itoaBase(int64(round)), NowMs: 7100, LeaseMs: 100000, Limit: 5})
				if err != nil {
					t.Errorf("concurrent claim: %v", err)
					return
				}
				mu.Lock()
				for _, it := range items {
					seen[it.ID]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("concurrent claims covered %d items, want 20", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("item %s claimed %d times", id, n)
		}
	}
}
