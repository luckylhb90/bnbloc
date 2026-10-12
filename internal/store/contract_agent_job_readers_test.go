package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Contracts of the bridge-job store's read sides over memory and MySQL:
// dispatches (AgentDispatchReader), run records (AgentRunReader), the room
// expert a turn wore (RoomExpertRecorder) and expert usage counts
// (ExpertUsageReader). All are tenant-scoped except BridgeOwner.

// A dispatched job keeps its parent and depth, a parent's dispatches list
// oldest first (the job id breaks a tie), and a bridge's owner resolves by
// id alone.
func TestContractAgentDispatchReaderParentChain(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		reader := b.BridgeJobs.(AgentDispatchReader)
		parent := r.put(t, 1, 100)
		for i, provider := range []string{"zeta", "claude", "codex"} {
			created := int64(101)
			if i == 0 {
				created = 102 // newest
			}
			r.put(t, int64(2+i), created, func(j *AgentJob) {
				j.JobID = provider + "-" + j.JobID
				j.Provider, j.OwnerUserID, j.DispatchedByJobID, j.DispatchDepth = provider, "jack", parent.JobID, 1
			})
		}
		got := must(reader.ListDispatchedJobs(r.ctx, parent.JobID))(t)
		var providers []string
		for _, j := range got {
			providers = append(providers, j.Provider)
			if j.DispatchedByJobID != parent.JobID || j.DispatchDepth != 1 {
				t.Fatalf("child marker = %q/%d", j.DispatchedByJobID, j.DispatchDepth)
			}
		}
		if !slices.Equal(providers, []string{"claude", "codex", "zeta"}) {
			t.Fatalf("dispatched = %v, want claude, codex (same ms, by id) then zeta", providers)
		}
		run := must(b.AgentRuns().GetAgentRun(r.ctx, got[1].JobID))(t)
		if run.DispatchedByJobID != parent.JobID || run.DispatchDepth != 1 {
			t.Fatalf("GetAgentRun = %+v; want the dispatch marker read back", run)
		}
		for _, parentID := range []string{got[0].JobID, ""} {
			if none := must(reader.ListDispatchedJobs(r.ctx, parentID))(t); none == nil || len(none) != 0 {
				t.Fatalf("dispatches of %q = %#v, want []", parentID, none)
			}
		}
		if other := must(reader.ListDispatchedJobs(tenantCtx(uniq("t-other-")), parent.JobID))(t); len(other) != 0 {
			t.Fatalf("another tenant lists %d dispatches", len(other))
		}
		r.beat(t, BridgeOnline, 1)
		if owner, err := reader.BridgeOwner(tenantCtx(uniq("t-other-")), r.bridge); err != nil || owner != r.owner {
			t.Fatalf("BridgeOwner = %q, %v; want %s from any tenant", owner, err, r.owner)
		}
		if _, err := reader.BridgeOwner(r.ctx, uniq("never-")); !errors.Is(err, ErrBridgeNotFound) {
			t.Fatalf("unknown bridge = %v; want ErrBridgeNotFound", err)
		}
	})
}

// LoadLeasedJob over a real store: the job's live lease on the caller's own
// bridge; a case variant of the owner is another member.
func TestContractAgentDispatchReaderLoadLeasedJob(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r, job := claimedRig(t, b)
		reader := b.BridgeJobs.(LeasedJobReader)
		if _, err := LoadLeasedJob(r.ctx, reader, job.JobID, job.RoomID, r.owner); err != nil {
			t.Fatalf("lease holder = %v", err)
		}
		if _, err := LoadLeasedJob(r.ctx, reader, job.JobID, job.RoomID, strings.ToUpper(r.owner)); !errors.Is(err, ErrJobNotCallers) {
			t.Fatalf("case variant of the owner = %v, want ErrJobNotCallers", err)
		}
		if _, err := LoadLeasedJob(r.ctx, reader, job.JobID, uniq("other-room-"), r.owner); !errors.Is(err, ErrJobNotInRoom) {
			t.Fatalf("another room = %v, want ErrJobNotInRoom", err)
		}
	})
}

