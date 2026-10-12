package store

import (
	"errors"
	"slices"
	"strconv"
	"testing"
)

// RoomStore contract: rooms, memberships, invites and the plaza search
// behave the same on both backends.

// createRoomAt creates a live room owned by owner at createdAt.
func createRoomAt(t *testing.T, b *Backends, tid, id, name, desc, owner string, createdAt int64) {
	t.Helper()
	if err := b.Room.CreateRoomWithOwner(tenantCtx(tid), Room{RoomID: id, Name: name, Description: desc, CreatedBy: owner, CreatedAtMs: createdAt},
		Membership{RoomID: id, UserID: owner, Role: RoleOwner, JoinedAtMs: createdAt}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func TestContractRoomStoreCreateGetUpdate(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid, other := uniq("t-room-"), uniq("t-room-other-")
		ctx, octx := tenantCtx(tid), tenantCtx(other)
		s := b.Room
		id := uniq("room-")
		if err := s.CreateRoom(ctx, Room{RoomID: id, Name: "n", CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRoom(ctx, id)
		if err != nil || got.Visibility != VisibilityPrivate || got.SyncMode != SyncModeGit || got.TenantID != tid || got.Name != "n" {
			t.Fatalf("GetRoom = %+v, %v; want private, git, the ctx tenant", got, err)
		}
		if tenantOf, ok := must2(s.RoomTenant(octx, id))(t); !ok || tenantOf != tid {
			t.Fatalf("RoomTenant = %q %v", tenantOf, ok)
		}
		if _, err := s.GetRoom(octx, id); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("GetRoom from another tenant = %v, want ErrRoomNotFound", err)
		}
		if _, err := s.GetRoom(ctx, uniq("nope-")); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("GetRoom of a missing room = %v", err)
		}
		// An id is global: a create never adopts it, in this tenant or another.
		for _, c := range []struct {
			name string
			err  error
		}{
			{"same tenant", s.CreateRoom(ctx, Room{RoomID: id, Name: "x"})},
			{"other tenant", s.CreateRoom(octx, Room{RoomID: id, Name: "x"})},
			{"with owner", s.CreateRoomWithOwner(octx, Room{RoomID: id, Name: "x"}, Membership{RoomID: id, UserID: "jack", Role: RoleOwner})},
		} {
			if !errors.Is(c.err, ErrRoomExists) {
				t.Fatalf("%s re-create = %v, want ErrRoomExists", c.name, c.err)
			}
		}
		if must(s.IsMember(octx, id, "jack"))(t) || memberAnyTenant(t, s, octx, id, "jack") {
			t.Fatal("a refused create left a membership")
		}
		if got, _ := s.GetRoom(ctx, id); got.Name != "n" {
			t.Fatalf("a refused create renamed the room to %q", got.Name)
		}

		// A patch changes only the fields it sets.
		shared, mutagen, desc := VisibilityShared, SyncModeMutagen, "d"
		if err := s.UpdateRoom(ctx, id, RoomPatch{Visibility: &shared, SyncMode: &mutagen, Description: &desc}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetRoom(ctx, id); got.Visibility != shared || got.SyncMode != mutagen || got.Description != "d" || got.Name != "n" {
			t.Fatalf("patched room = %+v", got)
		}
		// An empty patch is a no-op on a live room and a miss elsewhere.
		if err := s.UpdateRoom(ctx, id, RoomPatch{}); err != nil {
			t.Fatalf("empty patch = %v", err)
		}
		for _, c := range []struct {
			name string
			err  error
		}{
			{"missing room", s.UpdateRoom(ctx, uniq("nope-"), RoomPatch{})},
			{"other tenant", s.UpdateRoom(octx, id, RoomPatch{Description: &desc})},
		} {
			if !errors.Is(c.err, ErrRoomNotFound) {
				t.Fatalf("UpdateRoom on a %s = %v, want ErrRoomNotFound", c.name, c.err)
			}
		}

		// Delete hides the room and its memberships; the id stays taken.
		gone := uniq("gone-")
		createRoomAt(t, b, tid, gone, "gone", "", "maya", 2)
		if err := s.DeleteRoom(octx, gone); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("delete from another tenant = %v", err)
		}
		if err := s.DeleteRoom(ctx, gone); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetRoom(ctx, gone); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("GetRoom after delete = %v", err)
		}
		if must(s.IsMember(ctx, gone, "maya"))(t) || must(s.MemberCount(ctx, gone))(t) != 0 {
			t.Fatal("delete left memberships")
		}
		if rooms, total, err := s.SearchRooms(ctx, "gone", 0, 0); err != nil || total != 0 || len(rooms) != 0 {
			t.Fatalf("plaza after delete = %+v (total %d), %v; want nothing", rooms, total, err)
		}
		if err := s.UpdateRoom(ctx, gone, RoomPatch{Description: &desc}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("UpdateRoom after delete = %v", err)
		}
		if err := s.DeleteRoom(ctx, gone); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("second delete = %v", err)
		}
		if err := s.CreateRoom(ctx, Room{RoomID: gone, Name: "again"}); !errors.Is(err, ErrRoomExists) {
			t.Fatalf("re-create of a deleted id = %v", err)
		}
	})
}

