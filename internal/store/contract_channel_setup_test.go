package store

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The channel setup contract (docs/prd/2026-10-28-channels-and-threads.md,
// slice 1): a channel's purpose, rules, section, position, owner, archive
// mark and settings; sections; follows and the slow-mode claim; a room's
// rules and the agreement to them. Both backends answer alike.

func ptr[T any](v T) *T { return &v }

func TestContractChannelStoreSetup(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-chs-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		def, a, c := uniq("d-"), uniq("a-"), uniq("c-")
		agent := &wire.ChannelAgent{OwnerID: "jack", Core: "nimbus", Provider: "codex"}
		for _, ch := range []RoomChannel{
			{ChannelID: def, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "maya", OwnerID: "maya", CreatedAtMs: 1},
			{ChannelID: a, RoomID: room, Name: "alpha", CreatedBy: "jack", OwnerID: "jack", CreatedAtMs: 2, Position: 5,
				Purpose: "releases", Rules: "be kind", SectionID: "sec-1",
				Settings: wire.ChannelSettings{DefaultAgent: agent, WhoPosts: wire.ChannelPostersAdmins, SlowModeSeconds: 30, AgentParticipation: wire.ChannelAgentsQuestions}},
			{ChannelID: c, RoomID: room, Name: "gamma", CreatedBy: "jack", OwnerID: "jack", CreatedAtMs: 3, Position: 1},
		} {
			if err := s.PutChannel(ctx, ch); err != nil {
				t.Fatalf("put %s: %v", ch.Name, err)
			}
		}
		// The store keeps its own copy of the settings' default agent.
		agent.OwnerID = "changed"
		got, ok := must2(s.GetChannel(ctx, room, a))(t)
		if !ok || got.Purpose != "releases" || got.Rules != "be kind" || got.SectionID != "sec-1" || got.Position != 5 || got.OwnerID != "jack" ||
			got.Settings.DefaultAgent == nil || *got.Settings.DefaultAgent != (wire.ChannelAgent{OwnerID: "jack", Core: "nimbus", Provider: "codex"}) ||
			got.Settings.WhoPosts != wire.ChannelPostersAdmins || got.Settings.SlowModeSeconds != 30 || got.Settings.AgentParticipation != wire.ChannelAgentsQuestions ||
			got.Settings.NoAgents || got.Archived() {
			t.Fatalf("GetChannel = %+v (agent %+v) %v", got, got.Settings.DefaultAgent, ok)
		}
		// Default first, then position, then age.
		if ids := channelIDs(must(s.ListChannels(ctx, room))(t)); !slices.Equal(ids, []string{def, c, a}) {
			t.Fatalf("ListChannels = %v, want [%s %s %s]", ids, def, c, a)
		}

		// A patch changes only what it names; settings are replaced whole.
		if err := s.UpdateChannel(ctx, room, a, ChannelPatch{Purpose: ptr("ship it"), Rules: ptr(""), SectionID: ptr(""), Position: ptr(0), OwnerID: ptr("maya"),
			Settings: &wire.ChannelSettings{NoAgents: true}}); err != nil {
			t.Fatal(err)
		}
		got, _ = must2(s.GetChannel(ctx, room, a))(t)
		if got.Name != "alpha" || got.Purpose != "ship it" || got.Rules != "" || got.SectionID != "" || got.Position != 0 || got.OwnerID != "maya" ||
			got.Settings != (wire.ChannelSettings{NoAgents: true}) {
			t.Fatalf("after patch = %+v", got)
		}
		if adm, found, err := s.ChannelAdmission(ctx, room, a); err != nil || !found || !adm.NoAgents {
			t.Fatalf("admission after NoAgents = %+v %v %v", adm, found, err)
		}
		if err := s.UpdateChannel(ctx, room, a, ChannelPatch{}); err != nil {
			t.Fatalf("empty patch of a live channel = %v", err)
		}

		// Archive keeps the first time, stays readable and listed; restore clears it.
		if err := s.SetChannelArchived(ctx, room, c, ptr(int64(40))); err != nil {
			t.Fatal(err)
		}
		if err := s.SetChannelArchived(ctx, room, c, ptr(int64(50))); err != nil {
			t.Fatal(err)
		}
		if got, ok := must2(s.GetChannel(ctx, room, c))(t); !ok || got.ArchivedAtMs == nil || *got.ArchivedAtMs != 40 {
			t.Fatalf("archived channel = %+v %v", got, ok)
		}
		if ids := channelIDs(must(s.ListChannels(ctx, room))(t)); !slices.Contains(ids, c) {
			t.Fatalf("an archived channel left ListChannels: %v", ids)
		}
		if err := s.SetChannelArchived(ctx, room, c, nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := must2(s.GetChannel(ctx, room, c))(t); got.Archived() {
			t.Fatalf("restored channel still archived: %+v", got)
		}
		if err := s.DeleteChannel(ctx, room, c, 60); err != nil {
			t.Fatal(err)
		}
		if err := s.SetChannelArchived(ctx, room, c, ptr(int64(70))); !errors.Is(err, ErrChannelNotFound) {
			t.Fatalf("archive of a deleted channel = %v, want ErrChannelNotFound", err)
		}
	})
}

