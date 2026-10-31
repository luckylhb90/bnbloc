package store

import (
	"context"
	"errors"
	"testing"
)

// Multi-tenant isolation is the load-bearing invariant of the tenancy retrofit:
// two tenants that happen to reuse the same room/session/grant/device id must
// never read or mutate each other's rows, and a globally-keyed lookup (invite
// code, grant id, device id, client-msg id) presented under the wrong tenant
// must fail closed. A regression here is a silent cross-organization data leak,
// so every assertion runs against both backends (forEachBackend).

const (
	tenantA = "org-a"
	tenantB = "org-b"
)

// isolationTenants returns two tenant contexts and an id prefix unique to
// this run, so the cases can share a MySQL database.
func isolationTenants() (ca, cb context.Context, a, b, id string) {
	sfx := uniq("")
	a, b = tenantA+"-"+sfx, tenantB+"-"+sfx
	return tenantCtx(a), tenantCtx(b), a, b, sfx
}

// plantMember files a membership row directly, bypassing AddMember's guard,
// to model a stray row (as the pre-BE-1 create upsert could leave behind).
func plantMember(t *testing.T, b *Backends, tenantID string, m Membership) {
	t.Helper()
	if b.DB != nil {
		if _, err := b.DB.Exec(`INSERT INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms) VALUES (?, ?, ?, ?, ?)`,
			tenantID, m.RoomID, m.UserID, string(m.Role), m.JoinedAtMs); err != nil {
			t.Fatalf("plant member: %v", err)
		}
		return
	}
	s := b.Room.(*MemoryRoom)
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.addMemberLocked(tenantID, m)
}

// Rooms in two tenants are independent: each tenant sees only its own room,
// membership, and plaza search results. Room ids are globally unique (MySQL's
// rooms primary key is room_id alone), so a second tenant cannot create a room
// with an id another tenant already holds — that create must fail rather than
// adopt the room (BE-1: room takeover via create-upsert).
func TestContractRoomStoreTenantIsolation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		s := bk.Room
		ca, cb, _, _, id := isolationTenants()
		aID, bID := "a-"+id, "b-"+id

		if err := s.CreateRoomWithOwner(ca, Room{RoomID: aID, Name: "A-room", Visibility: VisibilityShared, CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: aID, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("A create: %v", err)
		}
		if err := s.CreateRoomWithOwner(cb, Room{RoomID: bID, Name: "B-room", Visibility: VisibilityShared, CreatedBy: "bob", CreatedAtMs: 1}, Membership{RoomID: bID, UserID: "bob", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("B create: %v", err)
		}

		// Tenant B reusing A's id is refused and leaves A's room untouched.
		err := s.CreateRoomWithOwner(cb, Room{RoomID: aID, Name: "hijack", CreatedBy: "bob", CreatedAtMs: 2}, Membership{RoomID: aID, UserID: "bob", Role: RoleOwner, JoinedAtMs: 2})
		if !errors.Is(err, ErrRoomExists) {
			t.Fatalf("cross-tenant create of an existing id = %v, want ErrRoomExists", err)
		}
		if ra, err := s.GetRoom(ca, aID); err != nil || ra.Name != "A-room" {
			t.Fatalf("A GetRoom = %+v, %v; want A-room unchanged", ra, err)
		}
		if _, err := s.GetRoom(cb, aID); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("B GetRoom(a) = %v, want not found", err)
		}
		if must(s.IsMember(cb, aID, "bob"))(t) || memberAnyTenant(t, s, cb, aID, "bob") {
			t.Fatal("bob must not have gained membership in A's room")
		}

		// Cross-tenant membership must not leak: alice is not a member in tenant B.
		if must(s.IsMember(cb, aID, "alice"))(t) {
			t.Fatal("alice (tenant A) must not be a member in tenant B")
		}
		if !must(s.IsMember(ca, aID, "alice"))(t) {
			t.Fatal("alice must be a member in her own tenant A")
		}

		// Plaza search is tenant-scoped: A sees only A-room.
		rooms, total, err := s.SearchRooms(ca, "", 50, 0)
		if err != nil {
			t.Fatalf("A search: %v", err)
		}
		if total != 1 || len(rooms) != 1 || rooms[0].Name != "A-room" {
			t.Fatalf("A search = %d rows (total %d), want only A-room", len(rooms), total)
		}

		// RoomsForUser is tenant-scoped: bob has a room in B, none in A.
		if got := must(s.RoomsForUser(ca, "bob"))(t); len(got) != 0 {
			t.Fatalf("bob rooms in tenant A = %v, want none", got)
		}
		if got := must(s.RoomsForUser(cb, "bob"))(t); len(got) != 1 {
			t.Fatalf("bob rooms in tenant B = %v, want 1", got)
		}
	})
}