func TestContractRoomStoreMembers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-mem-")
		ctx := tenantCtx(tid)
		s := b.Room
		a, z := "a-"+uniq(""), "z-"+uniq("")
		createRoomAt(t, b, tid, z, "z", "", "maya", 5)
		createRoomAt(t, b, tid, a, "a", "", "maya", 6)

		// Empty answers are empty lists (they serialize as []).
		for name, got := range map[string][]string{
			"RoomsForUser":           must(s.RoomsForUser(ctx, "nobody"))(t),
			"RoomsForUserAllTenants": must(s.RoomsForUserAllTenants(ctx, "nobody"))(t),
		} {
			if got == nil || len(got) != 0 {
				t.Fatalf("%s of nobody = %#v, want []", name, got)
			}
		}
		if got := must(s.Members(ctx, uniq("nope-")))(t); got == nil || len(got) != 0 {
			t.Fatalf("Members of a missing room = %#v, want []", got)
		}
		if got := must(s.RoomsForUserDetailed(ctx, "nobody"))(t); got == nil || len(got) != 0 {
			t.Fatalf("RoomsForUserDetailed of nobody = %#v, want []", got)
		}

		// Room lists are ordered by room id; the detailed list newest first.
		if got := must(s.RoomsForUser(ctx, "maya"))(t); !slices.Equal(got, []string{a, z}) {
			t.Fatalf("RoomsForUser = %v, want [%s %s]", got, a, z)
		}
		if got := must(s.RoomsForUserAllTenants(ctx, "maya"))(t); !slices.Equal(got, []string{a, z}) {
			t.Fatalf("RoomsForUserAllTenants = %v", got)
		}
		if got := must(s.RoomsForUserDetailed(ctx, "maya"))(t); len(got) != 2 || got[0].RoomID != a || !got[0].IsOwner || got[0].Role != RoleOwner {
			t.Fatalf("RoomsForUserDetailed = %+v", got)
		}

		// A re-add changes only the role; Members is in join order, then id.
		for _, m := range []Membership{
			{RoomID: z, UserID: "jack", Role: RoleMember, JoinedAtMs: 7},
			{RoomID: z, UserID: "bea", Role: RoleMember, JoinedAtMs: 7},
			{RoomID: z, UserID: "jack", Role: RoleVisitor, JoinedAtMs: 99},
		} {
			if err := s.AddMember(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		var order []string
		for _, m := range must(s.Members(ctx, z))(t) {
			order = append(order, m.UserID+"/"+string(m.Role)+"/"+strconv.FormatInt(m.JoinedAtMs, 10))
		}
		if want := []string{"maya/owner/5", "bea/member/7", "jack/visitor/7"}; !slices.Equal(order, want) {
			t.Fatalf("Members = %v, want %v", order, want)
		}
		if role, ok := must2(s.MemberRole(ctx, z, "jack"))(t); !ok || role != RoleVisitor {
			t.Fatalf("MemberRole = %q %v", role, ok)
		}
		if n := must(s.MemberCount(ctx, z))(t); n != 3 {
			t.Fatalf("MemberCount = %d", n)
		}
		if err := s.AddMember(ctx, Membership{RoomID: uniq("nope-"), UserID: "jack", Role: RoleMember}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("AddMember to a missing room = %v", err)
		}
		if err := s.RemoveMember(ctx, z, "bea"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(ctx, z, "bea"); err != nil {
			t.Fatalf("removing an absent member = %v", err)
		}
		if must(s.IsMember(ctx, z, "bea"))(t) {
			t.Fatal("bea still a member")
		}

		// Ownership moves to a human member only.
		if err := s.TransferOwner(ctx, z, "stranger"); !errors.Is(err, ErrNewOwnerNotMember) {
			t.Fatalf("transfer to a non-member = %v, want ErrNewOwnerNotMember", err)
		}
		if role, _ := must2(s.MemberRole(ctx, z, "maya"))(t); role != RoleOwner {
			t.Fatalf("owner after a refused transfer = %q", role)
		}
		if err := s.TransferOwner(ctx, uniq("nope-"), "jack"); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("transfer in a missing room = %v, want ErrRoomNotFound", err)
		}
		if err := s.TransferOwner(ctx, z, "jack"); err != nil {
			t.Fatal(err)
		}
		if role, _ := must2(s.MemberRole(ctx, z, "jack"))(t); role != RoleOwner {
			t.Fatalf("new owner role = %q", role)
		}
		if role, _ := must2(s.MemberRole(ctx, z, "maya"))(t); role != RoleMember {
			t.Fatalf("prior owner role = %q, want member", role)
		}
		if got := must(s.RoomsForUserDetailed(ctx, "maya"))(t); len(got) != 2 || got[1].RoomID != z || got[1].IsOwner || got[1].Role != RoleMember || got[1].MemberCount != 2 {
			t.Fatalf("detailed row of a joined room = %+v", got)
		}
	})
}

