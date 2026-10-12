package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A platform grant persists its source and adder, reserves without limits
// and no expiry, and stays outside the tenant's borrow ceilings (TenantUsage).
func TestContractGrantStorePlatformGrant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) { testPlatformGrant(t, b.Grant, uniq("pg-")) })
}

func testPlatformGrant(t *testing.T, g GrantStore, p string) {
	t.Helper()
	ctx := tenantCtx(p + "-t")
	pg, own, r1 := p+"-pg", p+"-own", p+"-r1"
	if err := g.PutGrant(ctx, Grant{
		GrantID: pg, RoomID: r1, TargetID: "shared-agent:nimbus:codex", OwnerUserID: "host", GranteeUserID: "*",
		CreatedAtMs: 5, CreatedBy: "bob", Source: GrantSourcePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	quota := 10
	if err := g.PutGrant(ctx, Grant{
		GrantID: own, RoomID: r1, TargetID: "shared-agent:nimbus:codex", OwnerUserID: "alice", GranteeUserID: "*",
		RunQuota: &quota, CreatedAtMs: 6, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := g.GetGrant(ctx, pg)
	if err != nil || got.Source != GrantSourcePlatform || got.CreatedBy != "bob" || got.CreatedAtMs != 5 || got.ExpiresAtMs != nil || got.RunQuota != nil {
		t.Fatalf("platform grant = %+v %v", got, err)
	}
	var seen int
	for _, rg := range must(g.RoomGrants(ctx, r1))(t) {
		if rg.GrantID == pg && rg.Source == GrantSourcePlatform && rg.CreatedBy == "bob" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("RoomGrants lost the platform grant's source/adder")
	}
	for i := range 3 {
		if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: p + "-rsv-" + string(rune('a'+i)), GrantID: pg, RoomID: r1, OwnerUserID: "host", BorrowerUserID: "bob", AtMs: 1 << 50}); err != nil {
			t.Fatalf("reserve platform run %d: %v", i, err)
		}
	}
	if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: p + "-rsv-o", GrantID: own, RoomID: r1, OwnerUserID: "alice", BorrowerUserID: "bob", AtMs: 7}); err != nil {
		t.Fatal(err)
	}
	if runs, _, _ := g.TenantUsage(ctx); runs != 1 {
		t.Fatalf("tenant borrow runs = %d, want 1 (platform runs are outside the org ceilings)", runs)
	}
	// Usage accountability: the platform runs are audited as borrowed turns.
	turns := 0
	for _, rec := range must(g.AuditRecords(ctx, AuditRecordQuery{RoomID: r1}))(t) {
		if rec.GrantID == pg && rec.Action == "turn" && rec.Decision == "allow" && rec.ActorUserID == "bob" {
			turns++
		}
	}
	if turns != 3 {
		t.Fatalf("audited platform turns = %d, want 3", turns)
	}
	// A re-put (a re-add after removal) is a fresh grant with its new adder.
	if err := g.PutGrant(ctx, Grant{
		GrantID: pg, RoomID: r1, TargetID: "shared-agent:nimbus:codex", OwnerUserID: "host", GranteeUserID: "*",
		CreatedAtMs: 9, CreatedBy: "carol", Source: GrantSourcePlatform,
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.GetGrant(ctx, pg); got.CreatedBy != "carol" || got.CreatedAtMs != 9 || got.RunsUsed != 0 {
		t.Fatalf("re-added grant = %+v", got)
	}
}

// The platform-agent policy is saved compare-and-set: hosts saving their
// entries at the same time all keep them (a load-modify-save used to lose
// all but one).
func TestContractPlatformSettingsStoreConcurrentPolicySaves(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		if _, err := UpdatePlatformAgents(ctx, b.Settings, "root", 1, func(wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
			return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{}}
		}); err != nil {
			t.Fatal(err)
		}
		hosts := []string{uniq("h0-"), uniq("h1-"), uniq("h2-"), uniq("h3-"), uniq("h4-"), uniq("h5-"), uniq("h6-"), uniq("h7-")}
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(i int, h string) {
				defer wg.Done()
				entry := wire.PlatformAgentHost{HostUserID: h, Enabled: true, Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}}}
				if _, err := UpdatePlatformAgents(ctx, b.Settings, h, int64(10+i), func(cur wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
					return cur.WithHost(entry)
				}); err != nil {
					t.Errorf("save %s: %v", h, err)
				}
			}(i, h)
		}
		wg.Wait()
		p := must(LoadPlatformAgents(ctx, b.Settings))(t)
		for _, h := range hosts {
			if !p.Publishes(h, "nimbus", "codex") {
				t.Errorf("host %s lost its entry: %+v", h, p.Hosts)
			}
		}
		if len(p.Hosts) != len(hosts) {
			t.Fatalf("hosts = %d, want %d", len(p.Hosts), len(hosts))
		}
	})
}

