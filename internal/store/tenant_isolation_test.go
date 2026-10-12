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
// so these assertions run against the in-memory stores (same contract the MySQL
// impls satisfy).

const (
	tenantA = "org-a"
	tenantB = "org-b"
)

// Rooms in two tenants are independent: each tenant sees only its own room,
// membership, and plaza search results. Room ids are globally unique (MySQL's
// rooms primary key is room_id alone), so a second tenant cannot create a room
// with an id another tenant already holds — that create must fail rather than
// adopt the room (BE-1: room takeover via create-upsert).
func TestRoomIsolationAcrossTenants(t *testing.T) {
	s := NewMemoryRoom()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)

	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "a-id", Name: "A-room", Visibility: VisibilityShared, CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: "a-id", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("A create: %v", err)
	}
	if err := s.CreateRoomWithOwner(cb, Room{RoomID: "b-id", Name: "B-room", Visibility: VisibilityShared, CreatedBy: "bob", CreatedAtMs: 1}, Membership{RoomID: "b-id", UserID: "bob", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("B create: %v", err)
	}

	// Tenant B reusing A's id is refused and leaves A's room untouched.
	err := s.CreateRoomWithOwner(cb, Room{RoomID: "a-id", Name: "hijack", CreatedBy: "bob", CreatedAtMs: 2}, Membership{RoomID: "a-id", UserID: "bob", Role: RoleOwner, JoinedAtMs: 2})
	if !errors.Is(err, ErrRoomExists) {
		t.Fatalf("cross-tenant create of an existing id = %v, want ErrRoomExists", err)
	}
	if ra, err := s.GetRoom(ca, "a-id"); err != nil || ra.Name != "A-room" {
		t.Fatalf("A GetRoom = %+v, %v; want A-room unchanged", ra, err)
	}
	if _, err := s.GetRoom(cb, "a-id"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("B GetRoom(a-id) = %v, want not found", err)
	}
	if s.IsMember(cb, "a-id", "bob") || memberAnyTenant(s, cb, "a-id", "bob") {
		t.Fatal("bob must not have gained membership in A's room")
	}

	// Cross-tenant membership must not leak: alice is not a member in tenant B.
	if s.IsMember(cb, "a-id", "alice") {
		t.Fatal("alice (tenant A) must not be a member in tenant B")
	}
	if !s.IsMember(ca, "a-id", "alice") {
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
	if got := s.RoomsForUser(ca, "bob"); len(got) != 0 {
		t.Fatalf("bob rooms in tenant A = %v, want none", got)
	}
	if got := s.RoomsForUser(cb, "bob"); len(got) != 1 {
		t.Fatalf("bob rooms in tenant B = %v, want 1", got)
	}
}

// A membership filed under a tenant other than the room's own (as the
// pre-BE-1 create upsert could leave behind) never grants cross-tenant access.
func TestIsMemberAnyTenantIgnoresForeignTenantMembership(t *testing.T) {
	s := NewMemoryRoom()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)
	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "r", Name: "r", CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: "r", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// AddMember never files a row under a tenant that does not hold the room.
	if err := s.AddMember(cb, Membership{RoomID: "r", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("add into another tenant's room = %v, want ErrRoomNotFound", err)
	}
	// A stray row under tenant B for A's room, planted directly.
	s.mu.Lock()
	_ = s.addMemberLocked(tenantB, Membership{RoomID: "r", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2})
	s.mu.Unlock()
	if memberAnyTenant(s, cb, "r", "mallory") {
		t.Fatal("a membership under a foreign tenant must not count")
	}
	if got := s.RoomsForUserAllTenants(cb, "mallory"); len(got) != 0 {
		t.Fatalf("mallory all-tenant rooms = %v, want none", got)
	}
	if !memberAnyTenant(s, cb, "r", "alice") {
		t.Fatal("the real member resolves from any tenant context")
	}
	// A deleted room grants nothing.
	if err := s.DeleteRoom(ca, "r"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if memberAnyTenant(s, ca, "r", "alice") {
		t.Fatal("deleted room must not report members")
	}
	if _, ok := s.RoomTenant(ca, "r"); ok {
		t.Fatal("deleted room must not resolve a tenant")
	}
	// The id is never reused, even after delete.
	if err := s.CreateRoomWithOwner(cb, Room{RoomID: "r", Name: "again", CreatedBy: "mallory", CreatedAtMs: 3}, Membership{RoomID: "r", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 3}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("re-create of a deleted id = %v, want ErrRoomExists", err)
	}
}

// An invite code minted in one tenant cannot be redeemed from another: the
// code is a global primary key, so tenant scoping is the only thing preventing
// a cross-org join.
func TestInviteRedeemRejectsCrossTenant(t *testing.T) {
	s := NewMemoryRoom()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)
	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "ar", Name: "ar", CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: "ar", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateInvite(ca, Invite{Code: "INV1", RoomID: "ar", Role: RoleMember, ExpiresAtMs: 1 << 62, Capacity: 5, CreatedBy: "alice"}); err != nil {
		t.Fatalf("invite: %v", err)
	}
	// Redeeming INV1 from tenant B must fail (code not found in B's scope).
	if _, err := s.RedeemInvite(cb, "INV1", "mallory", 2); err == nil {
		t.Fatal("cross-tenant invite redeem must fail, but it succeeded")
	}
	// Redeeming in the correct tenant works.
	if _, err := s.RedeemInvite(ca, "INV1", "carol", 2); err != nil {
		t.Fatalf("same-tenant redeem should work: %v", err)
	}
}

// A grant id is a global primary key; a lookup under the wrong tenant must
// return not-found rather than another org's row.
func TestGrantIsolation(t *testing.T) {
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)

	g := NewMemoryGrant()
	if err := g.PutGrant(ca, Grant{GrantID: "G1", RoomID: "r", TargetID: "shared-agent:*", OwnerUserID: "alice", GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1}); err != nil {
		t.Fatalf("put grant: %v", err)
	}
	if _, err := g.GetGrant(cb, "G1"); err == nil {
		t.Fatal("grant G1 must not be visible to tenant B")
	}
	if _, err := g.GetGrant(ca, "G1"); err != nil {
		t.Fatalf("grant G1 must be visible to its own tenant A: %v", err)
	}
}

