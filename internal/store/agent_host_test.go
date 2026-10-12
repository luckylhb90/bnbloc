package store

import (
	"errors"
	"slices"
	"testing"
)

// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2): an
// agent-host membership lets the host's bridge read the room but is not a
// human membership. The same contract holds for the memory and MySQL stores.
func TestContractRoomStoreAgentHost(t *testing.T) {
	forEachBackend(t, testAgentHostMembership)
}

func testAgentHostMembership(t *testing.T, b *Backends) {
	t.Helper()
	s := b.Room
	ctx := tenantCtx(uniq("t-host-"))
	host := uniq("host-")
	r1, r2 := "r1-"+uniq(""), "r2-"+uniq("")
	for _, id := range []string{r1, r2} {
		if err := s.CreateRoomWithOwner(ctx, Room{RoomID: id, Name: id, Visibility: VisibilityShared, CreatedBy: "alice", CreatedAtMs: 1},
			Membership{RoomID: id, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := s.AddAgentHost(ctx, r1, host, 5); err != nil {
		t.Fatalf("add agent host: %v", err)
	}
	// Idempotent.
	if err := s.AddAgentHost(ctx, r1, host, 6); err != nil {
		t.Fatalf("re-add agent host: %v", err)
	}
	if err := s.AddAgentHost(ctx, uniq("gone-"), host, 5); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("agent host of a missing room = %v, want ErrRoomNotFound", err)
	}

	m, ok := must2(s.GetMember(ctx, r1, host))(t)
	if !ok || !m.AgentHost || m.Role != RoleVisitor {
		t.Fatalf("agent host membership = %+v %v; want an agent-host visitor", m, ok)
	}
	if !must(s.IsMember(ctx, r1, host))(t) {
		t.Fatal("an agent host reads the room (its bridge): IsMember must hold")
	}
	if am, ok := must2(s.MembershipAnyTenant(tenantCtx(uniq("t-host-other-")), r1, host))(t); !ok || !am.AgentHost {
		t.Fatalf("cross-tenant probe = %+v %v; want the agent-host flag", am, ok)
	}
	// Not the host's rooms, not counted, flagged in the member list.
	if got := must(s.RoomsForUser(ctx, host))(t); len(got) != 0 {
		t.Fatalf("RoomsForUser(host) = %v, want none", got)
	}
	if got := must(s.RoomsForUserAllTenants(ctx, host))(t); len(got) != 0 {
		t.Fatalf("RoomsForUserAllTenants(host) = %v, want none", got)
	}
	if got := must(s.RoomsForUserDetailed(ctx, host))(t); len(got) != 0 {
		t.Fatalf("RoomsForUserDetailed(host) = %v, want none", got)
	}
	if got := must(s.AgentHostRoomsAllTenants(ctx, host))(t); !slices.Equal(got, []string{r1}) {
		t.Fatalf("AgentHostRoomsAllTenants(host) = %v, want [r1]", got)
	}
	if got := must(s.AgentHostRoomsAllTenants(ctx, "alice"))(t); len(got) != 0 {
		t.Fatalf("a human member's agent-host rooms = %v, want none", got)
	}
	if n := must(s.MemberCount(ctx, r1))(t); n != 1 {
		t.Fatalf("MemberCount = %d, want 1 (agent hosts are not counted)", n)
	}
	if rows := must(s.RoomsForUserDetailed(ctx, "alice"))(t); len(rows) != 2 || rows[0].MemberCount != 1 || rows[1].MemberCount != 1 {
		t.Fatalf("detailed rows = %+v; member counts exclude agent hosts", rows)
	}
	if rooms, _, err := s.SearchRooms(ctx, r1, 10, 0); err != nil || len(rooms) != 1 || rooms[0].MemberCount != 1 {
		t.Fatalf("plaza = %+v %v; member counts exclude agent hosts", rooms, err)
	}
	flagged := 0
	for _, mem := range must(s.Members(ctx, r1))(t) {
		if mem.AgentHost {
			flagged++
			if mem.UserID != host {
				t.Fatalf("flagged %q", mem.UserID)
			}
		}
	}
	if flagged != 1 {
		t.Fatalf("Members flagged %d agent hosts, want 1", flagged)
	}
	if err := s.TransferOwner(ctx, r1, host); err == nil {
		t.Fatal("an agent host cannot become the room owner")
	}

	// A human membership is never downgraded or removed by the agent-host
	// writes, and a human join converts the agent-host row.
	if err := s.AddAgentHost(ctx, r2, "alice", 7); err != nil {
		t.Fatal(err)
	}
	if m, _ := must2(s.GetMember(ctx, r2, "alice"))(t); m.AgentHost || m.Role != RoleOwner {
		t.Fatalf("owner after AddAgentHost = %+v; must stay a human owner", m)
	}
	if err := s.RemoveAgentHost(ctx, r2, "alice"); err != nil || !must(s.IsMember(ctx, r2, "alice"))(t) {
		t.Fatalf("RemoveAgentHost removed a human member (err %v)", err)
	}
	if err := s.AddMember(ctx, Membership{RoomID: r1, UserID: host, Role: RoleMember, JoinedAtMs: 8}); err != nil {
		t.Fatal(err)
	}
	if m, _ := must2(s.GetMember(ctx, r1, host))(t); m.AgentHost || m.Role != RoleMember {
		t.Fatalf("joined agent host = %+v; want a human member", m)
	}
	if got := must(s.RoomsForUser(ctx, host))(t); !slices.Equal(got, []string{r1}) {
		t.Fatalf("RoomsForUser after joining = %v", got)
	}

	// Removal only touches an agent-host row.
	if err := s.AddAgentHost(ctx, r2, host, 9); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAgentHost(ctx, r2, host); err != nil {
		t.Fatal(err)
	}
	if must(s.IsMember(ctx, r2, host))(t) {
		t.Fatal("RemoveAgentHost left the agent-host membership")
	}
	if err := s.RemoveAgentHost(ctx, r2, host); err != nil {
		t.Fatalf("removing an absent agent host = %v, want nil", err)
	}

	// An invite makes an agent host a human member (and consumes the invite).
	if err := s.AddAgentHost(ctx, r2, host, 10); err != nil {
		t.Fatal(err)
	}
	code := uniq("inv-")
	if err := s.CreateInvite(ctx, Invite{Code: code, RoomID: r2, Role: RoleMember, ExpiresAtMs: 1 << 50, Capacity: 1, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RedeemInvite(ctx, code, host, 11)
	if err != nil || got.Role != RoleMember || got.AgentHost {
		t.Fatalf("redeem by an agent host = %+v %v; want a human member", got, err)
	}
	if m, _ := must2(s.GetMember(ctx, r2, host))(t); m.AgentHost {
		t.Fatalf("membership after redeem = %+v", m)
	}
}