// The plaza search: a literal, case-insensitive substring of the name or
// description (% and _ match only themselves), newest first then room id,
// paged with the shared page-size rule.
func TestContractRoomStoreSearch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-plaza-")
		ctx := tenantCtx(tid)
		s := b.Room
		pct, und, plain, tieA, tieB := "p-"+uniq(""), "u-"+uniq(""), "x-"+uniq(""), "ta-"+uniq(""), "tb-"+uniq("")
		createRoomAt(t, b, tid, pct, "Rate 100% Off", "", "maya", 50)
		createRoomAt(t, b, tid, und, "Design", "snake_case notes", "maya", 40)
		createRoomAt(t, b, tid, plain, "Rate 100 Of", "snakeXcase", "maya", 30)
		createRoomAt(t, b, tid, tieB, "tie", "", "maya", 10)
		createRoomAt(t, b, tid, tieA, "tie", "", "maya", 10)

		ids := func(q string, limit, offset int) ([]string, int) {
			t.Helper()
			rooms, total, err := s.SearchRooms(ctx, q, limit, offset)
			if err != nil {
				t.Fatal(err)
			}
			out := []string{}
			for _, r := range rooms {
				out = append(out, r.RoomID)
			}
			return out, total
		}
		for _, c := range []struct {
			q    string
			want []string
		}{
			{"100%", []string{pct}},
			{"%", []string{pct}},
			{"e_c", []string{und}},
			{"_", []string{und}},
			{"DESIGN", []string{und}},
			{"rate 100", []string{pct, plain}},
			{"", []string{pct, und, plain, tieA, tieB}},
		} {
			if got, total := ids(c.q, 0, 0); !slices.Equal(got, c.want) || total != len(c.want) {
				t.Fatalf("SearchRooms(%q) = %v (total %d), want %v", c.q, got, total, c.want)
			}
		}
		if got, total := ids("", 2, 1); !slices.Equal(got, []string{und, plain}) || total != 5 {
			t.Fatalf("page (2, 1) = %v total %d", got, total)
		}
		if got, _ := ids("", 2, -5); !slices.Equal(got, []string{pct, und}) {
			t.Fatalf("negative offset = %v, want the first page", got)
		}
		if got, total := ids("", 10, 99); len(got) != 0 || total != 5 {
			t.Fatalf("offset past the end = %v total %d", got, total)
		}

		// The page size: unset means DefaultSearchRoomsPage, never more than
		// MaxSearchRoomsPage.
		bulk := uniq("t-plaza-bulk-")
		for i := 0; i < MaxSearchRoomsPage+3; i++ {
			createRoomAt(t, b, bulk, uniq("bulk-"), "bulk", "", "maya", int64(i))
		}
		for _, c := range []struct{ limit, want int }{{0, DefaultSearchRoomsPage}, {-1, DefaultSearchRoomsPage}, {7, 7}, {MaxSearchRoomsPage * 5, MaxSearchRoomsPage}} {
			rooms, total, err := s.SearchRooms(tenantCtx(bulk), "", c.limit, 0)
			if err != nil || len(rooms) != c.want || total != MaxSearchRoomsPage+3 {
				t.Fatalf("SearchRooms(limit %d) = %d rooms (total %d), %v; want %d", c.limit, len(rooms), total, err, c.want)
			}
		}
	})
}