// SwapPlatformSetting writes only over the row the caller read: an unset key
// only while unset, a set one only while value, time and author are as read
// (the value byte-exact).
func TestContractPlatformSettingsStoreSwap(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Settings
		ctx := context.Background()
		key := uniq("swap.")
		v1 := PlatformSetting{Key: key, Value: "Alpha", UpdatedAtMs: 1, UpdatedBy: "maya"}
		if err := s.SwapPlatformSetting(ctx, nil, v1); err != nil {
			t.Fatalf("swap into an unset key: %v", err)
		}
		if err := s.SwapPlatformSetting(ctx, nil, v1); !errors.Is(err, ErrPlatformSettingChanged) {
			t.Fatalf("swap expecting unset over a set key = %v, want ErrPlatformSettingChanged", err)
		}
		stale := v1
		stale.Value = "alpha" // differs in case only
		v2 := PlatformSetting{Key: key, Value: "Beta", UpdatedAtMs: 2, UpdatedBy: "jack"}
		if err := s.SwapPlatformSetting(ctx, &stale, v2); !errors.Is(err, ErrPlatformSettingChanged) {
			t.Fatalf("swap over a case-variant value = %v, want ErrPlatformSettingChanged", err)
		}
		staleAt := v1
		staleAt.UpdatedAtMs = 7
		if err := s.SwapPlatformSetting(ctx, &staleAt, v2); !errors.Is(err, ErrPlatformSettingChanged) {
			t.Fatalf("swap over another time = %v, want ErrPlatformSettingChanged", err)
		}
		if err := s.SwapPlatformSetting(ctx, &v1, v2); err != nil {
			t.Fatalf("swap over the row as read: %v", err)
		}
		if got, ok := must2(s.GetPlatformSetting(ctx, key))(t); !ok || got != v2 {
			t.Fatalf("stored = %+v %v, want %+v", got, ok, v2)
		}
		// Writing the same row again is a match, not a change.
		if err := s.SwapPlatformSetting(ctx, &v2, v2); err != nil {
			t.Fatalf("identical swap: %v", err)
		}
		// UpdatePlatformSetting aborts on a change error and writes nothing.
		boom := errors.New("boom")
		if _, err := UpdatePlatformSetting(ctx, s, key, "maya", 3, func(string, bool) (string, error) { return "", boom }); !errors.Is(err, boom) {
			t.Fatalf("update with a failing change = %v", err)
		}
		if got, _ := must2(s.GetPlatformSetting(ctx, key))(t); got != v2 {
			t.Fatalf("a failed update wrote %+v", got)
		}
	})
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
	saved, err := UpdatePlatformAgents(ctx, st, "root", 3, func(cur wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
		if len(cur.Hosts) != 0 {
			t.Errorf("unset policy passed to change = %+v", cur)
		}
		return want
	})
	if err != nil || len(saved.Hosts) != 3 {
		t.Fatalf("update = %+v, %v", saved, err)
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
	if _, err := UpdatePlatformAgents(ctx, st, "root", 4, func(p wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy { return p }); err == nil {
		t.Fatal("an update over a corrupt policy must fail, not overwrite it")
	}
}
