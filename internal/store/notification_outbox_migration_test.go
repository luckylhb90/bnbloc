package store

import (
	"context"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A database migrated before the outbox became per-notifier still has the
// per-event table (the 2026-10-08 production incident: every enqueue failed).
// The converging migration must replace it so enqueues work again, and must
// leave a current-shape table alone.
func TestNotificationOutboxPerNotifierMigrationConvergesOldShape(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	run := migrationRun(t, "2026_10_notification_outbox_per_notifier")

	if _, err := b.DB.ExecContext(ctx, "DROP TABLE notification_outbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DB.ExecContext(ctx, `CREATE TABLE notification_outbox (
  tenant_id VARCHAR(64) NOT NULL, id VARCHAR(64) NOT NULL, event_json TEXT NOT NULL,
  attempts INT NOT NULL DEFAULT 0, next_attempt_at_ms BIGINT NOT NULL,
  claim_token VARCHAR(64) NOT NULL DEFAULT '', delivered_at_ms BIGINT NULL, failed_at_ms BIGINT NULL,
  last_error VARCHAR(512) NOT NULL DEFAULT '', created_at_ms BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		t.Fatal(err)
	}
	tctx := tenant.NewContext(ctx, "outbox-migration")
	item := NotificationOutboxItem{ID: "ntf_mig", Notifier: "corp", EventJSON: []byte("{}"), NextAttemptAtMs: time.Now().UnixMilli(), CreatedAtMs: time.Now().UnixMilli()}
	if _, err := b.Notifications.EnqueueNotification(tctx, item); err == nil {
		t.Fatal("enqueue into the old-shape table succeeded; the repro is wrong")
	}
	if err := run(ctx, b.DB); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ok, err := b.Notifications.EnqueueNotification(tctx, item); err != nil || !ok {
		t.Fatalf("enqueue after migration = %v, %v", ok, err)
	}
	// A current-shape table (with its queued row) is left untouched.
	if err := run(ctx, b.DB); err != nil {
		t.Fatalf("migration rerun: %v", err)
	}
	if ok, err := b.Notifications.EnqueueNotification(tctx, item); err != nil || ok {
		t.Fatalf("re-enqueue after rerun = %v, %v; want the existing row kept (duplicate ignored)", ok, err)
	}
}

func migrationRun(t *testing.T, id string) func(context.Context, sqlConn) error {
	t.Helper()
	for _, m := range migrations {
		if m.id == id {
			return m.run
		}
	}
	t.Fatalf("no migration %s", id)
	return nil
}