func TestContractChannelStoreSections(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-sec-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		if got := must(s.ListSections(ctx, room))(t); got == nil || len(got) != 0 {
			t.Fatalf("sections of a room without any = %#v, want []", got)
		}
		s1, s2 := uniq("s1-"), uniq("s2-")
		for _, sec := range []RoomSection{
			{SectionID: s1, RoomID: room, Name: "Design", Position: 2, Visibility: wire.SectionOpen, Owners: []string{"maya"}, JoinPolicy: "open", CreatedBy: "maya", CreatedAtMs: 1},
			{SectionID: s2, RoomID: room, Name: "Support", Position: 1, Visibility: wire.SectionOpen, Rules: "reply within a day", JoinPolicy: "open", CreatedBy: "jack", CreatedAtMs: 2},
		} {
			if err := s.PutSection(ctx, sec); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutSection(ctx, RoomSection{SectionID: s1, RoomID: room, Name: "again", CreatedBy: "jack"}); !errors.Is(err, ErrSectionExists) {
			t.Fatalf("re-put of a section id = %v, want ErrSectionExists", err)
		}
		list := must(s.ListSections(ctx, room))(t)
		if len(list) != 2 || list[0].SectionID != s2 || list[1].SectionID != s1 {
			t.Fatalf("ListSections = %+v, want by position", list)
		}
		if list[0].Rules != "reply within a day" || list[0].Owners == nil || len(list[0].Owners) != 0 {
			t.Fatalf("section read = %+v", list[0])
		}
		if err := s.UpdateSection(ctx, room, s2, SectionPatch{Name: ptr("Help"), Owners: ptr([]string{"jack", "Maya"})}); err != nil {
			t.Fatal(err)
		}
		got, ok := must2(s.GetSection(ctx, room, s2))(t)
		if !ok || got.Name != "Help" || !slices.Equal(got.Owners, []string{"jack", "Maya"}) || got.Rules != "reply within a day" {
			t.Fatalf("after patch = %+v %v", got, ok)
		}
		// Owners compare exactly.
		if !got.IsOwner("Maya") || got.IsOwner("maya") {
			t.Fatalf("IsOwner folds case: %+v", got.Owners)
		}
		if err := s.UpdateSection(ctx, room, uniq("nope-"), SectionPatch{Name: ptr("x")}); !errors.Is(err, ErrSectionNotFound) {
			t.Fatalf("patch of an unknown section = %v", err)
		}
		// Deleting a section moves its channels to the top level.
		in, out := uniq("in-"), uniq("out-")
		for _, ch := range []RoomChannel{
			{ChannelID: in, RoomID: room, Name: "in", SectionID: s2, CreatedBy: "jack", CreatedAtMs: 3},
			{ChannelID: out, RoomID: room, Name: "out", SectionID: s1, CreatedBy: "jack", CreatedAtMs: 4},
		} {
			if err := s.PutChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteSection(ctx, room, s2, 10); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSection(ctx, room, s2, 20); err != nil {
			t.Fatalf("second delete = %v", err)
		}
		if _, ok := must2(s.GetSection(ctx, room, s2))(t); ok {
			t.Fatal("a deleted section is still readable")
		}
		if err := s.UpdateSection(ctx, room, s2, SectionPatch{Name: ptr("back")}); !errors.Is(err, ErrSectionNotFound) {
			t.Fatalf("patch of a deleted section = %v", err)
		}
		if got, _ := must2(s.GetChannel(ctx, room, in))(t); got.SectionID != "" {
			t.Fatalf("a deleted section's channel = %+v, want the top level", got)
		}
		if got, _ := must2(s.GetChannel(ctx, room, out))(t); got.SectionID != s1 {
			t.Fatalf("another section's channel moved: %+v", got)
		}
		// Another tenant sees none of it.
		other := tenantCtx(uniq("t-sec-b-"))
		if got := must(s.ListSections(other, room))(t); len(got) != 0 {
			t.Fatalf("tenant B lists %+v", got)
		}
		if err := s.UpdateSection(other, room, s1, SectionPatch{Name: ptr("hijack")}); !errors.Is(err, ErrSectionNotFound) {
			t.Fatalf("tenant B patch = %v", err)
		}
		// The room's delete takes its sections.
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if got := must(s.ListSections(ctx, room))(t); len(got) != 0 {
			t.Fatalf("sections after the room's delete = %+v", got)
		}
	})
}

