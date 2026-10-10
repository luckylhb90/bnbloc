package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// exerciseAPITokens is the behaviour both API token backends share.
func exerciseAPITokens(t *testing.T, s APITokenStore, id string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	tok := APIToken{
		TokenID: id, SecretHash: "ab12", Subject: "svc:smoke", Email: "", TenantHint: "acme",
		Label: "nightly smoke", CreatedBy: "root@example.com", CreatedAtMs: now, ExpiresAtMs: now + 60_000,
	}
	if err := s.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateAPIToken(ctx, tok); !errors.Is(err, ErrAPITokenExists) {
		t.Fatalf("duplicate create err = %v, want ErrAPITokenExists", err)
	}
	got, ok, err := s.GetAPIToken(ctx, id)
	if err != nil || !ok || got.Subject != "svc:smoke" || got.TenantHint != "acme" || got.Label != "nightly smoke" || !got.Active(now) {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}
	if err := s.TouchAPIToken(ctx, id, now+5); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetAPIToken(ctx, id); got.LastUsedAtMs != now+5 {
		t.Fatalf("last used = %d", got.LastUsedAtMs)
	}
	list, err := s.ListAPITokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range list {
		found = found || l.TokenID == id
	}
	if !found {
		t.Fatalf("token %s not listed", id)
	}
	revoked, err := s.RevokeAPIToken(ctx, id, "root@example.com", now+10)
	if err != nil || !revoked {
		t.Fatalf("revoke = %v, %v", revoked, err)
	}
	if again, _ := s.RevokeAPIToken(ctx, id, "root@example.com", now+11); again {
		t.Fatal("second revoke reported revoked")
	}
	got, _, _ = s.GetAPIToken(ctx, id)
	if got.Active(now+12) || got.RevokedAtMs != now+10 || got.RevokedBy != "root@example.com" {
		t.Fatalf("revoked token = %+v", got)
	}
	if _, ok, _ := s.GetAPIToken(ctx, "missing-"+id); ok {
		t.Fatal("missing token found")
	}
}

func TestMemoryAPITokens(t *testing.T) {
	exerciseAPITokens(t, NewMemoryAPITokens(), "0123456789abcdef0123456789abcdef")
	expired := APIToken{ExpiresAtMs: 100}
	if expired.Active(100) {
		t.Fatal("token active at its expiry")
	}
}

func TestMySQLAPITokens(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	id := strconv.FormatInt(time.Now().UnixNano(), 16)
	for len(id) < 32 {
		id += "0"
	}
	exerciseAPITokens(t, b.APITokens, id[:32])
}

// The operational aggregates count pending (queued vs retrying), finished
// items and online bridges the way the metrics samplers read them.
func TestMemoryOpsStats(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), "t1")
	n := NewMemoryNotifications()
	for i, created := range []int64{300, 100, 200} {
		if _, err := n.EnqueueNotification(ctx, NotificationOutboxItem{ID: "e" + strconv.Itoa(i), Notifier: "x", EventJSON: []byte("{}"), NextAttemptAtMs: 0, CreatedAtMs: created}); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := n.ClaimNotifications(ctx, NotificationClaim{Token: "k", NowMs: 1000, LeaseMs: 10, Limit: 2})
	if len(items) != 2 {
		t.Fatalf("claimed %d", len(items))
	}
	// One retry, one delivered.
	_ = n.RecordNotificationAttempt(ctx, NotificationAttempt{TenantID: "t1", ID: items[0].ID, Notifier: "x", Token: "k", Outcome: NotificationRetry, NextAttemptAtMs: 5000, AtMs: 1000})
	_ = n.RecordNotificationAttempt(ctx, NotificationAttempt{TenantID: "t1", ID: items[1].ID, Notifier: "x", Token: "k", Outcome: NotificationDelivered, AtMs: 1000})
	st, err := n.NotificationOutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Queued != 1 || st.Retrying != 1 || st.Delivered != 1 || st.Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if st.OldestPendingCreatedAtMs == 0 {
		t.Fatalf("oldest pending unset: %+v", st)
	}

	b := NewMemoryBridgeJobs()
	now := time.Now().UnixMilli()
	_ = b.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: "live", OwnerUserID: "a", Status: BridgeOnline, LastSeenAtMs: now})
	_ = b.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: "stale", OwnerUserID: "a", Status: BridgeOnline, LastSeenAtMs: now - BridgeOfflineAfterMs - 1})
	_ = b.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: "drain", OwnerUserID: "a", Status: BridgeDraining, LastSeenAtMs: now})
	if got, err := b.CountOnlineBridges(ctx, now); err != nil || got != 1 {
		t.Fatalf("online = %d, %v; want 1", got, err)
	}
}

func TestMySQLOpsStats(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := tenant.NewContext(context.Background(), "ops-stats")
	now := time.Now().UnixMilli()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := b.Notifications.EnqueueNotification(ctx, NotificationOutboxItem{ID: "ops-" + suffix, Notifier: "ops", EventJSON: []byte("{}"), NextAttemptAtMs: now + 3_600_000, CreatedAtMs: now - 1}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Notifications.NotificationOutboxStats(ctx)
	if err != nil || st.Queued < 1 || st.OldestPendingCreatedAtMs == 0 || st.OldestPendingCreatedAtMs > now-1 {
		t.Fatalf("outbox stats = %+v, %v", st, err)
	}
	if err := b.BridgeJobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: "ops-" + suffix, OwnerUserID: "ops", Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := b.BridgeJobs.CountOnlineBridges(ctx, now); err != nil || n < 1 {
		t.Fatalf("online bridges = %d, %v", n, err)
	}
}
