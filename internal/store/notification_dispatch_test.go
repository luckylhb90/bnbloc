package store

import (
	"errors"
	"testing"
)

// The dispatched outcome on both backends. Claims are cross-tenant, so the
// test reads back its own tenant's items only.
func TestContractNotificationOutboxStoreDispatched(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		testNotificationDispatched(t, b.Notifications, uniq("ntf-dsp-"))
	})
}

// A dispatched hand-off keeps the claim (the outcome is recorded under the
// same token), counts no attempt, replaces the body, and makes the item due
// again only at its deadline — where a new claim takes it over and the old
// token is refused.
func testNotificationDispatched(t *testing.T, s NotificationOutboxStore, tid string) {
	ctx := tenantCtx(tid)
	if _, err := s.EnqueueNotification(ctx, NotificationOutboxItem{ID: "d1", Notifier: "builtin:desktop", EventJSON: []byte(`{"v":1}`), NextAttemptAtMs: 20000, CreatedAtMs: 20000}); err != nil {
		t.Fatal(err)
	}
	claim := func(token string, now int64) []NotificationOutboxItem {
		t.Helper()
		items, err := s.ClaimNotifications(bg(), NotificationClaim{Token: token, NowMs: now, LeaseMs: 1000, Limit: 50})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		var mine []NotificationOutboxItem
		for _, it := range items {
			if it.TenantID == tid {
				mine = append(mine, it)
			}
		}
		return mine
	}
	record := func(token string, outcome NotificationOutcome, next int64, body string) error {
		return s.RecordNotificationAttempt(bg(), NotificationAttempt{
			TenantID: tid, ID: "d1", Notifier: "builtin:desktop", Token: token, Outcome: outcome,
			AtMs: 20001, NextAttemptAtMs: next, EventJSON: []byte(body),
		})
	}
	if got := claim("t1", 20000); len(got) != 1 {
		t.Fatalf("first claim = %+v", got)
	}
	if err := record("t1", NotificationDispatched, 25000, `{"v":2}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := claim("t2", 24000); len(got) != 0 {
		t.Fatalf("claimed before the deadline: %+v", got)
	}
	if err := record("t1", NotificationDispatched, 25000, `{"v":3}`); err != nil {
		t.Fatalf("narrow under the kept claim: %v", err)
	}
	got := claim("t3", 25000)
	if len(got) != 1 || !sameJSON(got[0].EventJSON, `{"v":3}`) || got[0].Attempts != 0 {
		t.Fatalf("deadline claim = %+v", got)
	}
	if err := record("t1", NotificationDelivered, 0, ""); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("old token after the deadline claim = %v, want lease lost", err)
	}
	if err := record("t3", NotificationDelivered, 0, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := claim("t4", 30000); len(got) != 0 {
		t.Fatalf("finished item claimed again: %+v", got)
	}
}
