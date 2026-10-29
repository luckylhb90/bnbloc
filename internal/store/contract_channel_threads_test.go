package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The thread contract (docs/prd/2026-10-28-channels-and-threads.md §2.2,
// C1): a thread is a channel of kind thread with its parent and root
// message, one live thread per root, its section and agents switch its
// parent's (kept so when the parent changes), its replies counted, and a
// deleted thread frees its root. Both backends answer alike.
func TestContractChannelStoreThreads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-thr-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		parent, other := uniq("p-"), uniq("o-")
		for _, ch := range []RoomChannel{
			{ChannelID: parent, RoomID: room, Name: "releases", CreatedBy: "maya", OwnerID: "maya", CreatedAtMs: 1, SectionID: "sec-a",
				Settings: wire.ChannelSettings{WhoPosts: wire.ChannelPostersAdmins, SlowModeSeconds: 30}},
			{ChannelID: other, RoomID: room, Name: "random", CreatedBy: "maya", OwnerID: "maya", CreatedAtMs: 2},
		} {
			if err := s.PutChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		pch, _ := must2(s.GetChannel(ctx, room, parent))(t)
		thr := ThreadOf(pch, uniq("th-"), 7, "the login bug", "jack", 10)
		if err := s.PutChannel(ctx, thr); err != nil {
			t.Fatal(err)
		}
		if err := s.PutChannel(ctx, ThreadOf(pch, uniq("th-"), 7, "again", "maya", 11)); !errors.Is(err, ErrThreadExists) {
			t.Fatalf("a second thread of one root = %v, want ErrThreadExists", err)
		}
		got, ok := must2(s.ThreadOfRoot(ctx, room, parent, 7))(t)
		if !ok || got.ChannelID != thr.ChannelID || !got.IsThread() || got.Listed() || got.ParentID != parent || got.RootSeq != 7 ||
			got.SectionID != "sec-a" || got.Settings != (wire.ChannelSettings{}) || got.Name != "the login bug" {
			t.Fatalf("thread of root = %+v, %v", got, ok)
		}
		if _, ok := must2(s.ThreadOfRoot(ctx, room, other, 7))(t); ok {
			t.Fatal("another channel's message 7 has no thread")
		}
		// Replies: counted, the last time kept, people once each.
		for _, r := range []struct {
			who string
			at  int64
		}{{"jack", 20}, {"", 25}, {"maya", 30}, {"jack", 40}} {
			if _, err := s.NoteThreadReply(ctx, room, thr.ChannelID, r.who, r.at); err != nil {
				t.Fatal(err)
			}
		}
		got, _ = must2(s.GetChannel(ctx, room, thr.ChannelID))(t)
		if got.Thread.Replies != 4 || got.Thread.LastReplyAtMs != 40 || !slices.Equal(got.Thread.Participants, []string{"jack", "maya"}) {
			t.Fatalf("thread stats = %+v", got.Thread)
		}
		// A reply noted on a channel that is no thread changes nothing.
		if c, err := s.NoteThreadReply(ctx, room, other, "jack", 50); err != nil || c.Thread.Replies != 0 {
			t.Fatalf("a channel's note = %+v, %v", c.Thread, err)
		}
		// The parent moves to another section and bars agents: its threads follow.
		sec := "sec-b"
		if err := s.UpdateChannel(ctx, room, parent, ChannelPatch{SectionID: &sec, Settings: &wire.ChannelSettings{NoAgents: true, SlowModeSeconds: 5}}); err != nil {
			t.Fatal(err)
		}
		got, _ = must2(s.GetChannel(ctx, room, thr.ChannelID))(t)
		if got.SectionID != "sec-b" || !got.Settings.NoAgents || got.Settings.SlowModeSeconds != 0 {
			t.Fatalf("a thread after its parent changed = %+v", got)
		}
		if a, _, _ := s.ChannelAdmission(ctx, room, thr.ChannelID); a.Admits() {
			t.Fatal("a thread of a channel that bars agents admits them")
		}
		// Listing: the parent's threads, newest activity first; every thread.
		thr2 := ThreadOf(pch, uniq("th-"), 9, "second", "maya", 100)
		if err := s.PutChannel(ctx, thr2); err != nil {
			t.Fatal(err)
		}
		ids := func(cs []RoomChannel) []string {
			var out []string
			for _, c := range cs {
				out = append(out, c.ChannelID)
			}
			return out
		}
		if l := must(s.ListThreads(ctx, room, parent))(t); !slices.Equal(ids(l), []string{thr2.ChannelID, thr.ChannelID}) {
			t.Fatalf("threads of the parent = %v", ids(l))
		}
		if l := must(s.ListThreads(ctx, room, other))(t); len(l) != 0 {
			t.Fatalf("threads of a channel without any = %v", ids(l))
		}
		if l := must(s.ListThreads(ctx, room, ""))(t); len(l) != 2 {
			t.Fatalf("every thread = %v", ids(l))
		}
		// A deleted thread frees its root for a new one.
		if err := s.DeleteChannel(ctx, room, thr.ChannelID, 200); err != nil {
			t.Fatal(err)
		}
		if _, ok := must2(s.ThreadOfRoot(ctx, room, parent, 7))(t); ok {
			t.Fatal("a deleted thread is still the root's")
		}
		if err := s.PutChannel(ctx, ThreadOf(pch, uniq("th-"), 7, "reopened", "jack", 210)); err != nil {
			t.Fatalf("a new thread of a root whose thread was deleted: %v", err)
		}
	})
}
