package store

import (
	"errors"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A members-only section's members and join requests (slice 2, C17): a
// member is added once, a request closes when they join, a removal of a
// non-member says so, and nothing crosses a tenant.
func TestContractSectionMembershipStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-secm-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		sec := uniq("sec-")
		if err := s.PutSectionJoinRequest(ctx, SectionJoinRequest{RoomID: room, SectionID: sec, UserID: "jack", RequestedAtMs: 5}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutSectionJoinRequest(ctx, SectionJoinRequest{RoomID: room, SectionID: sec, UserID: "jack", RequestedAtMs: 9}); err != nil {
			t.Fatalf("a repeated request = %v", err)
		}
		if got := must(s.SectionJoinRequests(ctx, room, sec))(t); len(got) != 1 || got[0].RequestedAtMs != 5 {
			t.Fatalf("requests = %+v (one, its first time)", got)
		}
		for _, m := range []SectionMember{
			{RoomID: room, SectionID: sec, UserID: "maya", AddedBy: "maya", JoinedAtMs: 1},
			{RoomID: room, SectionID: sec, UserID: "jack", AddedBy: "maya", JoinedAtMs: 10},
			{RoomID: room, SectionID: sec, UserID: "jack", AddedBy: "erin", JoinedAtMs: 20},
		} {
			if err := s.AddSectionMember(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		got := must(s.SectionMembers(ctx, room, sec))(t)
		if len(got) != 2 || got[0].UserID != "maya" || got[1].UserID != "jack" || got[1].AddedBy != "maya" || got[1].JoinedAtMs != 10 {
			t.Fatalf("members = %+v (a repeat keeps the first)", got)
		}
		if got := must(s.SectionJoinRequests(ctx, room, sec))(t); len(got) != 0 {
			t.Fatalf("joining closes the request: %+v", got)
		}
		if err := s.TakeSectionJoinRequest(ctx, room, sec, "jack"); !errors.Is(err, ErrNoJoinRequest) {
			t.Fatalf("taking a closed request = %v", err)
		}
		if ids := must(s.MemberSections(ctx, room, "jack"))(t); len(ids) != 1 || ids[0] != sec {
			t.Fatalf("jack's sections = %v", ids)
		}
		if err := s.RemoveSectionMember(ctx, room, sec, "jack"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveSectionMember(ctx, room, sec, "jack"); !errors.Is(err, ErrNotSectionMember) {
			t.Fatalf("removing a non-member = %v", err)
		}
		if ids := must(s.MemberSections(ctx, room, "jack"))(t); len(ids) != 0 {
			t.Fatalf("after removal = %v", ids)
		}
		if err := s.PutSectionJoinRequest(ctx, SectionJoinRequest{RoomID: room, SectionID: sec, UserID: "erin", RequestedAtMs: 30}); err != nil {
			t.Fatal(err)
		}
		if err := s.TakeSectionJoinRequest(ctx, room, sec, "erin"); err != nil {
			t.Fatalf("declining = %v", err)
		}
		other := tenantCtx(uniq("t-secm-b-"))
		if got := must(s.SectionMembers(other, room, sec))(t); len(got) != 0 {
			t.Fatalf("tenant B reads members: %+v", got)
		}
		// The room's delete takes its section rows.
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if got := must(s.SectionMembers(ctx, room, sec))(t); len(got) != 0 {
			t.Fatalf("after the room's delete = %+v", got)
		}
	})
}

// MayReadChannel: an open or top-level channel is anyone's in the room; a
// members-only section's channels are its members' only, the room's admins
// included only once they join (C12); an agent reads as the person it works
// for, and never a channel barring agents.
func TestContractMayReadChannel(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-mrc-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		open, closed := uniq("open-"), uniq("closed-")
		for _, sec := range []RoomSection{
			{SectionID: open, RoomID: room, Name: "Open", Visibility: wire.SectionOpen, JoinPolicy: SectionJoinOpen, CreatedBy: "maya", CreatedAtMs: 1},
			{SectionID: closed, RoomID: room, Name: "Closed", Visibility: wire.SectionMembers, JoinPolicy: SectionJoinInvite, Owners: []string{"jack"}, CreatedBy: "jack", CreatedAtMs: 2},
		} {
			if err := s.PutSection(ctx, sec); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.AddSectionMember(ctx, SectionMember{RoomID: room, SectionID: closed, UserID: "jack", AddedBy: "jack", JoinedAtMs: 2}); err != nil {
			t.Fatal(err)
		}
		top := RoomChannel{ChannelID: "top", SectionID: ""}
		inOpen := RoomChannel{ChannelID: "o", SectionID: open}
		inClosed := RoomChannel{ChannelID: "c", SectionID: closed}
		barred := RoomChannel{ChannelID: "b", SectionID: closed, Settings: wire.ChannelSettings{NoAgents: true}}
		load := func(r Reader) ChannelAccess {
			a, err := LoadChannelAccess(ctx, s, room, r)
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
		jack, erin, admin := load(Reader{User: "jack"}), load(Reader{User: "erin"}), load(Reader{User: "maya", Admin: true})
		for _, c := range []RoomChannel{top, inOpen, inClosed} {
			if !jack.MayReadChannel(c) {
				t.Fatalf("a section member reads %s", c.ChannelID)
			}
		}
		if !erin.MayReadChannel(top) || !erin.MayReadChannel(inOpen) || erin.MayReadChannel(inClosed) {
			t.Fatal("a non-member reads the top level and open sections, never a members-only one")
		}
		closedSec := must(s.ListSections(ctx, room))(t)[1]
		if erin.MayKnowSection(closedSec) || !jack.MayKnowSection(closedSec) || !admin.MayKnowSection(closedSec) {
			t.Fatal("a non-member does not know an invite section; its members and the room's admins do")
		}
		if erin.MayKnowMembers(closedSec) || !admin.MayKnowMembers(closedSec) || !jack.MayKnowMembers(closedSec) {
			t.Fatal("who belongs to it: its members and the room's admins")
		}
		shown := closedSec
		shown.JoinPolicy = SectionJoinAsk
		if !erin.MayKnowSection(shown) || erin.MayKnowMembers(shown) || erin.MayReadChannel(inClosed) {
			t.Fatal("an ask section shows its name for joining, never its members or channels")
		}
		if admin.MayReadChannel(inClosed) {
			t.Fatal("an admin reads a members-only section only by joining it")
		}
		agentForJack, agentForErin := load(Reader{User: "jack", Agent: true}), load(Reader{User: "erin", Agent: true})
		if !agentForJack.MayReadChannel(inClosed) || agentForErin.MayReadChannel(inClosed) {
			t.Fatal("an agent reads as the person it works for")
		}
		if agentForJack.MayReadChannel(barred) || !jack.MayReadChannel(barred) {
			t.Fatal("an agent never reads a channel that bars agents; its member does")
		}
		if load(Reader{Agent: true}).MayReadChannel(inClosed) || load(Reader{Agent: true, Admin: true}).MayKnowSection(closedSec) {
			t.Fatal("an agent for nobody reads and knows no members-only section")
		}
	})
}
