package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestMemoryRoomTemplateStore(t *testing.T) {
	testRoomTemplateStore(t, NewMemoryRoomTemplates(), "tpl-mem")
}

func TestMySQLRoomTemplateStore(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	tid := "tpl-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM room_templates WHERE tenant_id LIKE ?`, tid+"%")
		_ = b.DB.Close()
	})
	testRoomTemplateStore(t, b.Templates, tid)
}

func testRoomTemplateStore(t *testing.T, s RoomTemplateStore, tid string) {
	ctx := tenant.NewContext(context.Background(), tid)
	other := tenant.NewContext(context.Background(), tid+"-other")
	priv := wire.RoomTemplate{
		TemplateID: tid + "-p", Name: "Standup", Visibility: wire.TemplatePrivate, CreatedBy: "alice",
		Body: wire.RoomTemplateBody{
			Channels:        []string{"design"},
			StarterPrompts:  []string{"what changed?"},
			SuggestedAgents: []wire.TemplateAgent{{OwnerID: "bob", Provider: "codex"}},
			Schedules: []wire.TemplateSchedule{{Text: "daily", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
				Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}, Timezone: "UTC"}},
		},
		CreatedAtMs: 1, UpdatedAtMs: 1,
	}
	if _, err := s.CreateTemplate(ctx, priv); err != nil {
		t.Fatal(err)
	}
	shared := wire.RoomTemplate{TemplateID: tid + "-s", Name: "Incident", Visibility: wire.TemplateTenant, CreatedBy: "carol", CreatedAtMs: 2, UpdatedAtMs: 2}
	if _, err := s.CreateTemplate(ctx, shared); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTemplate(ctx, priv.TemplateID, "alice")
	if err != nil || got.Body.Channels[0] != "design" || got.Body.Schedules[0].Recurrence.Time != "09:00" || got.Body.DefaultMembers == nil {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	// Private is invisible to others; tenant scope holds.
	if _, err := s.GetTemplate(ctx, priv.TemplateID, "bob"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("private visible to bob: %v", err)
	}
	if _, err := s.GetTemplate(other, shared.TemplateID, "carol"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if l, _ := s.ListTemplates(ctx, "bob", false); len(l) != 1 || l[0].TemplateID != shared.TemplateID {
		t.Fatalf("bob list = %+v", l)
	}
	if l, _ := s.ListTemplates(ctx, "alice", false); len(l) != 2 || l[0].TemplateID != shared.TemplateID {
		t.Fatalf("alice list = %+v", l)
	}
	if l, _ := s.ListTemplates(ctx, "alice", true); len(l) != 1 {
		t.Fatalf("alice mine = %+v", l)
	}
	// Only the author edits; a moderator deletes tenant-visible only.
	name := "Daily standup"
	if _, err := s.UpdateTemplate(ctx, shared.TemplateID, "alice", TemplatePatch{Name: &name}, 5); !errors.Is(err, ErrTemplateForbidden) {
		t.Fatalf("non-author update = %v", err)
	}
	up, err := s.UpdateTemplate(ctx, priv.TemplateID, "alice", TemplatePatch{Name: &name}, 5)
	if err != nil || up.Name != name || up.UpdatedAtMs != 5 {
		t.Fatalf("author update = %+v, %v", up, err)
	}
	if err := s.DeleteTemplate(ctx, shared.TemplateID, "alice", false); !errors.Is(err, ErrTemplateForbidden) {
		t.Fatalf("non-author delete = %v", err)
	}
	if err := s.DeleteTemplate(ctx, priv.TemplateID, "admin", true); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("moderator delete of a private template = %v", err)
	}
	if err := s.UseTemplate(ctx, shared.TemplateID, "bob"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetTemplate(ctx, shared.TemplateID, "bob"); g.UseCount != 1 {
		t.Fatalf("use count = %d", g.UseCount)
	}
	if err := s.DeleteTemplate(ctx, shared.TemplateID, "admin", true); err != nil {
		t.Fatalf("moderator delete = %v", err)
	}
	if err := s.DeleteTemplate(ctx, priv.TemplateID, "alice", false); err != nil {
		t.Fatal(err)
	}
}
