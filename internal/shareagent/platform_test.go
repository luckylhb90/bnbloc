package shareagent

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestPlatformAgentOfTarget(t *testing.T) {
	for target, want := range map[string]wire.PlatformAgentRef{
		"shared-agent:nimbus:codex":    {Core: "nimbus", Provider: "codex"},
		"shared-agent:acme:acp:hermes": {Core: "acme", Provider: "acp:hermes"},
	} {
		if got, ok := PlatformAgentOfTarget(target); !ok || got != want {
			t.Errorf("%s = %+v %v, want %+v", target, got, ok, want)
		}
	}
	for _, bad := range []string{"", "shared-agent:codex", "shared-agent::codex", "shared-agent:nimbus:", "nimbus:codex"} {
		if _, ok := PlatformAgentOfTarget(bad); ok {
			t.Errorf("%q parsed, want refused", bad)
		}
	}
}

// Only live, well-formed platform grants the policy still publishes for
// their host are available; owner-issued grants are not listed at all.
func TestRoomPlatformGrantsAvailability(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	grants := store.NewMemoryGrant()
	past := int64(50)
	for _, g := range []store.Grant{
		{GrantID: "live", TargetID: "shared-agent:c:codex", OwnerUserID: "root", Source: store.GrantSourcePlatform},
		{GrantID: "expired", TargetID: "shared-agent:c:claude", OwnerUserID: "root", Source: store.GrantSourcePlatform, ExpiresAtMs: &past},
		{GrantID: "unpublished", TargetID: "shared-agent:c:gemini", OwnerUserID: "root", Source: store.GrantSourcePlatform},
		{GrantID: "other-host", TargetID: "shared-agent:c:codex", OwnerUserID: "eve", Source: store.GrantSourcePlatform},
		{GrantID: "owner-issued", TargetID: "shared-agent:c:codex", OwnerUserID: "alice"},
	} {
		g.RoomID = "room-1"
		if err := grants.PutGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	policy := wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{{HostUserID: "root", Enabled: true,
		Agents: []wire.PlatformAgentSpec{{Core: "c", Provider: "codex"}, {Core: "c", Provider: "claude"}}}}}
	pgs, err := RoomPlatformGrants(ctx, grants, policy, "room-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, pg := range pgs {
		got[pg.Grant.GrantID] = pg.Available
	}
	want := map[string]bool{"live": true, "expired": false, "unpublished": false, "other-host": false}
	if len(got) != len(want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s available = %v, want %v", id, got[id], w)
		}
	}
}

// Two hosts publishing the same agent each make their own grant available;
// a disabled host entry publishes nothing.
func TestRoomPlatformGrantsMultiHost(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	grants := store.NewMemoryGrant()
	for _, g := range []store.Grant{
		{GrantID: "root", TargetID: "shared-agent:c:codex", OwnerUserID: "root", Source: store.GrantSourcePlatform},
		{GrantID: "root2", TargetID: "shared-agent:c:codex", OwnerUserID: "root2", Source: store.GrantSourcePlatform},
		{GrantID: "root3", TargetID: "shared-agent:c:codex", OwnerUserID: "root3", Source: store.GrantSourcePlatform},
	} {
		g.RoomID = "room-1"
		_ = grants.PutGrant(ctx, g)
	}
	codex := []wire.PlatformAgentSpec{{Core: "c", Provider: "codex"}}
	policy := wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{
		{HostUserID: "root", Enabled: true, Agents: codex},
		{HostUserID: "root2", Enabled: true, Agents: codex},
		{HostUserID: "root3", Enabled: false, Agents: codex},
	}}
	pgs, err := RoomPlatformGrants(ctx, grants, policy, "room-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, pg := range pgs {
		got[pg.Grant.GrantID] = pg.Available
	}
	if !got["root"] || !got["root2"] || got["root3"] {
		t.Fatalf("availability = %v", got)
	}
}

type fixedAds map[string][]store.BridgeAgent

func (f fixedAds) ListBridgeAgentsForOwner(_ context.Context, owner string) ([]store.BridgeAgent, error) {
	return f[owner], nil
}

// A chat is a platform drive only for someone else's agent the room holds an
// available platform grant of; online follows the host's advertisement.
func TestReadPlatformDrive(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	grants := store.NewMemoryGrant()
	_ = grants.PutGrant(ctx, store.Grant{GrantID: "p", RoomID: "room-1", TargetID: "shared-agent:c:codex", OwnerUserID: "root", Source: store.GrantSourcePlatform})
	_ = grants.PutGrant(ctx, store.Grant{GrantID: "o", RoomID: "room-1", TargetID: "shared-agent:c:claude", OwnerUserID: "alice"})
	policy := wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{{HostUserID: "root", Enabled: true,
		Agents: []wire.PlatformAgentSpec{{Core: "c", Provider: "codex"}}}}}
	codex := wire.PlatformAgentRef{Core: "c", Provider: "codex"}
	offline := fixedAds{}
	online := fixedAds{"root": {{OwnerUserID: "root", Core: "c", Provider: "codex", TargetID: "c/codex"}}}
	if d, err := ReadPlatformDrive(ctx, grants, policy, offline, "room-1", "bob", "root", codex, 1); err != nil || !d.Platform || d.Online {
		t.Fatalf("offline host = %+v", d)
	}
	if d, err := ReadPlatformDrive(ctx, grants, policy, online, "room-1", "bob", "root", codex, 1); err != nil || !d.Platform || !d.Online {
		t.Fatalf("online host = %+v", d)
	}
	if d, err := ReadPlatformDrive(ctx, grants, policy, offline, "room-1", "root", "root", codex, 1); err != nil || d.Platform {
		t.Fatal("the host driving their own agent is not a platform drive")
	}
	if d, err := ReadPlatformDrive(ctx, grants, policy, offline, "room-1", "bob", "alice", wire.PlatformAgentRef{Core: "c", Provider: "claude"}, 1); err != nil || d.Platform {
		t.Fatal("an owner-issued borrow is not a platform drive")
	}
	if d, err := ReadPlatformDrive(ctx, grants, wire.PlatformAgentsPolicy{}, offline, "room-1", "bob", "root", codex, 1); err != nil || d.Platform {
		t.Fatal("an unpublished agent is not a platform drive")
	}
}
