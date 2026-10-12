package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Contracts of the small room-scoped and per-user stores: message tasks,
// agent prefs, display names and organization sync.

// A re-put changes only the status and done fields; tasks list by seq; the
// (room, seq) key is never written for another tenant.
func TestContractMessageTaskStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx, octx := tenantCtx(uniq("t-task-")), tenantCtx(uniq("t-task-other-"))
		s := b.MessageTasks
		room := uniq("room-")
		if got := must(s.ListMessageTasks(ctx, room))(t); got == nil || len(got) != 0 {
			t.Fatalf("tasks of an empty room = %#v, want []", got)
		}
		task := wire.MessageTask{RoomID: room, ChannelID: "ch", Seq: 7, Status: "open", Title: "ship it", MessageAuthor: "jack", CreatedBy: "maya", CreatedAtMs: 10}
		stored, err := s.PutMessageTask(ctx, task)
		if err != nil || stored != task {
			t.Fatalf("put = %+v, %v; want %+v", stored, err, task)
		}
		if _, err := s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 3, Status: "open", Title: "earlier", CreatedBy: "maya", CreatedAtMs: 11}); err != nil {
			t.Fatal(err)
		}
		done := wire.MessageTask{RoomID: room, ChannelID: "other", Seq: 7, Status: "done", Title: "rewritten", MessageAuthor: "x", CreatedBy: "jack", CreatedAtMs: 99, DoneBy: "jack", DoneAtMs: 50}
		stored, err = s.PutMessageTask(ctx, done)
		want := task
		want.Status, want.DoneBy, want.DoneAtMs = "done", "jack", 50
		if err != nil || stored != want {
			t.Fatalf("re-put = %+v, %v; want only status and done fields changed: %+v", stored, err, want)
		}
		if got, ok := must2(s.GetMessageTask(ctx, room, 7))(t); !ok || got != want {
			t.Fatalf("GetMessageTask = %+v %v", got, ok)
		}
		var seqs []int64
		for _, tk := range must(s.ListMessageTasks(ctx, room))(t) {
			seqs = append(seqs, tk.Seq)
		}
		if !slices.Equal(seqs, []int64{3, 7}) {
			t.Fatalf("tasks = %v, want by seq", seqs)
		}
		// Reopening clears the done fields.
		if got, err := s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 7, Status: "open"}); err != nil || got.DoneBy != "" || got.DoneAtMs != 0 || got.Title != "ship it" {
			t.Fatalf("reopen = %+v, %v", got, err)
		}

		// Another tenant neither reads nor changes the room's tasks.
		if _, ok := must2(s.GetMessageTask(octx, room, 7))(t); ok {
			t.Fatal("cross-tenant get")
		}
		if l := must(s.ListMessageTasks(octx, room))(t); len(l) != 0 {
			t.Fatalf("cross-tenant list = %+v", l)
		}
		if _, err := s.PutMessageTask(octx, wire.MessageTask{RoomID: room, Seq: 7, Status: "done", DoneBy: "mallory", DoneAtMs: 1}); !errors.Is(err, ErrMessageTaskTaken) {
			t.Fatalf("put from another tenant = %v, want ErrMessageTaskTaken", err)
		}
		if got, _ := must2(s.GetMessageTask(ctx, room, 7))(t); got.Status != "open" || got.DoneBy != "" {
			t.Fatalf("another tenant's put changed the task: %+v", got)
		}
		if removed, err := s.DeleteMessageTask(octx, room, 7); err != nil || removed {
			t.Fatalf("cross-tenant delete = %v %v", removed, err)
		}
		if removed, err := s.DeleteMessageTask(ctx, room, 7); err != nil || !removed {
			t.Fatalf("delete = %v %v", removed, err)
		}
		if removed, err := s.DeleteMessageTask(ctx, room, 7); err != nil || removed {
			t.Fatalf("second delete = %v %v", removed, err)
		}
	})
}

// The selection persists per (tenant, user, room, channel) and a put replaces
// every field, the time included (the caller stamps it).
func TestContractAgentPrefsStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx, octx := tenantCtx(uniq("t-prefs-")), tenantCtx(uniq("t-prefs-other-"))
		s := b.AgentPrefs
		room := uniq("room-")
		if _, ok := must2(s.GetAgentPrefs(ctx, room, "ch1", "maya"))(t); ok {
			t.Fatal("absent prefs reported present")
		}
		p := AgentPrefs{RoomID: room, ChannelID: "ch1", OwnerID: "jack", Core: "nimbus", Provider: "codex", Model: "gpt-x", Reasoning: "high", UpdatedAtMs: 1}
		if err := s.PutAgentPrefs(ctx, p, "maya"); err != nil {
			t.Fatal(err)
		}
		if got, ok := must2(s.GetAgentPrefs(ctx, room, "ch1", "maya"))(t); !ok || got != p {
			t.Fatalf("got %+v %v, want %+v", got, ok, p)
		}
		// Room level (channel ""), another user, a case variant of the user
		// and another tenant are distinct rows.
		if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, OwnerID: "maya", Core: "nimbus", Provider: "claude-code", UpdatedAtMs: 2}, "maya"); err != nil {
			t.Fatal(err)
		}
		if got, _ := must2(s.GetAgentPrefs(ctx, room, "", "maya"))(t); got.Provider != "claude-code" || got.OwnerID != "maya" {
			t.Fatalf("room-level = %+v", got)
		}
		for _, c := range []struct {
			name string
			ctx  context.Context
			user string
		}{{"another user", ctx, "jack"}, {"a case variant", ctx, "Maya"}, {"another tenant", octx, "maya"}} {
			if _, ok := must2(s.GetAgentPrefs(c.ctx, room, "ch1", c.user))(t); ok {
				t.Fatalf("%s sees maya's prefs", c.name)
			}
		}
		// A put replaces the row, an unstamped time included.
		if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, ChannelID: "ch1", OwnerID: "maya", Core: "nimbus", Provider: "opencode"}, "maya"); err != nil {
			t.Fatal(err)
		}
		if got, _ := must2(s.GetAgentPrefs(ctx, room, "ch1", "maya"))(t); got.Provider != "opencode" || got.Model != "" || got.OwnerID != "maya" || got.UpdatedAtMs != 0 {
			t.Fatalf("replaced = %+v, want the new row as given", got)
		}
	})
}

