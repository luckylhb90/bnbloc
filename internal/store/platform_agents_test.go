package store

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A platform grant persists its source and adder, reserves without limits
// and no expiry, and stays outside the tenant's borrow ceilings (TenantUsage).
func TestPlatformGrant_memory(t *testing.T) { testPlatformGrant(t, NewMemoryGrant()) }

func TestPlatformGrant_mysql(t *testing.T) {
	db, err := OpenMySQL(scratchDB(t, "platformgrant"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testPlatformGrant(t, NewMySQLGrant(db))
}

func testPlatformGrant(t *testing.T, g GrantStore) {
	t.Helper()
	ctx := tenantCtx(tenantA)
	if err := g.PutGrant(ctx, Grant{
		GrantID: "pg", RoomID: "r1", TargetID: "shared-agent:nimbus:codex", OwnerUserID: "host", GranteeUserID: "*",
		CreatedAtMs: 5, CreatedBy: "bob", Source: GrantSourcePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	quota := 10
	if err := g.PutGrant(ctx, Grant{
		GrantID: "own", RoomID: "r1", TargetID: "shared-agent:nimbus:codex", OwnerUserID: "alice", GranteeUserID: "*",
		RunQuota: &quota, CreatedAtMs: 6, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := g.GetGrant(ctx, "pg")
	if err != nil || got.Source != GrantSourcePlatform || got.CreatedBy != "bob" || got.CreatedAtMs != 5 || got.ExpiresAtMs != nil || got.RunQuota != nil {
		t.Fatalf("platform grant = %+v %v", got, err)
	}
	var seen int
	for _, rg := range g.RoomGrants(ctx, "r1") {
		if rg.GrantID == "pg" && rg.Source == GrantSourcePlatform && rg.CreatedBy == "bob" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("RoomGrants lost the platform grant's source/adder")
	}
	for i := range 3 {
		if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: "rsv_p" + string(rune('a'+i)), GrantID: "pg", RoomID: "r1", OwnerUserID: "host", BorrowerUserID: "bob", AtMs: 1 << 50}); err != nil {
			t.Fatalf("reserve platform run %d: %v", i, err)
		}
	}
	if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: "rsv_o", GrantID: "own", RoomID: "r1", OwnerUserID: "alice", BorrowerUserID: "bob", AtMs: 7}); err != nil {
		t.Fatal(err)
	}
	if runs, _, _ := g.TenantUsage(ctx); runs != 1 {
		t.Fatalf("tenant borrow runs = %d, want 1 (platform runs are outside the org ceilings)", runs)
	}
	// Usage accountability: the platform runs are audited as borrowed turns.
	turns := 0
	for _, rec := range g.AuditRecords(ctx, "r1", 0, 0) {
		if rec.GrantID == "pg" && rec.Action == "turn" && rec.Decision == "allow" && rec.ActorUserID == "bob" {
			turns++
		}
	}
	if turns != 3 {
		t.Fatalf("audited platform turns = %d, want 3", turns)
	}
	// A re-put (a re-add after removal) is a fresh grant with its new adder.
	if err := g.PutGrant(ctx, Grant{
		GrantID: "pg", RoomID: "r1", TargetID: "shared-agent:nimbus:codex", OwnerUserID: "host", GranteeUserID: "*",
		CreatedAtMs: 9, CreatedBy: "carol", Source: GrantSourcePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.GetGrant(ctx, "pg"); got.CreatedBy != "carol" || got.CreatedAtMs != 9 || got.RunsUsed != 0 {
		t.Fatalf("re-added grant = %+v", got)
	}
}

// The policy round-trips through the platform settings, one entry per
// host; unset publishes nothing.
func TestPlatformAgentsPolicySetting(t *testing.T) {
	st := NewMemoryPlatformSettings()
	ctx := testCtx()
	p, err := LoadPlatformAgents(ctx, st)
	if err != nil || p.Hosts == nil || len(p.Hosts) != 0 || len(p.Published()) != 0 {
		t.Fatalf("unset policy = %+v %v", p, err)
	}
	want := wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{
		{HostUserID: "root", Enabled: true, Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex", DefaultModel: "free-1"}}},
		{HostUserID: "root2", Enabled: true, Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}, {Core: "nimbus", Provider: "claude"}}},
		{HostUserID: "root3", Enabled: false, Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "gemini"}}},
	}}
	if err := SavePlatformAgents(ctx, st, want, "root", 3); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPlatformAgents(ctx, st)
	if err != nil || len(got.Hosts) != 3 || !got.Publishes("root", "nimbus", "codex") || !got.Publishes("root2", "nimbus", "claude") {
		t.Fatalf("policy = %+v %v", got, err)
	}
	if spec, ok := got.Agent("root", "nimbus", "codex"); !ok || spec.DefaultModel != "free-1" {
		t.Fatalf("root codex spec = %+v %v", spec, ok)
	}
	if got.Publishes("other", "nimbus", "codex") || got.Publishes("root", "nimbus", "claude") || got.Publishes("root3", "nimbus", "gemini") {
		t.Fatal("Publishes must match an enabled host and the exact agent")
	}
	pub := got.Published()
	if len(pub) != 2 || pub[0].Ref.Provider != "codex" || len(pub[0].Hosts) != 2 || pub[1].Ref.Provider != "claude" || len(pub[1].Hosts) != 1 {
		t.Fatalf("published = %+v", pub)
	}
	// WithHost replaces one entry and keeps the others.
	next := got.WithHost(wire.PlatformAgentHost{HostUserID: "root2", Enabled: false, Agents: []wire.PlatformAgentSpec{}})
	if len(next.Hosts) != 3 || next.Publishes("root2", "nimbus", "codex") || !next.Publishes("root", "nimbus", "codex") {
		t.Fatalf("with host = %+v", next)
	}
	if hosts := next.HostsOf(wire.PlatformAgentRef{Core: "nimbus", Provider: "codex"}); len(hosts) != 1 || hosts[0].HostUserID != "root" {
		t.Fatalf("hosts of codex = %+v", hosts)
	}
	if err := st.PutPlatformSetting(ctx, PlatformSetting{Key: PlatformAgentsSettingKey, Value: "{"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlatformAgents(ctx, st); err == nil {
		t.Fatal("a corrupt policy must be an error (callers fail closed)")
	}
}
