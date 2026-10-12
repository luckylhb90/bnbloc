package store

import (
	"slices"
	"strings"
	"testing"
)

// The team dashboard read side (TeamAgentReader) over memory and MySQL.

// TenantTeamAgents lists the tenant's enabled advertisements with their
// bridge's liveness (BridgeLive: online and not expired; draining, offline
// or a missing heartbeat is offline), in (owner, core,
// provider, target, bridge) order.
func TestContractTeamAgentReaderAgentsLiveness(t *testing.T) {
	const now = int64(3_000_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-team-"))
		team := b.TeamAgents()
		if got := must(team.TenantTeamAgents(ctx, "", now))(t); got == nil || len(got) != 0 {
			t.Fatalf("empty tenant = %#v, want []", got)
		}
		type bridge struct {
			owner  string
			status BridgeStatus
			seen   int64
			beat   bool
			online bool
		}
		bridges := []bridge{
			{"a-live", BridgeOnline, now - 1, true, true},
			{"b-expired", BridgeOnline, now - BridgeOfflineAfterMs, true, false},
			{"c-draining", BridgeDraining, now, true, false},
			{"d-offline", BridgeOffline, now, true, false},
			{"e-noheartbeat", "", 0, false, false},
		}
		sfx := uniq("")
		for _, br := range bridges {
			owner := br.owner + sfx
			r := &bjRig{b: b, ctx: ctx, owner: owner, bridge: uniq("br-"), core: "c1"}
			if br.beat {
				r.beat(t, br.status, br.seen)
			}
			if err := b.BridgeJobs.ReplaceBridgeAgents(ctx, r.bridge, []BridgeAgent{
				{OwnerUserID: owner, Core: "c1", Provider: "codex", TargetID: "t2", LastSeenAtMs: 77},
				{OwnerUserID: owner, Core: "c1", Provider: "codex", TargetID: "t1", LastSeenAtMs: 77},
				{OwnerUserID: owner, Core: "c1", Provider: "codex", TargetID: "off", Disabled: true},
			}); err != nil {
				t.Fatal(err)
			}
		}
		got := must(team.TenantTeamAgents(ctx, "", now))(t)
		var rows []string
		for _, a := range got {
			name := strings.TrimSuffix(a.OwnerUserID, sfx)
			rows = append(rows, name+"/"+a.TargetID)
			for _, br := range bridges {
				if br.owner == name {
					seen := br.seen
					if !br.beat {
						seen = 77 // the advertisement's own last-seen
					}
					if a.Online != br.online || a.LastSeenAtMs != seen || a.Core != "c1" || a.Provider != "codex" {
						t.Fatalf("%s: %+v, want online=%v seen=%d", name, a, br.online, seen)
					}
				}
			}
		}
		want := []string{"a-live/t1", "a-live/t2", "b-expired/t1", "b-expired/t2", "c-draining/t1", "c-draining/t2", "d-offline/t1", "d-offline/t2", "e-noheartbeat/t1", "e-noheartbeat/t2"}
		if !slices.Equal(rows, want) {
			t.Fatalf("agents = %v, want %v", rows, want)
		}
		if own := must(team.TenantTeamAgents(ctx, "c-draining"+sfx, now))(t); len(own) != 2 || own[0].OwnerUserID != "c-draining"+sfx {
			t.Fatalf("one owner = %+v", own)
		}
		if other := must(team.TenantTeamAgents(tenantCtx(uniq("t-other-")), "", now))(t); len(other) != 0 {
			t.Fatalf("another tenant sees %d agents", len(other))
		}
	})
}

// TenantTeamJobs returns the recent jobs plus every still-active older one,
// newest first (the job id breaks a tie), on both backends (MySQL reads them
// as two indexed queries).
func TestContractTeamAgentReaderRecentAndActiveJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-teamjobs-"))
		room, pfx := uniq("room-"), uniq("tj-")+"-"
		const since = int64(10_000_000)
		if got := must(b.TeamAgents().TenantTeamJobs(ctx, "", since))(t); got == nil || len(got) != 0 {
			t.Fatalf("no jobs = %#v, want []", got)
		}
		put := func(id string, created int64, owner string, status AgentJobStatus) {
			t.Helper()
			if _, err := b.BridgeJobs.PutAgentJob(ctx, AgentJob{JobID: pfx + id, RoomID: room, MessageSeq: created + int64(len(id)),
				RequesterUserID: "maya", OwnerUserID: owner, Core: "acme", Provider: "codex", Status: status,
				CreatedAtMs: created, UpdatedAtMs: created}); err != nil {
				t.Fatalf("put %s: %v", id, err)
			}
		}
		put("old-queued", since-3_600_000, "maya", AgentJobQueued)
		put("old-running", since-1_000, "jack", AgentJobRunning)
		put("old-reflowing", since-900, "jack", AgentJobReflowing)
		put("old-done", since-500, "maya", AgentJobReplied)
		put("old-failed", since-400, "jack", AgentJobFailed)
		put("recent-maya", since+10, "maya", AgentJobReplied)
		put("recent-jack", since+20, "jack", AgentJobQueued)
		put("recent-jack-b", since+20, "jack", AgentJobTimedOut)
		put("at-since", since, "maya", AgentJobCancelled)

		names := func(owner string) string {
			var out []string
			for _, j := range must(b.TeamAgents().TenantTeamJobs(ctx, owner, since))(t) {
				out = append(out, strings.TrimPrefix(j.JobID, pfx))
			}
			return strings.Join(out, ",")
		}
		// recent-jack-b and recent-jack share a millisecond: the id
		// (descending) puts recent-jack-b first.
		if got, want := names(""), "recent-jack-b,recent-jack,recent-maya,at-since,old-reflowing,old-running,old-queued"; got != want {
			t.Fatalf("all owners = %s; want %s", got, want)
		}
		if got, want := names("maya"), "recent-maya,at-since,old-queued"; got != want {
			t.Fatalf("maya = %s; want %s", got, want)
		}
		if got := must(b.TeamAgents().TenantTeamJobs(tenantCtx(uniq("t-other-")), "", since))(t); len(got) != 0 {
			t.Fatalf("another tenant sees %d jobs", len(got))
		}
	})
}