// Display names are global per user id, exact (a case variant is another
// subject), and an empty name deletes the row.
func TestContractUserProfileStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Profiles
		maya, jack, bea := uniq("maya-")+"@x.io", uniq("jack-"), uniq("bea-")
		if got, err := s.DisplayNames(ctx, nil); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty lookup = %#v, %v", got, err)
		}
		if err := s.SetDisplayName(ctx, maya, "Maya M", 1); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDisplayName(ctx, jack, "Jack", 1); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDisplayName(ctx, jack, "Jackie", 2); err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(maya)
		got := must(s.DisplayNames(ctx, []string{maya, jack, bea, upper}))(t)
		if len(got) != 2 || got[maya] != "Maya M" || got[jack] != "Jackie" {
			t.Fatalf("lookup = %v (want maya and jack; bea and the upper-case variant absent)", got)
		}
		if err := s.SetDisplayName(ctx, jack, "", 3); err != nil {
			t.Fatal(err)
		}
		if got := must(s.DisplayNames(ctx, []string{jack}))(t); len(got) != 0 {
			t.Fatalf("after clear = %v", got)
		}
		if err := s.SetDisplayName(ctx, bea, "", 4); err != nil {
			t.Fatalf("clearing an absent name = %v", err)
		}
	})
}

// Organization sync: subjects upsert (nil groups read back as []), list by
// user id and delete; a default room is recorded once.
func TestContractOrgSyncStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.OrgSync
		a, z := "a-"+uniq("os-"), "z-"+uniq("os-")
		for _, sub := range []OrgSyncSubject{
			{UserID: z, Email: "z@x", Department: "Ops", Groups: []string{"b"}, SyncedAtMs: 1},
			{UserID: a, Email: "a@x", Department: "R&D", Groups: []string{"a", "b"}, SyncedAtMs: 5},
		} {
			if err := s.PutOrgSyncSubject(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutOrgSyncSubject(ctx, OrgSyncSubject{UserID: a, Email: "a2@x", DisplayName: "A", Department: "R&D", SyncedAtMs: 6}); err != nil {
			t.Fatal(err)
		}
		var mine []OrgSyncSubject
		for _, sub := range must(s.ListOrgSyncSubjects(ctx))(t) {
			if sub.UserID == a || sub.UserID == z {
				mine = append(mine, sub)
			}
		}
		if len(mine) != 2 || mine[0].UserID != a || mine[1].UserID != z {
			t.Fatalf("subjects = %+v, want by user id", mine)
		}
		if got := mine[0]; got.Email != "a2@x" || got.DisplayName != "A" || got.SyncedAtMs != 6 || got.Groups == nil || len(got.Groups) != 0 {
			t.Fatalf("upserted subject = %+v, want every field replaced and groups []", got)
		}
		if got := mine[1]; !slices.Equal(got.Groups, []string{"b"}) {
			t.Fatalf("groups = %v", got.Groups)
		}
		if err := s.DeleteOrgSyncSubject(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteOrgSyncSubject(ctx, a); err != nil {
			t.Fatalf("deleting an absent subject = %v", err)
		}
		for _, sub := range must(s.ListOrgSyncSubjects(ctx))(t) {
			if sub.UserID == a {
				t.Fatal("deleted subject still listed")
			}
		}

		room := uniq("room-")
		if done, err := s.DefaultRoomGranted(ctx, a, room); err != nil || done {
			t.Fatalf("granted before record = %v, %v", done, err)
		}
		for i := 0; i < 2; i++ {
			if err := s.RecordDefaultRoom(ctx, a, room, int64(7+i)); err != nil {
				t.Fatal(err)
			}
		}
		if done, err := s.DefaultRoomGranted(ctx, a, room); err != nil || !done {
			t.Fatalf("granted after record = %v, %v", done, err)
		}
		if done, _ := s.DefaultRoomGranted(ctx, strings.ToUpper(a), room); done {
			t.Fatal("a case variant of the user counts as granted")
		}
	})
}
