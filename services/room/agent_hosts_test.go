package room

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// agentHostRoom seeds a shared room owned by alice with a human member bob.
func agentHostRoom(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Store.CreateRoomWithOwnerAndChannel(dctx(),
		store.Room{RoomID: "room-1", Name: "Team", Visibility: store.VisibilityShared, CreatedBy: "alice", CreatedAtMs: 1},
		store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner, JoinedAtMs: 1},
		store.RoomChannel{ChannelID: "ch-1", RoomID: "room-1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember, JoinedAtMs: 2})
}

// The internal agent-host routes are shareagent's only; through the shared
// membership client they add (idempotently) and remove the hidden
// membership, and the probe reports it.
func TestAgentHostRoutesViaMembershipClient(t *testing.T) {
	srv, svc := newTestServer(t)
	agentHostRoom(t, svc)

	// Another identity (a member, the gateway) is refused.
	for _, who := range []string{"alice", svcauth.ServiceSubject("gateway")} {
		resp := do(t, http.MethodPut, srv.URL+"/internal/v1/rooms/room-1/agent-hosts/root", who, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("PUT agent-host as %s = %d, want 403", who, resp.StatusCode)
		}
	}

	rc := membership.NewRoomClient(srv.URL)
	ctx := context.Background()
	if err := rc.PutAgentHost(ctx, tenant.Default, "room-1", "root"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := rc.PutAgentHost(ctx, tenant.Default, "room-1", "root"); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if err := rc.PutAgentHost(ctx, tenant.Default, "nope", "root"); err == nil {
		t.Fatal("agent host of a missing room must fail")
	}
	m, ok := rc.Lookup("room-1", "root")
	if !ok || !m.AgentHost || m.Role != store.RoleVisitor || m.IsHuman() || m.Tenant != tenant.Default {
		t.Fatalf("probe of the agent host = %+v %v", m, ok)
	}
	if !membership.HumanMember(rc, "room-1", "bob") {
		t.Fatal("a human member is a human member")
	}
	// A human member is never turned into (or removed as) an agent host.
	if err := rc.PutAgentHost(ctx, tenant.Default, "room-1", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := rc.DeleteAgentHost(ctx, tenant.Default, "room-1", "bob"); err != nil {
		t.Fatal(err)
	}
	mb, _, err := svc.Store.GetMember(dctx(), "room-1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if mb.AgentHost || mb.Role != store.RoleMember {
		t.Fatalf("bob after agent-host writes = %+v", mb)
	}
	if err := rc.DeleteAgentHost(ctx, tenant.Default, "room-1", "root"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, member := rc.Lookup("room-1", "root"); member {
		t.Fatal("the agent host survived its removal (the client cache must be dropped too)")
	}
	if member, err := svc.Store.IsMember(dctx(), "room-1", "root"); err != nil || member {
		t.Fatalf("the agent host survived its removal: member=%v err=%v", member, err)
	}
	if err := rc.DeleteAgentHost(ctx, tenant.Default, "room-1", "root"); err != nil {
		t.Fatalf("delete of an absent agent host = %v, want nil", err)
	}
}

// The host's own room lists leave agent-host rooms out unless the bridge
// asks with includeAgentHost=1; the member list flags them; member counts
// skip them.
func TestAgentHostListingSemantics(t *testing.T) {
	srv, svc := newTestServer(t)
	agentHostRoom(t, svc)
	if err := svc.Store.AddAgentHost(dctx(), "room-1", "root", 3); err != nil {
		t.Fatal(err)
	}

	var plain struct{ Rooms []string }
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/users/root/rooms", "root", nil), &plain)
	if len(plain.Rooms) != 0 {
		t.Fatalf("login room list = %v, want none", plain.Rooms)
	}
	var detail struct{ Rooms []map[string]any }
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/users/root/rooms?detail=1", "root", nil), &detail)
	if len(detail.Rooms) != 0 {
		t.Fatalf("detailed room list = %v, want none", detail.Rooms)
	}
	var all struct{ Rooms, AgentHostRooms []string }
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/users/root/rooms?allTenants=1", "root", nil), &all)
	if len(all.Rooms) != 0 || all.AgentHostRooms != nil {
		t.Fatalf("all-tenant list without includeAgentHost = %+v", all)
	}
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/users/root/rooms?allTenants=1&includeAgentHost=1", "root", nil), &all)
	if !slices.Equal(all.Rooms, []string{"room-1"}) || !slices.Equal(all.AgentHostRooms, []string{"room-1"}) {
		t.Fatalf("bridge discovery = %+v; want room-1 in rooms and agentHostRooms", all)
	}

	var members struct{ Members []map[string]any }
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/members", "bob", nil), &members)
	hosts := 0
	for _, m := range members.Members {
		if m["agentHost"] == true {
			hosts++
			if m["userId"] != "root" {
				t.Fatalf("flagged %v", m)
			}
		}
	}
	if len(members.Members) != 3 || hosts != 1 {
		t.Fatalf("members = %v; want 3 rows, the agent host flagged", members.Members)
	}
	var room map[string]any
	decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1", "bob", nil), &room)
	if room["memberCount"] != float64(2) {
		t.Fatalf("memberCount = %v, want 2", room["memberCount"])
	}
}

// An agent host is not a person: the people chat and channel creation are
// refused; joining the (shared) room makes it a human member.
func TestAgentHostHumanOnlySurfaces(t *testing.T) {
	srv, svc := newTestServer(t)
	agentHostRoom(t, svc)
	if err := svc.Store.AddAgentHost(dctx(), "room-1", "root", 3); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/rooms/room-1/people-chat"},
		{http.MethodGet, "/v1/rooms/room-1/people-chat"},
		{http.MethodPost, "/v1/rooms/room-1/channels"},
	} {
		resp := do(t, c.method, srv.URL+c.path, "root", map[string]string{"name": "x"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as agent host = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	// The bridge's reads work.
	resp := do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/channels", "root", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("channels as agent host = %d, want 200", resp.StatusCode)
	}
	var joined map[string]any
	decodeBody(t, do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/join", "root", nil), &joined)
	if joined["role"] != string(store.RoleMember) {
		t.Fatalf("join = %v", joined)
	}
	m, _, err := svc.Store.GetMember(dctx(), "room-1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if m.AgentHost {
		t.Fatal("joining must make the agent host a human member")
	}
}

// A handoff export never carries an agent host as a person (an import would
// make it a human member).
func TestAgentHostNotExportedAsMember(t *testing.T) {
	_, svc := newTestServer(t)
	agentHostRoom(t, svc)
	if err := svc.Store.AddAgentHost(dctx(), "room-1", "root", 3); err != nil {
		t.Fatal(err)
	}
	members, err := svc.Store.Members(dctx(), "room-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == "root" && !m.AgentHost {
			t.Fatal("seed")
		}
	}
	e := &roomExporter{svc: svc, ctx: dctx(), room: store.Room{RoomID: "room-1"}}
	manifest, err := e.manifest(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	exported := manifest.Members
	if len(exported) != 2 {
		t.Fatalf("exported members = %+v; want alice and bob", exported)
	}
	for _, m := range exported {
		if m.UserID == "root" {
			t.Fatal("the handoff manifest exported an agent host as a member")
		}
	}
}