// An invite is live strictly before its expiry, has a capacity, and a
// redeem by an existing member is idempotent.
func TestContractRoomStoreInvites(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-inv-")
		ctx := tenantCtx(tid)
		s := b.Room
		room := uniq("room-")
		createRoomAt(t, b, tid, room, "r", "", "maya", 1)
		code := uniq("INV-")
		if err := s.CreateInvite(ctx, Invite{Code: code, RoomID: room, Role: RoleMember, ExpiresAtMs: 100, Capacity: 1, CreatedBy: "maya"}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateInvite(tenantCtx(uniq("t-inv-other-")), Invite{Code: code, RoomID: room, Role: RoleMember, ExpiresAtMs: 100, Capacity: 9, CreatedBy: "jack"}); !errors.Is(err, ErrInviteExists) {
			t.Fatalf("re-used code = %v, want ErrInviteExists", err)
		}
		if _, err := s.RedeemInvite(ctx, uniq("nope-"), "jack", 1); !errors.Is(err, ErrInviteNotFound) {
			t.Fatalf("unknown code = %v", err)
		}
		if _, err := s.RedeemInvite(ctx, code, "jack", 100); !errors.Is(err, ErrInviteExpired) {
			t.Fatalf("redeem at now == expires = %v, want ErrInviteExpired", err)
		}
		m, err := s.RedeemInvite(ctx, code, "jack", 99)
		if err != nil || m.Role != RoleMember || m.JoinedAtMs != 99 || m.TenantID != tid {
			t.Fatalf("redeem before expiry = %+v, %v", m, err)
		}
		if _, err := s.RedeemInvite(ctx, code, "bea", 99); !errors.Is(err, ErrInviteFull) {
			t.Fatalf("redeem past capacity = %v, want ErrInviteFull", err)
		}
		// An existing member gets their membership back, even after expiry.
		again, err := s.RedeemInvite(ctx, code, "jack", 500)
		if err != nil || again != m {
			t.Fatalf("idempotent redeem = %+v, %v; want %+v", again, err, m)
		}
		owner, err := s.RedeemInvite(ctx, code, "maya", 500)
		if err != nil || owner.Role != RoleOwner || owner.JoinedAtMs != 1 {
			t.Fatalf("redeem by the owner = %+v, %v", owner, err)
		}
		// A deleted room's invite cannot be redeemed.
		if err := s.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RedeemInvite(ctx, code, "bea", 1); !errors.Is(err, ErrInviteNotFound) {
			t.Fatalf("redeem after the room's delete = %v, want ErrInviteNotFound (the delete removes its invites)", err)
		}
	})
}