func TestContractChannelStoreFollows(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-fol-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		a, c := uniq("a-"), uniq("c-")
		if err := s.FollowChannel(ctx, room, a, "maya", 10, FollowMarks{}); err != nil {
			t.Fatal(err)
		}
		// A repeat keeps the join time and applies only the marks it names.
		if err := s.FollowChannel(ctx, room, a, "maya", 20, FollowMarks{Starred: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if err := s.FollowChannel(ctx, room, a, "maya", 30, FollowMarks{Hidden: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if err := s.FollowChannel(ctx, room, c, "maya", 40, FollowMarks{Hidden: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if err := s.FollowChannel(ctx, room, a, "Maya", 50, FollowMarks{}); err != nil {
			t.Fatal(err)
		}
		follows := must(s.ChannelFollows(ctx, room, "maya"))(t)
		byID := map[string]ChannelFollow{}
		for _, f := range follows {
			byID[f.ChannelID] = f
		}
		if len(follows) != 2 || byID[a].JoinedAtMs != 10 || !byID[a].Starred || !byID[a].Hidden || byID[c].Starred || !byID[c].Hidden {
			t.Fatalf("follows = %+v", follows)
		}
		if got := must(s.ChannelFollowerCounts(ctx, room))(t); got[a] != 2 || got[c] != 1 {
			t.Fatalf("follower counts = %v (subjects are case-sensitive)", got)
		}
		if err := s.UnfollowChannel(ctx, room, a, "maya"); err != nil {
			t.Fatal(err)
		}
		if err := s.UnfollowChannel(ctx, room, a, "maya"); err != nil {
			t.Fatalf("second unfollow = %v", err)
		}
		if got := must(s.ChannelFollows(ctx, room, "maya"))(t); len(got) != 1 || got[0].ChannelID != c {
			t.Fatalf("follows after unfollow = %+v", got)
		}

		// Posting joins: no slow mode always claims and records the post.
		if wait := must(s.ClaimChannelPost(ctx, room, a, "jack", 1000, 0))(t); wait != 0 {
			t.Fatalf("claim without slow mode = %d", wait)
		}
		if wait := must(s.ClaimChannelPost(ctx, room, a, "jack", 1001, 0))(t); wait != 0 {
			t.Fatalf("second claim without slow mode = %d", wait)
		}
		jf := must(s.ChannelFollows(ctx, room, "jack"))(t)
		if len(jf) != 1 || jf[0].JoinedAtMs != 1000 || jf[0].LastPostedAtMs != 1001 {
			t.Fatalf("a post's follow = %+v", jf)
		}
		// Slow mode: within the gap a claim is refused with the wait and
		// writes nothing; at the gap it passes.
		if wait := must(s.ClaimChannelPost(ctx, room, a, "jack", 5000, 10_000))(t); wait != 6001 {
			t.Fatalf("claim within the gap = %d, want 6001", wait)
		}
		if got := must(s.ChannelFollows(ctx, room, "jack"))(t); got[0].LastPostedAtMs != 1001 {
			t.Fatalf("a refused claim wrote: %+v", got)
		}
		if wait := must(s.ClaimChannelPost(ctx, room, a, "jack", 11_001, 10_000))(t); wait != 0 {
			t.Fatalf("claim at the gap = %d", wait)
		}
		// A first post in slow mode passes.
		if wait := must(s.ClaimChannelPost(ctx, room, c, "jack", 11_002, 10_000))(t); wait != 0 {
			t.Fatalf("first claim in slow mode = %d", wait)
		}
		// Concurrent posts at one time: exactly one passes.
		d := uniq("d-")
		var wg sync.WaitGroup
		waits := make([]int64, 8)
		errs := make([]error, 8)
		for i := range waits {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				waits[i], errs[i] = s.ClaimChannelPost(ctx, room, d, "erin", 100_000+int64(i), 60_000)
			}(i)
		}
		wg.Wait()
		passed := 0
		for i := range waits {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if waits[i] == 0 {
				passed++
			}
		}
		if passed != 1 {
			t.Fatalf("concurrent claims passed = %d, want 1 (%v)", passed, waits)
		}
		// Another tenant reads none of it; the room's delete takes it.
		if got := must(s.ChannelFollows(tenantCtx(uniq("t-fol-b-")), room, "jack"))(t); len(got) != 0 {
			t.Fatalf("tenant B reads follows: %+v", got)
		}
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if got := must(s.ChannelFollows(ctx, room, "jack"))(t); len(got) != 0 {
			t.Fatalf("follows after the room's delete = %+v", got)
		}
	})
}

// A member's groups (C24) are theirs alone; a follow names one, and the
// group's delete takes its channels out of it; the room's delete takes all.
func TestContractChannelStoreGroups(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-grp-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		g1, g2 := uniq("g1-"), uniq("g2-")
		for _, g := range []ChannelGroup{
			{RoomID: room, UserID: "maya", GroupID: g1, Name: "Mine", Position: 1, CreatedAtMs: 10},
			{RoomID: room, UserID: "maya", GroupID: g2, Name: "Later", Position: 0, CreatedAtMs: 20},
			{RoomID: room, UserID: "jack", GroupID: uniq("gj-"), Name: "Jack's", CreatedAtMs: 30},
		} {
			if err := s.PutChannelGroup(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutChannelGroup(ctx, ChannelGroup{RoomID: room, UserID: "maya", GroupID: g1, Name: "Again", CreatedAtMs: 40}); !errors.Is(err, ErrChannelGroupExists) {
			t.Fatalf("a taken id = %v", err)
		}
		got := must(s.ChannelGroups(ctx, room, "maya"))(t)
		if len(got) != 2 || got[0].GroupID != g2 || got[1].GroupID != g1 || got[1].Name != "Mine" {
			t.Fatalf("maya's groups = %+v (by position, never jack's)", got)
		}
		if err := s.UpdateChannelGroup(ctx, room, "maya", g1, ChannelGroupPatch{Name: ptr("Focus"), Position: ptr(-1)}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateChannelGroup(ctx, room, "jack", g1, ChannelGroupPatch{Name: ptr("Taken")}); !errors.Is(err, ErrChannelGroupNotFound) {
			t.Fatalf("another member's group = %v", err)
		}
		if err := s.UpdateChannelGroup(ctx, room, "maya", g1, ChannelGroupPatch{}); err != nil {
			t.Fatalf("an empty patch of an existing group = %v", err)
		}
		if got := must(s.ChannelGroups(ctx, room, "maya"))(t); got[0].GroupID != g1 || got[0].Name != "Focus" {
			t.Fatalf("after the patch = %+v", got)
		}

		a, c := uniq("a-"), uniq("c-")
		if err := s.FollowChannel(ctx, room, a, "maya", 10, FollowMarks{Group: ptr(g1)}); err != nil {
			t.Fatal(err)
		}
		if err := s.FollowChannel(ctx, room, c, "maya", 10, FollowMarks{Group: ptr(g1), Starred: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		// A repeat that names no group keeps it.
		if err := s.FollowChannel(ctx, room, a, "maya", 20, FollowMarks{Hidden: ptr(false)}); err != nil {
			t.Fatal(err)
		}
		byID := map[string]ChannelFollow{}
		for _, f := range must(s.ChannelFollows(ctx, room, "maya"))(t) {
			byID[f.ChannelID] = f
		}
		if byID[a].GroupID != g1 || byID[c].GroupID != g1 || !byID[c].Starred {
			t.Fatalf("follows = %+v", byID)
		}
		if err := s.DeleteChannelGroup(ctx, room, "maya", g1); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteChannelGroup(ctx, room, "maya", g1); err != nil {
			t.Fatalf("a second delete = %v", err)
		}
		for _, f := range must(s.ChannelFollows(ctx, room, "maya"))(t) {
			if f.GroupID != "" || (f.ChannelID == c && !f.Starred) {
				t.Fatalf("after the group's delete = %+v", f)
			}
		}
		if got := must(s.ChannelGroups(ctx, room, "maya"))(t); len(got) != 1 || got[0].GroupID != g2 {
			t.Fatalf("groups after delete = %+v", got)
		}
		if got := must(s.ChannelGroups(tenantCtx(uniq("t-grp-b-")), room, "maya"))(t); len(got) != 0 {
			t.Fatalf("tenant B reads groups: %+v", got)
		}
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if got := must(s.ChannelGroups(ctx, room, "maya"))(t); len(got) != 0 {
			t.Fatalf("groups after the room's delete = %+v", got)
		}
	})
}

func TestContractRoomStoreRules(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-rules-"))
		room := newContractRoom(t, ctx, b, "maya")
		if err := b.Room.AddMember(ctx, Membership{RoomID: room, UserID: "jack", Role: RoleMember, JoinedAtMs: 2}); err != nil {
			t.Fatal(err)
		}
		r := must(b.Room.GetRoom(ctx, room))(t)
		if r.Rules != "" || r.RulesVersion != 0 || r.RequireRulesAgreement || r.ChannelCreationPolicy() != wire.ChannelCreationAnyone || len(r.RecommendedChannels) != 0 {
			t.Fatalf("a new room's rules = %+v", r)
		}
		if err := b.Room.UpdateRoom(ctx, room, RoomPatch{Rules: ptr("Be kind."), RequireRulesAgreement: ptr(true),
			RecommendedChannels: ptr([]string{"ch-1", "ch-2"}), ChannelCreation: ptr(wire.ChannelCreationSections)}); err != nil {
			t.Fatal(err)
		}
		r = must(b.Room.GetRoom(ctx, room))(t)
		if r.Rules != "Be kind." || r.RulesVersion != 1 || !r.RequireRulesAgreement || !slices.Equal(r.RecommendedChannels, []string{"ch-1", "ch-2"}) ||
			r.ChannelCreation != wire.ChannelCreationSections {
			t.Fatalf("after patch = %+v", r)
		}
		// The same text keeps the version; a change of case is a change.
		for _, text := range []string{"Be kind.", "be kind."} {
			if err := b.Room.UpdateRoom(ctx, room, RoomPatch{Rules: ptr(text)}); err != nil {
				t.Fatal(err)
			}
		}
		if r = must(b.Room.GetRoom(ctx, room))(t); r.RulesVersion != 2 || r.Rules != "be kind." {
			t.Fatalf("version after same and changed text = %d (%q), want 2", r.RulesVersion, r.Rules)
		}
		jack, _ := must2(b.Room.GetMember(ctx, room, "jack"))(t)
		if !r.RulesAgreementDue(jack) {
			t.Fatal("a member who never agreed is not due")
		}
		if err := b.Room.AgreeRoomRules(ctx, room, "jack", 1); !errors.Is(err, ErrRulesChanged) {
			t.Fatalf("agree to an old version = %v, want ErrRulesChanged", err)
		}
		if err := b.Room.AgreeRoomRules(ctx, room, "jack", 2); err != nil {
			t.Fatal(err)
		}
		if err := b.Room.AgreeRoomRules(ctx, room, "erin", 2); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("a non-member's agreement = %v, want ErrRoomNotFound", err)
		}
		jack, _ = must2(b.Room.GetMember(ctx, room, "jack"))(t)
		if jack.RulesAgreedVersion != 2 || r.RulesAgreementDue(jack) {
			t.Fatalf("after agreeing = %+v", jack)
		}
		// A re-role keeps the agreement; a rules change makes it due again.
		if err := b.Room.AddMember(ctx, Membership{RoomID: room, UserID: "jack", Role: RoleAdmin, JoinedAtMs: 9}); err != nil {
			t.Fatal(err)
		}
		if jack, _ = must2(b.Room.GetMember(ctx, room, "jack"))(t); jack.RulesAgreedVersion != 2 || jack.Role != RoleAdmin {
			t.Fatalf("after a re-role = %+v", jack)
		}
		if err := b.Room.UpdateRoom(ctx, room, RoomPatch{Rules: ptr("Be kinder.")}); err != nil {
			t.Fatal(err)
		}
		if r = must(b.Room.GetRoom(ctx, room))(t); !r.RulesAgreementDue(jack) {
			t.Fatal("a rules change did not ask again")
		}
		if err := b.Room.UpdateRoom(ctx, room, RoomPatch{RecommendedChannels: ptr([]string{})}); err != nil {
			t.Fatal(err)
		}
		if r = must(b.Room.GetRoom(ctx, room))(t); len(r.RecommendedChannels) != 0 {
			t.Fatalf("cleared recommended channels = %v", r.RecommendedChannels)
		}
	})
}