// The store must fail closed when no tenant is in context — a code path that
// forgets to propagate tenancy must error, never run unscoped.
func TestStoreFailsClosedWithoutTenant(t *testing.T) {
	s := NewMemoryRoom()
	if err := s.CreateRoom(bg(), Room{RoomID: "x", Name: "x", CreatedBy: "u", CreatedAtMs: 1}); err == nil {
		t.Fatal("CreateRoom without tenant in context must fail closed")
	}
}

// The bridge discovery shape: a member's bridge must find ALL their rooms
// across tenants (it subscribes/advertises/claims in each), even though the
// session token it authenticates with was minted in just one tenant. The
// tenant-scoped RoomsForUser stays scoped for UI listings.
func TestRoomsForUserAllTenantsIsTheBridgeDiscoveryShape(t *testing.T) {
	s := NewMemoryRoom()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)

	// bob belongs to a room in tenant A and another in tenant B.
	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "a-room", Name: "a", CreatedBy: "bob", CreatedAtMs: 1}, Membership{RoomID: "a-room", UserID: "bob", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("a create: %v", err)
	}
	if err := s.CreateRoomWithOwner(cb, Room{RoomID: "b-room", Name: "b", CreatedBy: "bob", CreatedAtMs: 2}, Membership{RoomID: "b-room", UserID: "bob", Role: RoleOwner, JoinedAtMs: 2}); err != nil {
		t.Fatalf("b create: %v", err)
	}

	// Scoped listing (UI): each session tenant sees only its own room.
	if got := s.RoomsForUser(ca, "bob"); len(got) != 1 || got[0] != "a-room" {
		t.Fatalf("bob rooms in tenant A = %v, want [a-room]", got)
	}
	if got := s.RoomsForUser(cb, "bob"); len(got) != 1 || got[0] != "b-room" {
		t.Fatalf("bob rooms in tenant B = %v, want [b-room]", got)
	}

	// Bridge discovery: minted-in-A token still finds the B room — the bridge
	// must subscribe to both or b-room's agents show offline for everyone.
	if got := s.RoomsForUserAllTenants(ca, "bob"); len(got) != 2 {
		t.Fatalf("bob all-tenant rooms from A ctx = %v, want both rooms", got)
	}
	seen := map[string]bool{}
	for _, id := range s.RoomsForUserAllTenants(ca, "bob") {
		seen[id] = true
	}
	if !seen["a-room"] || !seen["b-room"] {
		t.Fatalf("all-tenant discovery missing rooms: %v", seen)
	}

	// Another user's rooms are never included.
	if got := s.RoomsForUserAllTenants(ca, "alice"); len(got) != 0 {
		t.Fatalf("alice all-tenant rooms = %v, want none", got)
	}
}

// The UI "my rooms" listing is tenant-scoped: switching the acting tenant
// switches the visible workspace. The cross-tenant shape exists ONLY for the
// bridge's room discovery (allTenants), which no UI call ever uses.
func TestRoomsForUserDetailedStaysTenantScoped(t *testing.T) {
	s := NewMemoryRoom()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)
	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "da", Name: "da", CreatedBy: "u1", CreatedAtMs: 1}, Membership{RoomID: "da", UserID: "u1", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := s.CreateRoomWithOwner(cb, Room{RoomID: "db", Name: "db", CreatedBy: "u1", CreatedAtMs: 2}, Membership{RoomID: "db", UserID: "u1", Role: RoleOwner, JoinedAtMs: 2}); err != nil {
		t.Fatalf("b: %v", err)
	}
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{{ca, "da"}, {cb, "db"}} {
		got := s.RoomsForUserDetailed(tc.ctx, "u1")
		if len(got) != 1 || got[0].RoomID != tc.want {
			t.Fatalf("detailed rooms in %v = %+v, want only %s", tc.ctx, got, tc.want)
		}
	}
}

// memberAnyTenant reports whether MembershipAnyTenant finds a membership.
func memberAnyTenant(s RoomStore, ctx context.Context, roomID, userID string) bool {
	_, ok := s.MembershipAnyTenant(ctx, roomID, userID)
	return ok
}