// A membership filed under a tenant other than the room's own (as the
// pre-BE-1 create upsert could leave behind) never grants cross-tenant access,
// and AddMember never files one.
func TestContractRoomStoreForeignTenantMembership(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		s := bk.Room
		ca, cb, _, b, id := isolationTenants()
		r := "r-" + id
		if err := s.CreateRoomWithOwner(ca, Room{RoomID: r, Name: "r", CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: r, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.AddMember(cb, Membership{RoomID: r, UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("add into another tenant's room = %v, want ErrRoomNotFound", err)
		}
		plantMember(t, bk, b, Membership{RoomID: r, UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2})
		if memberAnyTenant(t, s, cb, r, "mallory") {
			t.Fatal("a membership under a foreign tenant must not count")
		}
		if got := must(s.RoomsForUserAllTenants(cb, "mallory"))(t); len(got) != 0 {
			t.Fatalf("mallory all-tenant rooms = %v, want none", got)
		}
		if !memberAnyTenant(t, s, cb, r, "alice") {
			t.Fatal("the real member resolves from any tenant context")
		}
		// A deleted room grants nothing.
		if err := s.DeleteRoom(ca, r); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if memberAnyTenant(t, s, ca, r, "alice") {
			t.Fatal("deleted room must not report members")
		}
		if _, ok := must2(s.RoomTenant(ca, r))(t); ok {
			t.Fatal("deleted room must not resolve a tenant")
		}
		// The id is never reused, even after delete.
		if err := s.CreateRoomWithOwner(cb, Room{RoomID: r, Name: "again", CreatedBy: "mallory", CreatedAtMs: 3}, Membership{RoomID: r, UserID: "mallory", Role: RoleOwner, JoinedAtMs: 3}); !errors.Is(err, ErrRoomExists) {
			t.Fatalf("re-create of a deleted id = %v, want ErrRoomExists", err)
		}
	})
}

// An invite code minted in one tenant cannot be redeemed from another: the
// code is a global primary key, so tenant scoping is the only thing preventing
// a cross-org join.
func TestContractRoomStoreInviteCrossTenant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		s := bk.Room
		ca, cb, _, _, id := isolationTenants()
		r, code := "ar-"+id, "INV-"+id
		if err := s.CreateRoomWithOwner(ca, Room{RoomID: r, Name: "ar", CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: r, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.CreateInvite(ca, Invite{Code: code, RoomID: r, Role: RoleMember, ExpiresAtMs: 1 << 62, Capacity: 5, CreatedBy: "alice"}); err != nil {
			t.Fatalf("invite: %v", err)
		}
		// Redeeming the code from tenant B must fail (not found in B's scope).
		if _, err := s.RedeemInvite(cb, code, "mallory", 2); err == nil {
			t.Fatal("cross-tenant invite redeem must fail, but it succeeded")
		}
		if must(s.IsMember(ca, r, "mallory"))(t) {
			t.Fatal("a refused cross-tenant redeem left a membership")
		}
		// Redeeming in the correct tenant works.
		if _, err := s.RedeemInvite(ca, code, "carol", 2); err != nil {
			t.Fatalf("same-tenant redeem should work: %v", err)
		}
	})
}

// A grant id is a global primary key; a lookup under the wrong tenant must
// return not-found rather than another org's row.
func TestGrantIsolation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		ca, cb, _, _, id := isolationTenants()
		g := bk.Grant
		gid := "G1-" + id
		if err := g.PutGrant(ca, Grant{GrantID: gid, RoomID: "r-" + id, TargetID: "shared-agent:*", OwnerUserID: "alice", GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1}); err != nil {
			t.Fatalf("put grant: %v", err)
		}
		if _, err := g.GetGrant(cb, gid); err == nil {
			t.Fatal("the grant must not be visible to tenant B")
		}
		if _, err := g.GetGrant(ca, gid); err != nil {
			t.Fatalf("the grant must be visible to its own tenant A: %v", err)
		}
	})
}

// The store must fail closed when no tenant is in context — a code path that
// forgets to propagate tenancy must error, never run unscoped.
func TestStoreFailsClosedWithoutTenant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		if err := bk.Room.CreateRoom(bg(), Room{RoomID: uniq("x-"), Name: "x", CreatedBy: "u", CreatedAtMs: 1}); err == nil {
			t.Fatal("CreateRoom without tenant in context must fail closed")
		}
		if err := bk.Room.AddMember(bg(), Membership{RoomID: "x", UserID: "u", Role: RoleMember}); err == nil {
			t.Fatal("AddMember without tenant in context must fail closed")
		}
	})
}