// The run listing: filters, newest first with a same-millisecond tie broken
// by job id, cursor paging, page size, tenant isolation, and the claim
// timestamp.
func TestContractAgentRunReaderListAndGet(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		runs := b.AgentRuns()
		alice, bob := uniq("alice-"), uniq("bob-")
		put := func(name, owner, requester string, seq, created int64) AgentJob {
			return r.put(t, seq, created, func(j *AgentJob) {
				j.JobID = name + "-" + j.JobID
				j.OwnerUserID, j.RequesterUserID = owner, requester
			})
		}
		a := put("a", alice, alice, 1, 100)
		bb := put("b", alice, bob, 2, 110)
		c := put("c", bob, bob, 3, 110) // same millisecond as b: the id breaks the tie
		other := newBJRig(b)
		other.room = r.room
		x := other.put(t, 4, 120)

		want := func(q AgentRunQuery, js ...AgentJob) {
			t.Helper()
			q.RoomID = r.room
			got := must(runs.ListAgentRuns(r.ctx, q))(t)
			if got == nil {
				t.Fatalf("ListAgentRuns(%+v) = nil, want []", q)
			}
			if ids, wantIDs := jobIDs(got), jobIDs(js); !slices.Equal(ids, wantIDs) {
				t.Fatalf("ListAgentRuns(%+v) = %v, want %v", q, ids, wantIDs)
			}
		}
		want(AgentRunQuery{}, c, bb, a)
		want(AgentRunQuery{Participant: bob}, c, bb)
		want(AgentRunQuery{Owner: alice}, bb, a)
		want(AgentRunQuery{Owner: strings.ToUpper(alice)})
		want(AgentRunQuery{Core: r.core, Provider: "codex"}, c, bb, a)
		want(AgentRunQuery{Provider: "zeta"})
		want(AgentRunQuery{FromMs: 101}, c, bb)
		want(AgentRunQuery{ToMs: 110}, a)
		want(AgentRunQuery{Statuses: []AgentJobStatus{AgentJobQueued}}, c, bb, a)
		want(AgentRunQuery{Statuses: []AgentJobStatus{AgentJobReplied}})
		want(AgentRunQuery{Limit: 1}, c)
		want(AgentRunQuery{Before: AgentRunCursor{CreatedAtMs: 110, JobID: c.JobID}}, bb, a)
		want(AgentRunQuery{Before: AgentRunCursor{CreatedAtMs: 110, JobID: bb.JobID}}, a)
		if n := (AgentRunQuery{Limit: MaxAgentRunsPage + 1}).PageSize(); n != MaxAgentRunsPage {
			t.Fatalf("PageSize beyond the cap = %d", n)
		}
		if n := (AgentRunQuery{}).PageSize(); n != MaxAgentRunsPage/2 {
			t.Fatalf("default PageSize = %d", n)
		}

		if _, err := runs.GetAgentRun(r.ctx, x.JobID); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("another tenant's run = %v, want ErrAgentJobNotFound", err)
		}
		if _, err := runs.GetAgentRun(r.ctx, uniq("nope-")); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("unknown run = %v, want ErrAgentJobNotFound", err)
		}
		// Claiming stamps the claim phase on the run.
		r.owner = bob
		r.beat(t, BridgeOnline, 1000)
		r.advertise(t, "t1")
		if claimed := r.claim(t, 1000, 5); len(claimed) != 1 || claimed[0].JobID != c.JobID {
			t.Fatalf("claim = %v", jobIDs(claimed))
		}
		got := must(runs.GetAgentRun(r.ctx, c.JobID))(t)
		if got.ClaimedAtMs != 1000 || got.Status != AgentJobClaimed {
			t.Fatalf("GetAgentRun = %+v", got)
		}
	})
}

// RecordJobRoomExpert reports a change once, not a repeat, lets a later
// resolution move it, and only in the job's tenant.
func TestContractRoomExpertRecorderRecordsOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		rec := b.BridgeJobs.(RoomExpertRecorder)
		job := r.put(t, 1, 1, func(j *AgentJob) { j.ExpertID, j.ExpertVersion, j.ExpertName = "cc-chat", "1.0.0", "Reviewer" })
		house := JobExpert{ID: uniq("cc-house-"), Version: "1.2.0", Name: "House style"}
		if changed, err := rec.RecordJobRoomExpert(r.ctx, job.JobID, house); err != nil || !changed {
			t.Fatalf("first record = %v, %v; want changed", changed, err)
		}
		if changed, err := rec.RecordJobRoomExpert(r.ctx, job.JobID, house); err != nil || changed {
			t.Fatalf("repeat record = %v, %v; want unchanged", changed, err)
		}
		got := must(b.AgentRuns().GetAgentRun(r.ctx, job.JobID))(t)
		if got.RoomExpertID != house.ID || got.RoomExpertVersion != "1.2.0" || got.RoomExpertName != "House style" || got.ExpertID != "cc-chat" {
			t.Fatalf("job = %+v", got)
		}
		for _, moved := range []JobExpert{{ID: house.ID, Version: "1.3.0", Name: house.Name}, {ID: house.ID, Version: "1.3.0", Name: "Renamed"}, {}} {
			if changed, err := rec.RecordJobRoomExpert(r.ctx, job.JobID, moved); err != nil || !changed {
				t.Fatalf("moved to %+v = %v, %v; want changed", moved, changed, err)
			}
		}
		if _, err := rec.RecordJobRoomExpert(r.ctx, uniq("job-missing-"), house); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("unknown job = %v", err)
		}
		if _, err := rec.RecordJobRoomExpert(tenantCtx(uniq("t-other-")), job.JobID, house); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("other tenant = %v; a job is recorded only in its tenant", err)
		}
	})
}

// ExpertUsage counts, per expert, the ctx tenant's jobs that wore it and
// their distinct requesters; experts without jobs are absent.
func TestContractExpertUsageReaderCountsRunsAndMembers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		usage := b.BridgeJobs.(ExpertUsageReader)
		reviewer, writer, unused := uniq("cc-rev-"), uniq("cc-wri-"), uniq("cc-none-")
		put := func(seq int64, expert, requester string) {
			r.put(t, seq, seq, func(j *AgentJob) { j.ExpertID, j.ExpertVersion, j.RequesterUserID = expert, "1.0.0", requester })
		}
		put(1, reviewer, "maya")
		put(2, reviewer, "maya")
		put(3, reviewer, "jack")
		put(4, reviewer, "Maya") // a case variant is another member (D2)
		put(5, writer, "maya")
		put(6, "", "maya")
		other := newBJRig(b)
		other.put(t, 1, 1, func(j *AgentJob) { j.ExpertID = reviewer })
		got := must(usage.ExpertUsage(r.ctx, []string{reviewer, writer, unused, reviewer, ""}))(t)
		if got[reviewer] != (ContentUsage{Runs: 4, Members: 3}) || got[writer] != (ContentUsage{Runs: 1, Members: 1}) || len(got) != 2 {
			t.Fatalf("usage = %+v", got)
		}
		if none := must(usage.ExpertUsage(r.ctx, nil))(t); len(none) != 0 {
			t.Fatalf("no ids = %+v", none)
		}
	})
}
