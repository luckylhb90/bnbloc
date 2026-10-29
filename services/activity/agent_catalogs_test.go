package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// catalogEnv: room-1 has human members alice and bob and the agent host root
// (a super admin whose platform agent nimbus/codex was added to the room).
// root also runs nimbus/claude (published, but its room grant was revoked)
// and nimbus/gemini (granted once, never published), plus nimbus/private
// (never published, no grant). dave runs agents but is not in the room.
type catalogEnv struct {
	svc      *Service
	settings *store.MemoryPlatformSettings
	rooms    *store.MemoryRoom
	jobs     *store.MemoryBridgeJobs
	grants   *store.MemoryGrant
}

func newCatalogEnv(t *testing.T) *catalogEnv {
	t.Helper()
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	now := time.Now().UnixMilli()

	rooms := store.NewMemoryRoom()
	if err := rooms.CreateRoom(ctx, store.Room{RoomID: "room-1", Name: "Room"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.Membership{{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner}, {RoomID: "room-1", UserID: "bob", Role: store.RoleMember}} {
		if err := rooms.AddMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := rooms.AddAgentHost(ctx, "room-1", "root", now); err != nil {
		t.Fatal(err)
	}
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	if err := mem.PutAgentHost(ctx, tenant.Default, "room-1", "root"); err != nil {
		t.Fatal(err)
	}

	jobs := store.NewMemoryBridgeJobs()
	advertise := func(bridge, owner string, agents ...store.BridgeAgent) {
		t.Helper()
		if err := jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: bridge, OwnerUserID: owner, Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
			t.Fatal(err)
		}
		for i := range agents {
			agents[i].OwnerUserID = owner
		}
		if err := jobs.ReplaceBridgeAgents(ctx, bridge, agents); err != nil {
			t.Fatal(err)
		}
	}
	catalog := func(models ...string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"models": models, "modelNames": map[string]string{models[0]: "Name of " + models[0]}, "defaultModel": models[0], "reasoningEfforts": nil})
		return raw
	}
	advertise("b-alice", "alice", store.BridgeAgent{Core: "acme", Provider: "codex", ModelOptionsJSON: catalog("gpt-5", "o3"), LastSeenAtMs: now})
	// alice's second device advertises an older catalog for the same agent.
	advertise("b-alice-2", "alice", store.BridgeAgent{Core: "acme", Provider: "codex", ModelOptionsJSON: catalog("stale"), LastSeenAtMs: now - 5_000})
	advertise("b-bob", "bob", store.BridgeAgent{Core: "acme", Provider: "codex", ModelOptionsJSON: catalog("bob-model"), LastSeenAtMs: now})
	advertise("b-root", "root",
		store.BridgeAgent{Core: "nimbus", Provider: "codex", ModelOptionsJSON: catalog("host-model"), LastSeenAtMs: now},
		store.BridgeAgent{Core: "nimbus", Provider: "claude", ModelOptionsJSON: catalog("opus"), LastSeenAtMs: now},
		store.BridgeAgent{Core: "nimbus", Provider: "gemini", ModelOptionsJSON: catalog("pro"), LastSeenAtMs: now},
		store.BridgeAgent{Core: "nimbus", Provider: "private", ModelOptionsJSON: catalog("secret"), LastSeenAtMs: now},
	)
	advertise("b-dave", "dave", store.BridgeAgent{Core: "acme", Provider: "codex", ModelOptionsJSON: catalog("dave-model"), LastSeenAtMs: now})

	settings := store.NewMemoryPlatformSettings()
	if _, err := store.UpdatePlatformAgents(ctx, settings, "root", now, func(wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
		return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{{
			HostUserID: "root", Enabled: true,
			Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}, {Core: "nimbus", Provider: "claude"}},
		}}}
	}); err != nil {
		t.Fatal(err)
	}
	grants := store.NewMemoryGrant()
	revoked := now - 1
	for _, g := range []store.Grant{
		{GrantID: "g-codex", TargetID: "shared-agent:nimbus:codex"},
		{GrantID: "g-claude", TargetID: "shared-agent:nimbus:claude", RevokedAtMs: &revoked},
		{GrantID: "g-gemini", TargetID: "shared-agent:nimbus:gemini"},
	} {
		g.RoomID, g.OwnerUserID, g.GranteeUserID, g.Source, g.CreatedAtMs = "room-1", "root", "*", store.GrantSourcePlatform, now
		if err := grants.PutGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	// An owner-issued grant of root's private agent (as if root, a member
	// elsewhere, shared it here once): not a platform grant, so it never
	// admits an agent host's agent.
	if err := grants.PutGrant(ctx, store.Grant{GrantID: "g-private", RoomID: "room-1", TargetID: "shared-agent:nimbus:private", OwnerUserID: "root", GranteeUserID: "*", CreatedAtMs: now}); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: mem, BridgeJobs: jobs, Rooms: rooms, Grants: grants, Settings: settings}
	return &catalogEnv{svc: svc, settings: settings, rooms: rooms, jobs: jobs, grants: grants}
}

func (e *catalogEnv) get(t *testing.T, caller string) (int, wire.RoomAgentCatalogsReply) {
	t.Helper()
	code, body := getRaw(t, e.svc, "/v1/rooms/room-1/agent-catalogs", caller)
	var out wire.RoomAgentCatalogsReply
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return code, out
}

