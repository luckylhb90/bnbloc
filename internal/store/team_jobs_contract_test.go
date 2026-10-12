package store

import (
	"strings"
	"testing"
)

// TenantTeamJobs returns the recent jobs plus every still-active older one,
// newest first, on both backends (MySQL reads them as two indexed queries).
func TestContractTenantTeamJobsRecentAndActive(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-teamjobs-"))
		room := uniq("room-")
		const since = int64(10_000_000)
		put := func(id string, created int64, owner string, status AgentJobStatus) {
			t.Helper()
			if _, err := b.BridgeJobs.PutAgentJob(ctx, AgentJob{JobID: uniq(id + "-"), RoomID: room, MessageSeq: created,
				RequesterUserID: "maya", OwnerUserID: owner, Core: "acme", Provider: "codex", Status: status,
				CreatedAtMs: created, UpdatedAtMs: created}); err != nil {
				t.Fatalf("put %s: %v", id, err)
			}
		}
		put("old-queued", since-3_600_000, "maya", AgentJobQueued)
		put("old-running", since-1_000, "jack", AgentJobRunning)
		put("old-done", since-500, "maya", AgentJobReplied)
		put("old-failed", since-400, "jack", AgentJobFailed)
		put("recent-maya", since+10, "maya", AgentJobReplied)
		put("recent-jack", since+20, "jack", AgentJobQueued)
		put("at-since", since, "maya", AgentJobCancelled)

		names := func(owner string) string {
			var out []string
			for _, j := range must(b.TeamAgents().TenantTeamJobs(ctx, owner, since))(t) {
				out = append(out, j.JobID[:strings.LastIndex(j.JobID[:strings.LastIndex(j.JobID, "-")], "-")])
			}
			return strings.Join(out, ",")
		}
		if got, want := names(""), "recent-jack,recent-maya,at-since,old-running,old-queued"; got != want {
			t.Fatalf("all owners = %s; want %s", got, want)
		}
		if got, want := names("maya"), "recent-maya,at-since,old-queued"; got != want {
			t.Fatalf("maya = %s; want %s", got, want)
		}
	})
}
