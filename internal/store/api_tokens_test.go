package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// The API token lifecycle on both backends: create (a taken id is
// ErrAPITokenExists), read, touch, list, revoke once (a revoke without its
// time is refused), and the expiry boundary (expired at now == expires).
func TestContractAPITokenStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.APITokens
		ctx := context.Background()
		id := uniq("tok-")
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
		// Live strictly before its expiry.
		if !got.Active(tok.ExpiresAtMs-1) || got.Active(tok.ExpiresAtMs) {
			t.Fatalf("active at expiry-1 / expiry = %v / %v, want true / false", got.Active(tok.ExpiresAtMs-1), got.Active(tok.ExpiresAtMs))
		}
		if err := s.TouchAPIToken(ctx, id, now+5); err != nil {
			t.Fatal(err)
		}
		if got, _ := must2(s.GetAPIToken(ctx, id))(t); got.LastUsedAtMs != now+5 {
			t.Fatalf("last used = %d", got.LastUsedAtMs)
		}
		// Newest first: a later token lists before this one.
		later := tok
		later.TokenID, later.CreatedAtMs = uniq("tok-later-"), now+1
		if err := s.CreateAPIToken(ctx, later); err != nil {
			t.Fatal(err)
		}
		pos := map[string]int{}
		for i, l := range must(s.ListAPITokens(ctx))(t) {
			pos[l.TokenID] = i + 1
		}
		if pos[id] == 0 || pos[later.TokenID] == 0 || pos[later.TokenID] > pos[id] {
			t.Fatalf("list positions = later %d, token %d; want both listed, the later first", pos[later.TokenID], pos[id])
		}
		if _, err := s.RevokeAPIToken(ctx, id, "root@example.com", 0); !errors.Is(err, ErrRevokeTimeRequired) {
			t.Fatalf("revoke at 0 = %v, want ErrRevokeTimeRequired", err)
		}
		if got, _ := must2(s.GetAPIToken(ctx, id))(t); got.RevokedAtMs != 0 || got.RevokedBy != "" {
			t.Fatalf("a refused revoke changed the token: %+v", got)
		}
		revoked, err := s.RevokeAPIToken(ctx, id, "root@example.com", now+10)
		if err != nil || !revoked {
			t.Fatalf("revoke = %v, %v", revoked, err)
		}
		if again, err := s.RevokeAPIToken(ctx, id, "root@example.com", now+11); err != nil || again {
			t.Fatalf("second revoke = %v, %v; want false", again, err)
		}
		got, _ = must2(s.GetAPIToken(ctx, id))(t)
		if got.Active(now+12) || got.RevokedAtMs != now+10 || got.RevokedBy != "root@example.com" {
			t.Fatalf("revoked token = %+v", got)
		}
		if _, ok, err := s.GetAPIToken(ctx, "missing-"+id); err != nil || ok {
			t.Fatalf("missing token = %v, %v", ok, err)
		}
		if revoked, err := s.RevokeAPIToken(ctx, "missing-"+id, "root", now); err != nil || revoked {
			t.Fatalf("revoke of a missing token = %v, %v", revoked, err)
		}
	})
}

// The outbox aggregates count pending (queued vs retrying) and finished
// items the way the metrics sampler reads them; online bridges count live
// heartbeats only.
func TestContractNotificationOutboxStoreStats(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		n := b.Notifications
		tid := uniq("t-ops-")
		ctx := tenant.NewContext(context.Background(), tid)
		before := must(n.NotificationOutboxStats(ctx))(t)
		// Due at small times (no other row is due then); the claim keeps
		// only this tenant's items.
		for i, created := range []int64{300, 100, 200} {
			if _, err := n.EnqueueNotification(ctx, NotificationOutboxItem{ID: uniq("e") + "-" + string(rune('a'+i)), Notifier: "x", EventJSON: []byte("{}"), NextAttemptAtMs: 5, CreatedAtMs: created}); err != nil {
				t.Fatal(err)
			}
		}
		token := uniq("k-")
		items := must(n.ClaimNotifications(ctx, NotificationClaim{Token: token, NowMs: 10, LeaseMs: 10, Limit: 50}))(t)
		var mine []NotificationOutboxItem
		for _, it := range items {
			if it.TenantID == tid {
				mine = append(mine, it)
			}
		}
		if len(mine) != 3 {
			t.Fatalf("claimed %+v", items)
		}
		// One retry, one delivered.
		if err := n.RecordNotificationAttempt(ctx, NotificationAttempt{TenantID: tid, ID: mine[0].ID, Notifier: "x", Token: token, Outcome: NotificationRetry, NextAttemptAtMs: 5000, AtMs: 10}); err != nil {
			t.Fatal(err)
		}
		if err := n.RecordNotificationAttempt(ctx, NotificationAttempt{TenantID: tid, ID: mine[1].ID, Notifier: "x", Token: token, Outcome: NotificationDelivered, AtMs: 10}); err != nil {
			t.Fatal(err)
		}
		st := must(n.NotificationOutboxStats(ctx))(t)
		if st.Queued-before.Queued != 1 || st.Retrying-before.Retrying != 1 || st.Delivered-before.Delivered != 1 || st.Failed != before.Failed {
			t.Fatalf("stats = %+v, before %+v", st, before)
		}
		if st.OldestPendingCreatedAtMs == 0 || st.OldestPendingCreatedAtMs > 300 {
			t.Fatalf("oldest pending = %d", st.OldestPendingCreatedAtMs)
		}

		jobs := b.BridgeJobs
		now := time.Now().UnixMilli()
		base := must(jobs.CountOnlineBridges(ctx, now))(t)
		owner := uniq("ops-owner-")
		_ = jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: uniq("live-"), OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now})
		_ = jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: uniq("stale-"), OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now - BridgeOfflineAfterMs - 1})
		_ = jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: uniq("drain-"), OwnerUserID: owner, Status: BridgeDraining, LastSeenAtMs: now})
		if got, err := jobs.CountOnlineBridges(ctx, now); err != nil || got-base != 1 {
			t.Fatalf("online = %d (base %d), %v; want one more", got, base, err)
		}
	})
}
