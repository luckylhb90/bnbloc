package store

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// TestRoomsPreviewableMigrationKeepsSharedRoomsReadable: a database from the
// release before the switch (no rooms.previewable) gains the column; its
// shared rooms stay readable before joining, as they were, and its private
// rooms stay closed; a rerun is a no-op that never reopens a room an owner
// closed since.
func TestRoomsPreviewableMigrationKeepsSharedRoomsReadable(t *testing.T) {
	b, err := NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	run := migrationRun(t, "2026_10_rooms_previewable")
	for _, q := range []string{
		"ALTER TABLE rooms DROP COLUMN previewable",
		"INSERT INTO rooms (tenant_id, room_id, name, visibility, created_by, created_at_ms) VALUES ('t', 'shared-room', 's', 'shared', 'maya', 1)",
		"INSERT INTO rooms (tenant_id, room_id, name, visibility, created_by, created_at_ms) VALUES ('t', 'private-room', 'p', 'private', 'maya', 1)",
	} {
		if _, err := b.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	previewable := func(room string) bool {
		t.Helper()
		var on bool
		if err := b.DB.QueryRowContext(ctx, "SELECT previewable FROM rooms WHERE room_id = ?", room).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}
	if err := run(ctx, b.DB); err != nil {
		t.Fatal(err)
	}
	if !previewable("shared-room") || previewable("private-room") {
		t.Fatalf("after the migration shared=%v private=%v, want true and false", previewable("shared-room"), previewable("private-room"))
	}
	if _, err := b.DB.ExecContext(ctx, "UPDATE rooms SET previewable = 0 WHERE room_id = 'shared-room'"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, b.DB); err != nil {
		t.Fatal(err)
	}
	if previewable("shared-room") {
		t.Fatal("a rerun reopened a room its owner closed")
	}
}
