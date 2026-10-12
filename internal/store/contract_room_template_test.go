package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomTemplateStore contract: visibility (author or tenant-shared), the
// list filters and order, author-only edits, moderator deletes, the use
// counter, insert-only ids and the list cap behave the same on both
// backends.
func TestContractRoomTemplateStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx, octx := tenantCtx(uniq("t-tpl-")), tenantCtx(uniq("t-tpl-other-"))
		s := b.Templates
		priv := wire.RoomTemplate{
			TemplateID: uniq("tpl-p-"), Name: "Standup", Visibility: wire.TemplatePrivate, CreatedBy: "maya",
			Body: wire.RoomTemplateBody{
				Channels:        []string{"design"},
				PromptPacks:     []wire.TemplateContentUse{{ComponentID: "cc_builtin_starters", Version: "1.0.0"}},
				SuggestedAgents: []wire.TemplateAgent{{OwnerID: "jack", Provider: "codex"}},
				Schedules: []wire.TemplateSchedule{{Text: "daily", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
					Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}, Timezone: "UTC"}},
			},
			CreatedAtMs: 1, UpdatedAtMs: 1,
		}
		shared := wire.RoomTemplate{TemplateID: uniq("tpl-s-"), Name: "Incident", Visibility: wire.TemplateTenant, CreatedBy: "bea", CreatedAtMs: 2, UpdatedAtMs: 2}
		tie := wire.RoomTemplate{TemplateID: uniq("tpl-t-"), Name: "Tie", Visibility: wire.TemplateTenant, CreatedBy: "bea", CreatedAtMs: 2, UpdatedAtMs: 2}
		for _, tp := range []wire.RoomTemplate{priv, shared, tie} {
			if _, err := s.CreateTemplate(ctx, tp); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.CreateTemplate(ctx, priv); !errors.Is(err, ErrTemplateExists) {
			t.Fatalf("re-create = %v, want ErrTemplateExists", err)
		}
		if _, err := s.CreateTemplate(octx, shared); !errors.Is(err, ErrTemplateExists) {
			t.Fatalf("re-create from another tenant = %v, want ErrTemplateExists", err)
		}
		got, err := s.GetTemplate(ctx, priv.TemplateID, "maya")
		if err != nil || got.Body.Channels[0] != "design" || got.Body.Schedules[0].Recurrence.Time != "09:00" || got.Body.DefaultMembers == nil || got.Name != "Standup" ||
			len(got.Body.PromptPacks) != 1 || got.Body.PromptPacks[0].Version != "1.0.0" {
			t.Fatalf("round trip = %+v, %v", got, err)
		}
		if got, _ := s.GetTemplate(ctx, shared.TemplateID, "maya"); got.Body.Channels == nil || got.Body.SuggestedAgents == nil || got.Body.PromptPacks == nil {
			t.Fatalf("empty body lists = %+v, want []", got.Body)
		}
		// Visibility and tenant scope.
		if _, err := s.GetTemplate(ctx, priv.TemplateID, "jack"); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("private visible to jack: %v", err)
		}
		if _, err := s.GetTemplate(ctx, priv.TemplateID, "Maya"); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("private visible to a case variant of its author: %v", err)
		}
		if _, err := s.GetTemplate(octx, shared.TemplateID, "bea"); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("cross-tenant get: %v", err)
		}
		// The list query, one filter at a time: viewer visibility, mineOnly,
		// newest updated first then id.
		ids := func(viewer string, mine bool) []string {
			t.Helper()
			list := must(s.ListTemplates(ctx, viewer, mine))(t)
			out := []string{}
			for _, tp := range list {
				out = append(out, tp.TemplateID)
			}
			return out
		}
		sharedPair := []string{minKey(shared.TemplateID, tie.TemplateID), maxKey(shared.TemplateID, tie.TemplateID)}
		for _, c := range []struct {
			name   string
			viewer string
			mine   bool
			want   []string
		}{
			{"another member sees the shared ones", "jack", false, sharedPair},
			{"the author sees hers too", "maya", false, append(append([]string{}, sharedPair...), priv.TemplateID)},
			{"mine only", "maya", true, []string{priv.TemplateID}},
			{"mine only, shared author", "bea", true, sharedPair},
			{"mine only, nothing", "jack", true, []string{}},
		} {
			if got := ids(c.viewer, c.mine); !slices.Equal(got, c.want) {
				t.Fatalf("%s: %v, want %v", c.name, got, c.want)
			}
		}
		if got := must(s.ListTemplates(octx, "bea", false))(t); got == nil || len(got) != 0 {
			t.Fatalf("another tenant's list = %#v, want []", got)
		}

		// Only the author edits; the same values twice succeed.
		name := "Daily standup"
		if _, err := s.UpdateTemplate(ctx, shared.TemplateID, "maya", TemplatePatch{Name: &name}, 5); !errors.Is(err, ErrTemplateForbidden) {
			t.Fatalf("non-author update = %v", err)
		}
		for i := 0; i < 2; i++ {
			up, err := s.UpdateTemplate(ctx, priv.TemplateID, "maya", TemplatePatch{Name: &name}, 5)
			if err != nil || up.Name != name || up.UpdatedAtMs != 5 || up.Body.Channels[0] != "design" {
				t.Fatalf("author update #%d = %+v, %v", i, up, err)
			}
		}
		if got := ids("maya", false); got[0] != priv.TemplateID {
			t.Fatalf("after the update the newest is %v", got)
		}
		if _, err := s.UpdateTemplate(ctx, uniq("nope-"), "maya", TemplatePatch{Name: &name}, 5); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("update of a missing template = %v", err)
		}
		// Use counter and deletes.
		if err := s.UseTemplate(ctx, shared.TemplateID, "jack"); err != nil {
			t.Fatal(err)
		}
		if err := s.UseTemplate(ctx, priv.TemplateID, "jack"); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("use of an invisible template = %v", err)
		}
		if g, _ := s.GetTemplate(ctx, shared.TemplateID, "jack"); g.UseCount != 1 {
			t.Fatalf("use count = %d", g.UseCount)
		}
		if err := s.DeleteTemplate(ctx, shared.TemplateID, "maya", false); !errors.Is(err, ErrTemplateForbidden) {
			t.Fatalf("non-author delete = %v", err)
		}
		if err := s.DeleteTemplate(ctx, priv.TemplateID, "admin", true); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("moderator delete of a private template = %v", err)
		}
		if err := s.DeleteTemplate(ctx, shared.TemplateID, "admin", true); err != nil {
			t.Fatalf("moderator delete = %v", err)
		}
		if err := s.DeleteTemplate(ctx, priv.TemplateID, "maya", false); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetTemplate(ctx, priv.TemplateID, "maya"); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("get after delete = %v", err)
		}

		// The list cap.
		bulk := tenantCtx(uniq("t-tpl-bulk-"))
		for i := 0; i < MaxTemplateList+2; i++ {
			if _, err := s.CreateTemplate(bulk, wire.RoomTemplate{TemplateID: uniq("tpl-b-"), Name: "b", Visibility: wire.TemplateTenant, CreatedBy: "maya", CreatedAtMs: 1, UpdatedAtMs: int64(i + 1)}); err != nil {
				t.Fatal(err)
			}
		}
		if got := must(s.ListTemplates(bulk, "jack", false))(t); len(got) != MaxTemplateList || got[0].UpdatedAtMs != MaxTemplateList+2 {
			t.Fatalf("capped list = %d templates (newest %d), want the newest %d", len(got), got[0].UpdatedAtMs, MaxTemplateList)
		}
	})
}