// summary renders "owner/core/provider=models" rows in reply order.
func summary(r wire.RoomAgentCatalogsReply) string {
	rows := make([]string, 0, len(r.Agents))
	for _, a := range r.Agents {
		models := ""
		if a.ModelOptions != nil {
			models = strings.Join(a.ModelOptions.Models, "|")
		}
		rows = append(rows, a.OwnerUserID+"/"+a.Core+"/"+a.Provider+"="+models)
	}
	return strings.Join(rows, ",")
}

// Every room member reads the catalogs of every addressable agent, keyed by
// owner: two members' same (core, provider) stay two rows with their own
// catalogs, and the host contributes only its added, published platform agent.
func TestRoomAgentCatalogsOwnerKeyedAndHostScoped(t *testing.T) {
	e := newCatalogEnv(t)
	want := "alice/acme/codex=gpt-5|o3,bob/acme/codex=bob-model,root/nimbus/codex=host-model"
	for _, caller := range []string{"alice", "bob"} {
		code, out := e.get(t, caller)
		if code != http.StatusOK || summary(out) != want {
			t.Fatalf("%s: catalogs = %d %q, want %q", caller, code, summary(out), want)
		}
	}
	_, out := e.get(t, "bob")
	for _, a := range out.Agents {
		if !a.Online || a.ModelOptions == nil || a.ModelOptions.DefaultModel != a.ModelOptions.Models[0] {
			t.Fatalf("row %+v: want online with the advertised catalog", a)
		}
		// The display names an agent gave its models are advertised with them.
		if first := a.ModelOptions.Models[0]; a.ModelOptions.ModelNames[first] != "Name of "+first {
			t.Fatalf("row %+v: the model names did not reach the member", a)
		}
	}
}

// Only room members may read; a missing identity is 401.
func TestRoomAgentCatalogsRequireMembership(t *testing.T) {
	e := newCatalogEnv(t)
	if code, _ := e.get(t, "dave"); code != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", code)
	}
	if code, _ := e.get(t, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", code)
	}
}

// Unpublishing hides the host's agent at once (the room grant is still
// there); an unreadable policy fails closed for hosts only.
func TestRoomAgentCatalogsHostAgentsFollowPolicy(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if _, err := store.UpdatePlatformAgents(ctx, e.settings, "root", 1, func(wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
		return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{{HostUserID: "root", Enabled: false,
			Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}}}}}
	}); err != nil {
		t.Fatal(err)
	}
	members := "alice/acme/codex=gpt-5|o3,bob/acme/codex=bob-model"
	if _, out := e.get(t, "alice"); summary(out) != members {
		t.Fatalf("disabled policy: catalogs = %q, want %q", summary(out), members)
	}
	if err := e.settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: store.PlatformAgentsSettingKey, Value: "{"}); err != nil {
		t.Fatal(err)
	}
	if code, out := e.get(t, "alice"); code != http.StatusOK || summary(out) != members {
		t.Fatalf("corrupt policy: catalogs = %d %q, want %q", code, summary(out), members)
	}
}

// Without the grant/policy stores an agent host contributes nothing.
func TestRoomAgentCatalogsWithoutPlatformStoresOmitHosts(t *testing.T) {
	e := newCatalogEnv(t)
	e.svc.Grants, e.svc.Settings = nil, nil
	if _, out := e.get(t, "alice"); summary(out) != "alice/acme/codex=gpt-5|o3,bob/acme/codex=bob-model" {
		t.Fatalf("catalogs = %q", summary(out))
	}
}

// Two hosts serving the same platform agent in the room each contribute
// their own catalog of it, and only of it.
func TestRoomAgentCatalogsMultiHost(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	now := time.Now().UnixMilli()
	if err := e.rooms.AddAgentHost(ctx, "room-1", "root2", now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Membership.(*membership.Static).PutAgentHost(ctx, tenant.Default, "room-1", "root2"); err != nil {
		t.Fatal(err)
	}
	if err := e.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "b-root2", OwnerUserID: "root2", Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	cat := func(m string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"models": []string{m}, "defaultModel": m})
		return raw
	}
	if err := e.jobs.ReplaceBridgeAgents(ctx, "b-root2", []store.BridgeAgent{
		{OwnerUserID: "root2", Core: "nimbus", Provider: "codex", ModelOptionsJSON: cat("root2-model"), LastSeenAtMs: now},
		{OwnerUserID: "root2", Core: "nimbus", Provider: "claude", ModelOptionsJSON: cat("root2-opus"), LastSeenAtMs: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.grants.PutGrant(ctx, store.Grant{GrantID: "g-codex-2", RoomID: "room-1", TargetID: "shared-agent:nimbus:codex",
		OwnerUserID: "root2", GranteeUserID: "*", Source: store.GrantSourcePlatform, CreatedAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePlatformAgents(ctx, e.settings, "root2", now, func(policy wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
		return policy.WithHost(wire.PlatformAgentHost{HostUserID: "root2", Enabled: true,
			Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}, {Core: "nimbus", Provider: "claude"}}})
	}); err != nil {
		t.Fatal(err)
	}
	want := "alice/acme/codex=gpt-5|o3,bob/acme/codex=bob-model,root/nimbus/codex=host-model,root2/nimbus/codex=root2-model"
	if _, out := e.get(t, "alice"); summary(out) != want {
		t.Fatalf("catalogs = %q, want %q", summary(out), want)
	}
}
