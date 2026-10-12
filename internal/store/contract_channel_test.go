package store

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
)

// ChannelStore contract: channels are tenant-scoped, ids are global and
// written insert-only, deletes are soft and keep the first time, and the
// people chat is created once per room.

func channelIDs(list []RoomChannel) []string {
	out := []string{}
	for _, c := range list {
		out = append(out, c.ChannelID)
	}
	return out
}

func TestContractChannelStoreCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-ch-")
		ctx := tenantCtx(tid)
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		if got := must(s.ListChannels(ctx, room))(t); got == nil || len(got) != 0 {
			t.Fatalf("channels of a room without any = %#v, want []", got)
		}
		if _, ok := must2(s.DefaultChannel(ctx, room))(t); ok {
			t.Fatal("a default channel before any was put")
		}
		def, fe, ops, late := "d-"+uniq(""), "b-"+uniq(""), "a-"+uniq(""), "c-"+uniq("")
		for _, c := range []RoomChannel{
			{ChannelID: late, RoomID: room, Name: "late", CreatedBy: "jack", CreatedAtMs: 9},
			{ChannelID: fe, RoomID: room, Name: "frontend", CreatedBy: "jack", CreatedAtMs: 2},
			{ChannelID: def, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 5},
			{ChannelID: ops, RoomID: room, Name: "ops", CreatedBy: "jack", CreatedAtMs: 2},
		} {
			if err := s.PutChannel(ctx, c); err != nil {
				t.Fatalf("put %s: %v", c.Name, err)
			}
		}
		// Default first, then oldest, then id.
		if got := channelIDs(must(s.ListChannels(ctx, room))(t)); !slices.Equal(got, []string{def, ops, fe, late}) {
			t.Fatalf("ListChannels = %v, want [%s %s %s %s]", got, def, ops, fe, late)
		}
		if d, ok := must2(s.DefaultChannel(ctx, room))(t); !ok || d.ChannelID != def || d.TenantID != tid || d.Name != "general" {
			t.Fatalf("DefaultChannel = %+v %v", d, ok)
		}
		if got, ok := must2(s.GetChannel(ctx, room, fe))(t); !ok || got.Name != "frontend" || got.CreatedBy != "jack" || got.CreatedAtMs != 2 || got.DeletedAtMs != nil {
			t.Fatalf("GetChannel = %+v %v", got, ok)
		}
		if _, ok := must2(s.GetChannel(ctx, room, uniq("nope-")))(t); ok {
			t.Fatal("GetChannel of an unknown id found one")
		}
		if err := s.PutChannel(ctx, RoomChannel{ChannelID: fe, RoomID: room, Name: "again", CreatedAtMs: 3}); !errors.Is(err, ErrChannelExists) {
			t.Fatalf("re-put of an id = %v, want ErrChannelExists", err)
		}

		// Rename: the same name twice succeeds; a miss is ErrChannelNotFound.
		for i := 0; i < 2; i++ {
			if err := s.RenameChannel(ctx, room, fe, "web"); err != nil {
				t.Fatalf("rename #%d: %v", i, err)
			}
		}
		if got, _ := must2(s.GetChannel(ctx, room, fe))(t); got.Name != "web" {
			t.Fatalf("renamed channel = %+v", got)
		}
		if err := s.RenameChannel(ctx, room, uniq("nope-"), "x"); !errors.Is(err, ErrChannelNotFound) {
			t.Fatalf("rename of an unknown channel = %v", err)
		}

		// A soft delete leaves every live view; a rename never resurrects it.
		if err := s.DeleteChannel(ctx, room, fe, 10); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteChannel(ctx, room, fe, 20); err != nil {
			t.Fatalf("second delete = %v", err)
		}
		if err := s.DeleteChannel(ctx, room, uniq("nope-"), 10); err != nil {
			t.Fatalf("delete of an unknown channel = %v", err)
		}
		if _, ok := must2(s.GetChannel(ctx, room, fe))(t); ok {
			t.Fatal("a deleted channel is still readable")
		}
		if got := channelIDs(must(s.ListChannels(ctx, room))(t)); !slices.Equal(got, []string{def, ops, late}) {
			t.Fatalf("ListChannels after delete = %v", got)
		}
		if err := s.RenameChannel(ctx, room, fe, "back"); !errors.Is(err, ErrChannelNotFound) {
			t.Fatalf("rename of a deleted channel = %v, want ErrChannelNotFound", err)
		}
		if _, ok := must2(s.GetChannel(ctx, room, fe))(t); ok {
			t.Fatal("a failed rename resurrected the channel")
		}
		if kind, found, err := s.ChannelKind(ctx, room, fe); err != nil || !found || kind != ChannelKindStandard {
			t.Fatalf("ChannelKind of a deleted channel = %q %v %v; want its kind", kind, found, err)
		}
		if _, found, err := s.ChannelKind(ctx, room, uniq("nope-")); err != nil || found {
			t.Fatalf("ChannelKind of an unknown channel = %v %v", found, err)
		}
		if _, ok := must2(s.DefaultChannel(ctx, room))(t); !ok {
			t.Fatal("the default channel must survive a sibling's deletion")
		}
	})
}