// The bridge discovery shape: a member's bridge must find ALL their rooms
// across tenants (it subscribes/advertises/claims in each), even though the
// session token it authenticates with was minted in just one tenant. The
// tenant-scoped RoomsForUser stays scoped for UI listings.
func TestContractRoomStoreAllTenantsDiscovery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		s := bk.Room
		ca, cb, _, _, id := isolationTenants()
		bob, alice := "bob-"+id, "alice-"+id
		aRoom, bRoom := "a-room-"+id, "b-room-"+id

		// bob belongs to a room in tenant A and another in tenant B.
		if err := s.CreateRoomWithOwner(ca, Room{RoomID: aRoom, Name: "a", CreatedBy: bob, CreatedAtMs: 1}, Membership{RoomID: aRoom, UserID: bob, Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("a create: %v", err)
		}
		if err := s.CreateRoomWithOwner(cb, Room{RoomID: bRoom, Name: "b", CreatedBy: bob, CreatedAtMs: 2}, Membership{RoomID: bRoom, UserID: bob, Role: RoleOwner, JoinedAtMs: 2}); err != nil {
			t.Fatalf("b create: %v", err)
		}

		// Scoped listing (UI): each session tenant sees only its own room.
		if got := must(s.RoomsForUser(ca, bob))(t); len(got) != 1 || got[0] != aRoom {
			t.Fatalf("bob rooms in tenant A = %v, want [%s]", got, aRoom)
		}
		if got := must(s.RoomsForUser(cb, bob))(t); len(got) != 1 || got[0] != bRoom {
			t.Fatalf("bob rooms in tenant B = %v, want [%s]", got, bRoom)
		}

		// Bridge discovery: minted-in-A token still finds the B room — the bridge
		// must subscribe to both or b-room's agents show offline for everyone.
		seen := map[string]bool{}
		for _, r := range must(s.RoomsForUserAllTenants(ca, bob))(t) {
			seen[r] = true
		}
		if len(seen) != 2 || !seen[aRoom] || !seen[bRoom] {
			t.Fatalf("all-tenant discovery = %v, want both rooms", seen)
		}

		// Another user's rooms are never included.
		if got := must(s.RoomsForUserAllTenants(ca, alice))(t); len(got) != 0 {
			t.Fatalf("alice all-tenant rooms = %v, want none", got)
		}
	})
}

// The UI "my rooms" listing is tenant-scoped: switching the acting tenant
// switches the visible workspace. The cross-tenant shape exists ONLY for the
// bridge's room discovery (allTenants), which no UI call ever uses.
func TestContractRoomStoreDetailedTenantScoped(t *testing.T) {
	forEachBackend(t, func(t *testing.T, bk *Backends) {
		s := bk.Room
		ca, cb, _, _, id := isolationTenants()
		u, da, db := "u1-"+id, "da-"+id, "db-"+id
		if err := s.CreateRoomWithOwner(ca, Room{RoomID: da, Name: "da", CreatedBy: u, CreatedAtMs: 1}, Membership{RoomID: da, UserID: u, Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("a: %v", err)
		}
		if err := s.CreateRoomWithOwner(cb, Room{RoomID: db, Name: "db", CreatedBy: u, CreatedAtMs: 2}, Membership{RoomID: db, UserID: u, Role: RoleOwner, JoinedAtMs: 2}); err != nil {
			t.Fatalf("b: %v", err)
		}
		for _, tc := range []struct {
			ctx  context.Context
			want string
		}{{ca, da}, {cb, db}} {
			got := must(s.RoomsForUserDetailed(tc.ctx, u))(t)
			if len(got) != 1 || got[0].RoomID != tc.want {
				t.Fatalf("detailed rooms = %+v, want only %s", got, tc.want)
			}
		}
	})
}

// memberAnyTenant reports whether MembershipAnyTenant finds a membership,
// and checks that MembershipsAnyTenant, asked with the room among others,
// finds the same one.
func memberAnyTenant(t testing.TB, s RoomStore, ctx context.Context, roomID, userID string) bool {
	t.Helper()
	m, ok := must2(s.MembershipAnyTenant(ctx, roomID, userID))(t)
	many := must(s.MembershipsAnyTenant(ctx, userID, []string{uniq("room-none-"), roomID}))(t)
	if bm, bok := many[roomID]; bok != ok || bm != m || len(many) > 1 {
		t.Fatalf("MembershipsAnyTenant(%s, %s) = %+v, MembershipAnyTenant = %+v %v", roomID, userID, many, m, ok)
	}
	return ok
}