// Channels are scoped by tenant: another tenant neither reads nor changes
// them, and cannot take a channel id another tenant holds.
func TestContractChannelStoreTenantIsolation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ca, cb := tenantCtx(uniq("t-ch-a-")), tenantCtx(uniq("t-ch-b-"))
		s := b.Channel
		room := newContractRoom(t, ca, b, "maya")
		id := uniq("ch-")
		if err := s.PutChannel(ca, RoomChannel{ChannelID: id, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got := must(s.ListChannels(cb, room))(t); len(got) != 0 {
			t.Fatalf("tenant B lists %+v", got)
		}
		if _, ok := must2(s.GetChannel(cb, room, id))(t); ok {
			t.Fatal("tenant B reads tenant A's channel")
		}
		if _, ok := must2(s.DefaultChannel(cb, room))(t); ok {
			t.Fatal("tenant B sees tenant A's default channel")
		}
		if _, found, _ := s.ChannelKind(cb, room, id); found {
			t.Fatal("tenant B reads tenant A's channel kind")
		}
		if err := s.DeleteChannel(cb, room, id, 9); err != nil {
			t.Fatal(err)
		}
		if err := s.RenameChannel(cb, room, id, "hijack"); !errors.Is(err, ErrChannelNotFound) {
			t.Fatalf("tenant B rename = %v, want ErrChannelNotFound", err)
		}
		if got, ok := must2(s.GetChannel(ca, room, id))(t); !ok || got.Name != "general" {
			t.Fatalf("tenant A's channel after tenant B's writes = %+v %v", got, ok)
		}
		if err := s.PutChannel(cb, RoomChannel{ChannelID: id, RoomID: room, Name: "hijack", CreatedAtMs: 2}); !errors.Is(err, ErrChannelExists) {
			t.Fatalf("tenant B put of tenant A's id = %v, want ErrChannelExists", err)
		}
	})
}

// A room created with its default channel lists it through the channel
// store at once.
func TestContractChannelStoreRoomCreateSeedsDefault(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-ch-seed-"))
		room, ch := uniq("room-"), uniq("ch-")
		if err := b.Room.CreateRoomWithOwnerAndChannel(ctx, Room{RoomID: room, Name: "r", CreatedBy: "maya", CreatedAtMs: 1},
			Membership{RoomID: room, UserID: "maya", Role: RoleOwner, JoinedAtMs: 1},
			RoomChannel{ChannelID: ch, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, ok := must2(b.Channel.DefaultChannel(ctx, room))(t); !ok || got.ChannelID != ch || !got.IsDefault {
			t.Fatalf("default channel = %+v %v", got, ok)
		}
		// A failed create (taken room id) writes no channel either.
		ch2 := uniq("ch-")
		if err := b.Room.CreateRoomWithOwnerAndChannel(ctx, Room{RoomID: room, Name: "r"}, Membership{RoomID: room, UserID: "jack", Role: RoleOwner},
			RoomChannel{ChannelID: ch2, RoomID: room, Name: "other", IsDefault: true}); !errors.Is(err, ErrRoomExists) {
			t.Fatalf("re-create = %v", err)
		}
		if _, ok := must2(b.Channel.GetChannel(ctx, room, ch2))(t); ok {
			t.Fatal("a failed room create left its channel")
		}
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if got := must(b.Channel.ListChannels(ctx, room))(t); len(got) != 0 {
			t.Fatalf("channels after the room's delete = %+v", got)
		}
	})
}

// The people chat: lazy creation is idempotent and race-free (one channel
// per room), it lists beside ordinary channels with its kind, and a
// deleted one keeps its first deletion time.
func TestContractChannelStorePeopleChannel(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-pc-"))
		s := b.Channel
		room := newContractRoom(t, ctx, b, "maya")
		def := uniq("ch-")
		if err := s.PutChannel(ctx, RoomChannel{ChannelID: def, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		got := make([]RoomChannel, 8)
		errs := make([]error, 8)
		for i := range got {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				got[i], errs[i] = s.EnsurePeopleChannel(ctx, room, "user"+strconv.Itoa(i), int64(10+i))
			}(i)
		}
		wg.Wait()
		for i := range got {
			if errs[i] != nil {
				t.Fatalf("ensure %d: %v", i, errs[i])
			}
			if got[i].ChannelID != got[0].ChannelID || !got[i].IsPeople() || got[i].IsDefault || got[i].Name != PeopleChannelName {
				t.Fatalf("ensure %d = %+v, want the one people channel %s", i, got[i], got[0].ChannelID)
			}
		}
		people := got[0].ChannelID
		if ids := channelIDs(must(s.ListChannels(ctx, room))(t)); !slices.Equal(ids, []string{def, people}) {
			t.Fatalf("ListChannels = %v, want the default then the people chat", ids)
		}
		if kind, found, err := s.ChannelKind(ctx, room, people); err != nil || !found || kind != ChannelKindPeople {
			t.Fatalf("kind(people) = %q,%v,%v", kind, found, err)
		}
		// Another room, and the same room id in another tenant, get their own.
		other, err := s.EnsurePeopleChannel(ctx, uniq("room-"), "maya", 1)
		if err != nil || other.ChannelID == people {
			t.Fatalf("other room people channel = %+v, %v", other, err)
		}
		foreign, err := s.EnsurePeopleChannel(tenantCtx(uniq("t-pc-other-")), room, "maya", 1)
		if err != nil || foreign.ChannelID == people {
			t.Fatalf("foreign tenant people channel = %+v, %v", foreign, err)
		}
		// A deleted people chat stays the room's one, deleted at the first time.
		if err := s.DeleteChannel(ctx, room, people, 50); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteChannel(ctx, room, people, 60); err != nil {
			t.Fatal(err)
		}
		again, err := s.EnsurePeopleChannel(ctx, room, "jack", 70)
		if err != nil || again.ChannelID != people || again.DeletedAtMs == nil || *again.DeletedAtMs != 50 {
			t.Fatalf("people chat after deletes = %+v, %v; want deleted at 50", again, err)
		}
	})
}
