package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The BridgeJobStore lifecycle contract, over memory and MySQL: create and
// idempotency, claim (live bridges only; draining, stale and missing
// heartbeats; several targets per provider; MaxJobs; FIFO), status updates,
// lease renewal, cancel, sweep, listing order, heartbeats and the owner-wide
// reads. Bridge-driven writes are cross-tenant (job and bridge ids are
// global); room-facing calls are tenant-scoped.

func bctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

// bjRig is one test's slice of a backend: its own tenant, owner, bridge and
// core, so cases never meet another test's rows in the shared database.
type bjRig struct {
	b      *Backends
	ctx    context.Context
	tid    string
	owner  string
	bridge string
	core   string
	room   string
}

func newBJRig(b *Backends) *bjRig {
	tid := uniq("t-bj-")
	return &bjRig{b: b, ctx: tenantCtx(tid), tid: tid, owner: uniq("maya-"), bridge: uniq("br-"), core: uniq("core-"), room: uniq("room-")}
}

func (r *bjRig) jobs() BridgeJobStore { return r.b.BridgeJobs }

// put enqueues a job of r.owner on (r.core, "codex") at message seq.
func (r *bjRig) put(t *testing.T, seq, created int64, edit ...func(*AgentJob)) AgentJob {
	t.Helper()
	j := AgentJob{JobID: uniq("job-"), RoomID: r.room, ChannelID: "ch", MessageSeq: seq, RequesterUserID: "jack", OwnerUserID: r.owner,
		Core: r.core, Provider: "codex", TargetID: r.core + "/codex", MessageText: "hi", CreatedAtMs: created, UpdatedAtMs: created}
	for _, e := range edit {
		e(&j)
	}
	got, err := r.jobs().PutAgentJob(r.ctx, j)
	if err != nil {
		t.Fatalf("PutAgentJob: %v", err)
	}
	return got
}

// beat records r.bridge's heartbeat.
func (r *bjRig) beat(t *testing.T, status BridgeStatus, seenAtMs int64) {
	t.Helper()
	if err := r.jobs().PutBridgeHeartbeat(r.ctx, BridgeHeartbeat{BridgeID: r.bridge, OwnerUserID: r.owner, Status: status, LastSeenAtMs: seenAtMs}); err != nil {
		t.Fatalf("PutBridgeHeartbeat: %v", err)
	}
}

// advertise replaces what r.bridge advertises: (r.core, "codex") under each
// target id.
func (r *bjRig) advertise(t *testing.T, targets ...string) {
	t.Helper()
	var ads []BridgeAgent
	for _, target := range targets {
		ads = append(ads, BridgeAgent{OwnerUserID: r.owner, Core: r.core, Provider: "codex", TargetID: target, LastSeenAtMs: 1})
	}
	if err := r.jobs().ReplaceBridgeAgents(r.ctx, r.bridge, ads); err != nil {
		t.Fatalf("ReplaceBridgeAgents: %v", err)
	}
}

func (r *bjRig) claim(t *testing.T, nowMs int64, maxJobs int) []AgentJob {
	t.Helper()
	got, err := r.jobs().ClaimAgentJobs(r.ctx, ClaimRequest{BridgeID: r.bridge, OwnerUserID: r.owner, MaxJobs: maxJobs, NowMs: nowMs, LeaseMs: 1000})
	if err != nil {
		t.Fatalf("ClaimAgentJobs: %v", err)
	}
	if got == nil {
		t.Fatal("ClaimAgentJobs returned nil; an empty claim is [] (it is serialized)")
	}
	return got
}

// claimLive heartbeats r.bridge at nowMs, then claims: a retry delay
// outlasts a heartbeat's liveness.
func (r *bjRig) claimLive(t *testing.T, nowMs int64, maxJobs int) []AgentJob {
	t.Helper()
	r.beat(t, BridgeOnline, nowMs)
	return r.claim(t, nowMs, maxJobs)
}

// claimedRig is a rig whose one job is claimed by its live bridge at 20
// with a 1000ms lease.
func claimedRig(t *testing.T, b *Backends) (*bjRig, AgentJob) {
	t.Helper()
	r := newBJRig(b)
	r.put(t, 1, 1)
	r.beat(t, BridgeOnline, 10)
	r.advertise(t, "t1")
	got := r.claim(t, 20, 1)
	if len(got) != 1 {
		t.Fatalf("setup claim = %d jobs", len(got))
	}
	return r, got[0]
}

func jobIDs(js []AgentJob) []string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.JobID
	}
	return out
}

func TestContractBridgeJobStorePutIsIdempotentByOrigin(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		first := r.put(t, 42, 100, func(j *AgentJob) {
			j.Model, j.ReasoningEffort, j.AttachmentIDs = "gpt-5.5", "high", []string{"att-1"}
			j.ExpertID, j.ExpertVersion, j.ExpertName = "cc-rev", "1.0.0", "Reviewer"
			j.FileRef = &wire.FileRef{ArtifactID: "artf_deck", Version: 3, Name: "deck.pptx"}
		})
		if first.Status != AgentJobQueued || first.TenantID != r.tid || first.UpdatedAtMs != 100 {
			t.Fatalf("stored job = %+v; want queued in the ctx tenant, updated = created", first)
		}
		// Same origin, another id: the stored job comes back unchanged.
		again := r.put(t, 42, 200, func(j *AgentJob) { j.MessageText = "changed" })
		if again.JobID != first.JobID || again.MessageText != "hi" || again.CreatedAtMs != 100 {
			t.Fatalf("re-put = %+v; want the first job %s unchanged", again, first.JobID)
		}
		got, err := b.AgentRuns().GetAgentRun(r.ctx, first.JobID)
		if err != nil || got.Model != "gpt-5.5" || got.ReasoningEffort != "high" || !slices.Equal(got.AttachmentIDs, []string{"att-1"}) ||
			got.TargetID != r.core+"/codex" || got.ExpertName != "Reviewer" || got.ChannelID != "ch" || got.RequesterUserID != "jack" ||
			got.FileRef == nil || *got.FileRef != (wire.FileRef{ArtifactID: "artf_deck", Version: 3, Name: "deck.pptx"}) {
			t.Fatalf("read back = %+v, %v", got, err)
		}
		// The id is global: another origin (in this tenant or another) cannot take it.
		if _, err := r.jobs().PutAgentJob(r.ctx, AgentJob{JobID: first.JobID, RoomID: r.room, MessageSeq: 43, OwnerUserID: r.owner, Core: r.core, Provider: "codex", CreatedAtMs: 1}); !errors.Is(err, ErrAgentJobIDTaken) {
			t.Fatalf("put of a taken id = %v, want ErrAgentJobIDTaken", err)
		}
		if _, err := r.jobs().PutAgentJob(tenantCtx(uniq("t-other-")), AgentJob{JobID: first.JobID, RoomID: r.room, MessageSeq: 42, OwnerUserID: r.owner, Core: r.core, Provider: "codex", CreatedAtMs: 1}); !errors.Is(err, ErrAgentJobIDTaken) {
			t.Fatalf("put of a taken id from another tenant = %v, want ErrAgentJobIDTaken", err)
		}
		// Subjects are case-sensitive (D2): a case-variant owner is another
		// origin.
		if got := r.put(t, 42, 300, func(j *AgentJob) { j.OwnerUserID = strings.ToUpper(r.owner) }); got.JobID == first.JobID {
			t.Fatal("a case-variant owner reused maya's job")
		}
		// A job with no timestamp gets the server clock, so it is not
		// "queued since the epoch".
		before := time.Now().UnixMilli()
		noClock := r.put(t, 45, 0)
		if noClock.CreatedAtMs < before || noClock.UpdatedAtMs != noClock.CreatedAtMs {
			t.Fatalf("job without a timestamp = created %d updated %d, want server time >= %d", noClock.CreatedAtMs, noClock.UpdatedAtMs, before)
		}
	})
}

func TestContractBridgeJobStoreListAgentJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		if got := must(r.jobs().ListAgentJobs(r.ctx, r.room, "", 0))(t); got == nil || len(got) != 0 {
			t.Fatalf("empty room = %#v, want []", got)
		}
		a := r.put(t, 1, 10, func(j *AgentJob) { j.JobID = "a-" + j.JobID })
		bb := r.put(t, 2, 20, func(j *AgentJob) { j.JobID = "b-" + j.JobID; j.ChannelID = "other" })
		c := r.put(t, 3, 20, func(j *AgentJob) { j.JobID = "c-" + j.JobID })
		// Newest first; the same millisecond breaks on the job id, descending.
		if got := jobIDs(must(r.jobs().ListAgentJobs(r.ctx, r.room, "", 0))(t)); !slices.Equal(got, []string{c.JobID, bb.JobID, a.JobID}) {
			t.Fatalf("list = %v", got)
		}
		if got := jobIDs(must(r.jobs().ListAgentJobs(r.ctx, r.room, "ch", 0))(t)); !slices.Equal(got, []string{c.JobID, a.JobID}) {
			t.Fatalf("channel list = %v", got)
		}
		if got := jobIDs(must(r.jobs().ListAgentJobs(r.ctx, r.room, "", 2))(t)); !slices.Equal(got, []string{c.JobID, bb.JobID}) {
			t.Fatalf("limit 2 = %v, want the newest two", got)
		}
		if got := must(r.jobs().ListAgentJobs(tenantCtx(uniq("t-other-")), r.room, "", 0))(t); len(got) != 0 {
			t.Fatalf("another tenant lists %v", jobIDs(got))
		}
		// A limit beyond the cap reads MaxAgentJobsListed.
		for i := int64(0); i < MaxAgentJobsListed; i++ {
			r.put(t, 100+i, 100+i)
		}
		if got := must(r.jobs().ListAgentJobs(r.ctx, r.room, "", MaxAgentJobsListed+50))(t); len(got) != MaxAgentJobsListed {
			t.Fatalf("limit beyond the cap = %d jobs, want %d", len(got), MaxAgentJobsListed)
		}
		if got := must(r.jobs().ListAgentJobs(r.ctx, r.room, "", 0))(t); len(got) != DefaultAgentJobsListed {
			t.Fatalf("default limit = %d jobs, want %d", len(got), DefaultAgentJobsListed)
		}
	})
}

// A claim leases the oldest claimable jobs to a live bridge of the claiming
// owner, at most MaxJobs, however many targets the bridge advertises.
func TestContractBridgeJobStoreClaimOldestFirstAcrossTargets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		j3 := r.put(t, 3, 30)
		j1 := r.put(t, 1, 10)
		j2a := r.put(t, 2, 20, func(j *AgentJob) { j.JobID = "a-" + j.JobID })
		j2b := r.put(t, 4, 20, func(j *AgentJob) { j.JobID = "b-" + j.JobID })
		r.beat(t, BridgeOnline, 100)
		// Several targets on one core/provider: each job is still claimed
		// once and counts once against MaxJobs.
		r.advertise(t, "codex", "claude-code", "opencode")
		got := r.claim(t, 110, 3)
		if ids := jobIDs(got); !slices.Equal(ids, []string{j1.JobID, j2a.JobID, j2b.JobID}) {
			t.Fatalf("claimed %v, want the three oldest (id breaks the tie) %v", ids, []string{j1.JobID, j2a.JobID, j2b.JobID})
		}
		for _, j := range got {
			if j.Status != AgentJobClaimed || j.Attempt != 1 || j.LeaseBridgeID != r.bridge || j.LeaseExpiresAtMs != 1110 || j.ClaimedAtMs != 110 || j.UpdatedAtMs != 110 || j.TenantID != r.tid {
				t.Fatalf("claimed job = %+v", j)
			}
		}
		// MaxJobs 0 means one.
		if got := r.claim(t, 120, 0); len(got) != 1 || got[0].JobID != j3.JobID {
			t.Fatalf("MaxJobs 0 claimed %v, want %s", jobIDs(got), j3.JobID)
		}
		if got := r.claim(t, 130, 5); len(got) != 0 {
			t.Fatalf("nothing left, claimed %v", jobIDs(got))
		}
		// The prompt payload travels with the claim.
		run := must(b.AgentRuns().GetAgentRun(r.ctx, j1.JobID))(t)
		if run.MessageText != "hi" || run.Status != AgentJobClaimed {
			t.Fatalf("claimed run = %+v", run)
		}
	})
}

// Only a live bridge claims: online and seen less than BridgeOfflineAfterMs
// ago (BridgeLive). Draining, offline, stale and missing heartbeats claim
// nothing, on both backends.
func TestContractBridgeJobStoreClaimNeedsALiveBridge(t *testing.T) {
	const now = int64(1_000_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		for _, tc := range []struct {
			name   string
			status BridgeStatus
			seen   int64
			beat   bool
			want   int
		}{
			{"online", BridgeOnline, now, true, 1},
			{"just inside the window", BridgeOnline, now - BridgeOfflineAfterMs + 1, true, 1},
			{"expired at the window", BridgeOnline, now - BridgeOfflineAfterMs, true, 0},
			{"draining", BridgeDraining, now, true, 0},
			{"offline", BridgeOffline, now, true, 0},
			{"no heartbeat", "", 0, false, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := newBJRig(b)
				r.put(t, 1, 1)
				if tc.beat {
					r.beat(t, tc.status, tc.seen)
				}
				r.advertise(t, "t1")
				if got := r.claim(t, now, 1); len(got) != tc.want {
					t.Fatalf("claimed %d, want %d", len(got), tc.want)
				}
				if got := must(r.jobs().ListBridgeAgentsForOwner(r.ctx, r.owner, now))(t); len(got) != tc.want {
					t.Fatalf("listed %d advertised agents, want %d", len(got), tc.want)
				}
			})
		}
	})
}

// Who may claim what: the bridge must be the claimer's, its advertisement
// must be enabled, and another owner's job needs ServeAllOwners.
func TestContractBridgeJobStoreClaimOwnership(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		jacks := r.put(t, 1, 1, func(j *AgentJob) { j.OwnerUserID = uniq("jack-") })
		r.beat(t, BridgeOnline, 10)
		r.advertise(t, "t1")
		// Through someone else's bridge: nothing, even serving all owners.
		if got := must(r.jobs().ClaimAgentJobs(r.ctx, ClaimRequest{BridgeID: r.bridge, OwnerUserID: "mallory", MaxJobs: 5, NowMs: 20, ServeAllOwners: true}))(t); len(got) != 0 {
			t.Fatalf("foreign-bridge claim = %v", jobIDs(got))
		}
		if got := r.claim(t, 20, 5); len(got) != 0 {
			t.Fatalf("claimed another owner's job without ServeAllOwners: %v", jobIDs(got))
		}
		got := must(r.jobs().ClaimAgentJobs(r.ctx, ClaimRequest{BridgeID: r.bridge, OwnerUserID: r.owner, MaxJobs: 5, NowMs: 20, ServeAllOwners: true}))(t)
		if len(got) != 1 || got[0].JobID != jacks.JobID {
			t.Fatalf("serve-all claim = %v, want %s", jobIDs(got), jacks.JobID)
		}
		// A disabled advertisement drives nothing.
		r.put(t, 2, 2)
		if err := r.jobs().ReplaceBridgeAgents(r.ctx, r.bridge, []BridgeAgent{{OwnerUserID: r.owner, Core: r.core, Provider: "codex", TargetID: "t1", Disabled: true}}); err != nil {
			t.Fatal(err)
		}
		if got := r.claim(t, 30, 5); len(got) != 0 {
			t.Fatalf("disabled agent claimed %v", jobIDs(got))
		}
		// Claim and capability are cross-tenant: a job of the room's tenant is
		// claimed by the owner's bridge heartbeating in another tenant, and
		// carries its own tenant.
		r2 := newBJRig(b)
		job := r2.put(t, 1, 1)
		r2.ctx = tenantCtx(uniq("t-session-"))
		r2.beat(t, BridgeOnline, 10)
		r2.advertise(t, "t1")
		if got := r2.claim(t, 20, 1); len(got) != 1 || got[0].JobID != job.JobID || got[0].TenantID != r2.tid {
			t.Fatalf("cross-tenant claim = %+v", got)
		}
	})
}

// A job whose lease lapsed is not claimable as it stands: the sweep puts it
// back on the queue once the lease expired (at now == expiry, liveAt, not
// before), recording the lost attempt as lease_expired, and the next claim
// is attempt 2.
func TestContractBridgeJobStoreLapsedLeaseRequeuedBySweep(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r, job := claimedRig(t, b) // lease expires at 1020
		other := &bjRig{b: b, ctx: r.ctx, tid: r.tid, owner: r.owner, bridge: uniq("br2-"), core: r.core, room: r.room}
		other.beat(t, BridgeOnline, 1000)
		other.advertise(t, "t1")
		if got := other.claim(t, 1020, 1); len(got) != 0 {
			t.Fatalf("claimed a lapsed job the sweep has not requeued: %v", jobIDs(got))
		}
		if _, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: 1019})[job.JobID]; ok {
			t.Fatal("swept a job whose lease is live")
		}
		requeued, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: 1020})[job.JobID]
		want := []AgentJobAttempt{{Attempt: 1, Reason: FailureLeaseExpired, AtMs: 1020}}
		if !ok || requeued.Status != AgentJobQueued || requeued.FailureReason != FailureLeaseExpired || requeued.LeaseBridgeID != "" || requeued.FinishedAtMs != 0 || !slices.Equal(requeued.Attempts, want) || requeued.NotBeforeMs != 1020+5000 {
			t.Fatalf("lapsed lease at expiry = %+v (swept %v)", requeued, ok)
		}
		if got := must(b.AgentRuns().GetAgentRun(r.ctx, job.JobID))(t); got.Status != AgentJobQueued || !slices.Equal(got.Attempts, want) {
			t.Fatalf("stored requeue = %+v", got)
		}
		if got := other.claim(t, 1020, 1); len(got) != 0 {
			t.Fatalf("claimed a requeued job inside its retry delay: %v", jobIDs(got))
		}
		got := other.claimLive(t, requeued.NotBeforeMs, 1)
		if len(got) != 1 || got[0].JobID != job.JobID || got[0].Attempt != 2 || got[0].LeaseBridgeID != other.bridge || !slices.Equal(got[0].Attempts, want) {
			t.Fatalf("reclaim after the requeue = %+v", got)
		}
		// The superseded bridge can no longer report or renew.
		if _, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobReplied, NowMs: 1030}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("superseded report = %v", err)
		}
		if err := r.jobs().RenewAgentJobLease(r.ctx, LeaseRenewal{JobID: job.JobID, BridgeID: r.bridge, NowMs: 1030}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("superseded renewal = %v", err)
		}
	})
}

// Concurrent claims of one job produce exactly one winner.
func TestContractBridgeJobStoreConcurrentClaimYieldsOneWinner(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		r.put(t, 1, 1)
		var bridges []string
		for i := 0; i < 3; i++ {
			br := &bjRig{b: b, ctx: r.ctx, owner: r.owner, bridge: uniq("race-"), core: r.core}
			br.beat(t, BridgeOnline, 10)
			br.advertise(t, "t1")
			bridges = append(bridges, br.bridge)
		}
		var mu sync.Mutex
		var winners []string
		var wg sync.WaitGroup
		for _, id := range bridges {
			for range 3 {
				wg.Add(1)
				go func(bridgeID string) {
					defer wg.Done()
					got, err := r.jobs().ClaimAgentJobs(r.ctx, ClaimRequest{BridgeID: bridgeID, OwnerUserID: r.owner, MaxJobs: 5, NowMs: 20})
					if err != nil {
						return // lock contention is acceptable; a lost claim is not a win
					}
					mu.Lock()
					defer mu.Unlock()
					for _, j := range got {
						winners = append(winners, j.LeaseBridgeID)
					}
				}(id)
			}
		}
		wg.Wait()
		if len(winners) != 1 {
			t.Fatalf("job claimed %d times by %v, want exactly 1", len(winners), winners)
		}
	})
}

func TestContractBridgeJobStoreUpdateStatus(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r, job := claimedRig(t, b)
		upd := func(ctx context.Context, u JobStatusUpdate) (AgentJob, error) {
			u.JobID = job.JobID
			return r.jobs().UpdateAgentJobStatus(ctx, u)
		}
		if _, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: uniq("nope-"), BridgeID: r.bridge, Status: AgentJobRunning, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("unknown job = %v, want ErrAgentJobNotFound", err)
		}
		if _, err := upd(r.ctx, JobStatusUpdate{BridgeID: "someone-else", Status: AgentJobRunning, NowMs: 30}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("another bridge = %v, want ErrAgentJobLeaseMismatch", err)
		}
		// Bridge reports are cross-tenant: the bridge's session tenant may not
		// be the job's. LeaseMs 0 renews by DefaultLeaseMs.
		got, err := upd(tenantCtx(uniq("t-session-")), JobStatusUpdate{BridgeID: r.bridge, Status: AgentJobRunning, NowMs: 30})
		if err != nil || got.Status != AgentJobRunning || got.StartedAtMs != 30 || got.LeaseExpiresAtMs != 30+DefaultLeaseMs || got.UpdatedAtMs != 30 {
			t.Fatalf("running = %+v, %v", got, err)
		}
		got, err = upd(r.ctx, JobStatusUpdate{BridgeID: r.bridge, Status: AgentJobReflowing, NowMs: 40, LeaseMs: 500, DiagnosticsJSON: []byte(`{"phase":"reflow"}`)})
		if err != nil || got.StartedAtMs != 30 || got.LeaseExpiresAtMs != 540 || !strings.Contains(string(got.DiagnosticsJSON), "reflow") {
			t.Fatalf("reflowing = %+v, %v; want the first start kept and the lease renewed", got, err)
		}
		got, err = upd(r.ctx, JobStatusUpdate{BridgeID: r.bridge, Status: AgentJobReplied, NowMs: 50})
		if err != nil || got.Status != AgentJobReplied || got.FinishedAtMs != 50 || got.LeaseExpiresAtMs != 0 || got.RoomID != r.room {
			t.Fatalf("replied = %+v, %v", got, err)
		}
		// A terminal job is final, even for its own bridge.
		if _, err := upd(r.ctx, JobStatusUpdate{BridgeID: r.bridge, Status: AgentJobFailed, NowMs: 60}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("rewrite of a terminal job = %v", err)
		}
	})
}

// A cause the next attempt could survive goes back on the queue (lease
// released, claimable again); one it cannot is terminal at once, and the
// attempt budget bounds retries.
func TestContractBridgeJobStoreRetryClassification(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		for _, tc := range []struct {
			name        string
			reason      AgentJobFailureReason
			maxAttempts int
			want        AgentJobStatus
		}{
			{"runner start failure retries", FailureRunnerStartFailed, 0, AgentJobQueued},
			{"core unavailable retries", FailureCoreUnavailable, 0, AgentJobQueued},
			{"a lapsed lease retries", FailureLeaseExpired, 0, AgentJobQueued},
			{"a starting sandbox retries", FailureSandboxStarting, 0, AgentJobQueued},
			{"an unreadable budget retries", FailureBudgetUnavailable, 0, AgentJobQueued},
			{"a started turn is final", FailureRunnerFailed, 0, AgentJobFailed},
			{"no reply is final", FailureReplyTimeout, 0, AgentJobFailed},
			{"unauthorized borrow is final", FailureBorrowNotAuthorized, 0, AgentJobFailed},
			{"unadvertised agent is final", FailureAgentNotAdvertised, 0, AgentJobFailed},
			{"retryable at the attempt limit is final", FailureRunnerStartFailed, 1, AgentJobFailed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r, job := claimedRig(t, b)
				got, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobFailed, FailureReason: tc.reason, NowMs: 30, MaxAttempts: tc.maxAttempts})
				if err != nil || got.Status != tc.want || got.FailureReason != tc.reason {
					t.Fatalf("after %s = %+v, %v; want %s", tc.reason, got, err, tc.want)
				}
				again := r.claim(t, 40, 1)
				if tc.want == AgentJobQueued {
					if got.NotBeforeMs <= 30 || len(again) != 0 {
						t.Fatalf("requeued job claimable at once: not before %d, re-claimed %d", got.NotBeforeMs, len(again))
					}
					again = r.claimLive(t, got.NotBeforeMs, 1)
					if got.LeaseBridgeID != "" || got.LeaseExpiresAtMs != 0 || got.FinishedAtMs != 0 {
						t.Fatalf("requeued job still leased or finished: %+v", got)
					}
					if want := []AgentJobAttempt{{Attempt: 1, Reason: tc.reason, AtMs: 30}}; !slices.Equal(got.Attempts, want) {
						t.Fatalf("requeue recorded %+v, want %+v", got.Attempts, want)
					}
					if len(again) != 1 {
						t.Fatal("a requeued job was not claimable again")
					}
				} else if len(again) != 0 || got.FinishedAtMs != 30 || len(got.Attempts) != 0 {
					t.Fatalf("final job: re-claimed %d, finished %d, attempts %+v", len(again), got.FinishedAtMs, got.Attempts)
				}
			})
		}
	})
}

// Every requeue records its attempt (a bridge's retryable report and the
// sweep's lapsed lease alike), the newest MaxRecordedAttempts are kept, and
// a terminal failure keeps the history.
func TestContractBridgeJobStoreAttemptHistory(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		job := r.put(t, 1, 1)
		r.beat(t, BridgeOnline, 10)
		r.advertise(t, "t1")
		const maxAttempts = MaxRecordedAttempts + 5
		var want []AgentJobAttempt
		now := int64(20)
		for attempt := 1; attempt <= MaxRecordedAttempts+2; attempt++ {
			got := r.claimLive(t, now, 1)
			if len(got) != 1 || got[0].Attempt != attempt {
				t.Fatalf("claim of attempt %d = %+v", attempt, got)
			}
			reason := []AgentJobFailureReason{FailureSandboxStarting, FailureCoreUnavailable, FailureRunnerStartFailed}[attempt%3]
			if attempt == 3 {
				// The lease lapses instead: the sweep requeues it.
				now = got[0].LeaseExpiresAtMs
				reason = FailureLeaseExpired
				if _, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: now, MaxAttempts: maxAttempts})[job.JobID]; !ok {
					t.Fatal("the lapsed attempt was not swept")
				}
			} else if _, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobFailed, FailureReason: reason, NowMs: now, MaxAttempts: maxAttempts}); err != nil {
				t.Fatal(err)
			}
			want = append(want, AgentJobAttempt{Attempt: attempt, Reason: reason, AtMs: now})
			if len(want) > MaxRecordedAttempts {
				want = want[1:]
			}
			stored := must(b.AgentRuns().GetAgentRun(r.ctx, job.JobID))(t)
			if stored.Status != AgentJobQueued || !slices.Equal(stored.Attempts, want) {
				t.Fatalf("after attempt %d: %s %+v, want queued %+v", attempt, stored.Status, stored.Attempts, want)
			}
			now = stored.NotBeforeMs
		}
		if len(want) != MaxRecordedAttempts || want[0].Attempt != 3 {
			t.Fatalf("test drove %+v; want the cap reached with the oldest dropped", want)
		}
		r.claimLive(t, now, 1)
		final, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobFailed, FailureReason: FailureRunnerFailed, NowMs: now + 20, MaxAttempts: maxAttempts})
		if err != nil || final.Status != AgentJobFailed || !slices.Equal(final.Attempts, want) {
			t.Fatalf("terminal failure = %+v, %v; want the history %+v kept", final, err, want)
		}
		listed, err := r.jobs().ListAgentJobs(r.ctx, r.room, "", 0)
		if err != nil || len(listed) != 1 || !slices.Equal(listed[0].Attempts, want) {
			t.Fatalf("listed = %+v, %v", listed, err)
		}
	})
}

// A requeued job waits out its retry delay (retryBackoffs: per reason, a
// base that doubles each attempt, capped) before any bridge may claim it:
// not a millisecond before, at once after. A booting sandbox thus gets
// 15 s, 30 s, … instead of every attempt in a few seconds.
func TestContractBridgeJobStoreRetryDelay(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		job := r.put(t, 1, 1)
		r.advertise(t, "t1")
		now := int64(20)
		for _, step := range []struct {
			reason AgentJobFailureReason
			delay  int64
		}{
			{FailureSandboxStarting, 15_000},
			{FailureSandboxStarting, 30_000},
			{FailureCoreUnavailable, 40_000}, // attempt 3: 10 s doubled twice
			{FailureSandboxStarting, 60_000}, // attempt 4: capped at a minute
		} {
			got := r.claimLive(t, now, 1)
			if len(got) != 1 {
				t.Fatalf("claim at %d = %+v", now, got)
			}
			requeued, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobFailed, FailureReason: step.reason, NowMs: now + 10, MaxAttempts: 10})
			if err != nil || requeued.Status != AgentJobQueued || requeued.NotBeforeMs != now+10+step.delay {
				t.Fatalf("attempt %d (%s) requeued = not before %d, %v; want %d", got[0].Attempt, step.reason, requeued.NotBeforeMs, err, now+10+step.delay)
			}
			if stored := must(b.AgentRuns().GetAgentRun(r.ctx, job.JobID))(t); stored.NotBeforeMs != requeued.NotBeforeMs {
				t.Fatalf("stored not before %d, returned %d", stored.NotBeforeMs, requeued.NotBeforeMs)
			}
			if early := r.claimLive(t, requeued.NotBeforeMs-1, 1); len(early) != 0 {
				t.Fatal("claimed 1ms before the retry delay ended")
			}
			now = requeued.NotBeforeMs
		}
		if got := r.claimLive(t, now, 1); len(got) != 1 || got[0].Attempt != 5 {
			t.Fatalf("claim when the delay ends = %+v", got)
		}
		// A lapsed lease waits 5 s.
		lapsed, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: now + 1000, MaxAttempts: 10})[job.JobID]
		if !ok || lapsed.Status != AgentJobQueued || lapsed.NotBeforeMs != now+1000+5000 {
			t.Fatalf("lapsed lease requeued = %+v (swept %v)", lapsed, ok)
		}
		if got := r.claimLive(t, lapsed.NotBeforeMs-1, 1); len(got) != 0 {
			t.Fatal("claimed a lapsed job inside its retry delay")
		}
		if got := r.claimLive(t, lapsed.NotBeforeMs, 1); len(got) != 1 {
			t.Fatal("a lapsed job was not claimable when its delay ended")
		}
	})
}

func TestContractBridgeJobStoreRenewLease(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r, job := claimedRig(t, b) // lease expires at 1020
		if err := r.jobs().RenewAgentJobLease(r.ctx, LeaseRenewal{JobID: uniq("nope-"), BridgeID: r.bridge, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("unknown job = %v", err)
		}
		if err := r.jobs().RenewAgentJobLease(r.ctx, LeaseRenewal{JobID: job.JobID, BridgeID: "someone-else", NowMs: 30}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("another bridge = %v", err)
		}
		// Cross-tenant like the status report; twice in one millisecond is the same lease.
		for i := 0; i < 2; i++ {
			if err := r.jobs().RenewAgentJobLease(tenantCtx(uniq("t-session-")), LeaseRenewal{JobID: job.JobID, BridgeID: r.bridge, NowMs: 900, LeaseMs: 1000}); err != nil {
				t.Fatalf("renew #%d: %v", i, err)
			}
		}
		other := &bjRig{b: b, ctx: r.ctx, owner: r.owner, bridge: uniq("br2-"), core: r.core}
		other.beat(t, BridgeOnline, 1500)
		other.advertise(t, "t1")
		if _, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: 1500})[job.JobID]; ok {
			t.Fatal("swept a renewed job")
		}
		if got := other.claim(t, 1500, 1); len(got) != 0 {
			t.Fatalf("renewed job re-claimed: %v", jobIDs(got))
		}
		requeued := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: 1900})[job.JobID]
		if got := other.claimLive(t, requeued.NotBeforeMs, 1); len(got) != 1 {
			t.Fatal("a lapsed renewal was not re-claimable after the sweep")
		}
		// LeaseMs 0 renews by DefaultLeaseMs.
		renewAt := requeued.NotBeforeMs + 100
		if err := r.jobs().RenewAgentJobLease(r.ctx, LeaseRenewal{JobID: job.JobID, BridgeID: other.bridge, NowMs: renewAt}); err != nil {
			t.Fatal(err)
		}
		if got := must(b.AgentRuns().GetAgentRun(r.ctx, job.JobID))(t); got.LeaseExpiresAtMs != renewAt+DefaultLeaseMs || got.UpdatedAtMs != renewAt {
			t.Fatalf("default renewal = %+v", got)
		}
	})
}

func TestContractBridgeJobStoreCancel(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r, job := claimedRig(t, b)
		if _, err := r.jobs().CancelAgentJob(r.ctx, CancelRequest{JobID: job.JobID, RoomID: "other-room", NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("cancel across rooms = %v, want ErrAgentJobNotFound", err)
		}
		if _, err := r.jobs().CancelAgentJob(tenantCtx(uniq("t-other-")), CancelRequest{JobID: job.JobID, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("cancel from another tenant = %v, want ErrAgentJobNotFound", err)
		}
		got, err := r.jobs().CancelAgentJob(r.ctx, CancelRequest{JobID: job.JobID, RoomID: r.room, RequestedByUser: "jack", NowMs: 30})
		if err != nil || got.Status != AgentJobCancelled || got.FailureReason != FailureCancelled || got.FinishedAtMs != 30 || got.LeaseBridgeID != "" || got.LeaseExpiresAtMs != 0 {
			t.Fatalf("cancelled = %+v, %v", got, err)
		}
		// The worker is locked out, and a queued job cancels without a room.
		if _, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: r.bridge, Status: AgentJobReplied, NowMs: 40}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("worker report after cancel = %v", err)
		}
		queued := r.put(t, 2, 2)
		if got, err := r.jobs().CancelAgentJob(r.ctx, CancelRequest{JobID: queued.JobID, NowMs: 50}); err != nil || got.Status != AgentJobCancelled {
			t.Fatalf("cancel of a queued job = %+v, %v", got, err)
		}
	})
}

// sweepAll sweeps at nowMs until a pass fails nothing (the shared database
// may hold other tests' stuck jobs) and returns every job failed, by id.
func sweepAll(t *testing.T, s BridgeJobStore, req SweepRequest) map[string]AgentJob {
	t.Helper()
	out := map[string]AgentJob{}
	for pass := 0; ; pass++ {
		got, err := s.SweepAgentJobs(context.Background(), req)
		if err != nil {
			t.Fatalf("SweepAgentJobs: %v", err)
		}
		if len(got) > MaxSweptJobs {
			t.Fatalf("one sweep failed %d jobs, more than MaxSweptJobs", len(got))
		}
		if len(got) == 0 || pass > 1000 {
			return out
		}
		for _, j := range got {
			out[j.JobID] = j
		}
	}
}

func TestContractBridgeJobStoreSweep(t *testing.T) {
	const now = int64(500_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		// Queued jobs: failed once they waited out the queue timeout.
		q := newBJRig(b)
		stuck := q.put(t, 1, now-120_000)   // queued exactly the timeout
		fresh := q.put(t, 2, now-120_000+1) // a millisecond short of it
		// Live jobs whose lease lapsed: requeued while attempts remain,
		// failed on their last attempt with the history kept.
		l := newBJRig(b)
		last := l.put(t, 1, 10)
		l.beat(t, BridgeOnline, 100)
		l.advertise(t, "t1")
		l.claim(t, 100, 5) // attempt 1, lease to 1100
		sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: 1100, MaxAttempts: 2})
		l.claimLive(t, 6100, 5) // requeued, claimable after 5s: attempt 2, lease to 7100
		retry := l.put(t, 2, 6200)
		l.claim(t, 6200, 5) // attempt 1, lease to 7200

		swept := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: now, QueuedTimeoutMs: 120_000, MaxAttempts: 2})
		if j, ok := swept[stuck.JobID]; !ok || j.Status != AgentJobFailed || j.FailureReason != FailureNoBridgeOnline || j.FinishedAtMs != now || j.UpdatedAtMs != now {
			t.Fatalf("queued past the timeout = %+v (swept %v)", j, ok)
		}
		lost := []AgentJobAttempt{{Attempt: 1, Reason: FailureLeaseExpired, AtMs: 1100}}
		if j, ok := swept[last.JobID]; !ok || j.Status != AgentJobFailed || j.FailureReason != FailureLeaseExpired || j.LeaseExpiresAtMs != 0 || j.Attempt != 2 || !slices.Equal(j.Attempts, lost) {
			t.Fatalf("lapsed lease on the last attempt = %+v (swept %v)", j, ok)
		}
		if j, ok := swept[retry.JobID]; !ok || j.Status != AgentJobQueued || j.FailureReason != FailureLeaseExpired || j.UpdatedAtMs != now || j.LeaseBridgeID != "" {
			t.Fatalf("lapsed lease with attempts left = %+v (swept %v), want requeued", j, ok)
		}
		if _, ok := swept[fresh.JobID]; ok {
			t.Fatal("swept a job a millisecond short of the queue timeout")
		}
		if got := must(b.AgentRuns().GetAgentRun(q.ctx, stuck.JobID))(t); got.Status != AgentJobFailed || got.FailureReason != FailureNoBridgeOnline {
			t.Fatalf("stored swept job = %+v", got)
		}
		if got := must(b.AgentRuns().GetAgentRun(l.ctx, last.JobID))(t); !slices.Equal(got.Attempts, lost) {
			t.Fatalf("stored failed job's attempts = %+v, want the history kept", got.Attempts)
		}
		// A requeued job's queue time starts at the requeue: it is not failed
		// as queued-too-long for having been created long ago.
		if got := must(b.AgentRuns().GetAgentRun(l.ctx, retry.JobID))(t); got.Status != AgentJobQueued {
			t.Fatalf("requeued job = %+v, want still queued", got)
		}
		// ...and from when its retry delay ends (claimable from now+5s).
		if _, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: now + 120_000, QueuedTimeoutMs: 120_000, MaxAttempts: 2})[retry.JobID]; ok {
			t.Fatal("a requeued job's queue timeout ran during its retry delay")
		}
		if j, ok := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: now + 5_000 + 120_000, QueuedTimeoutMs: 120_000, MaxAttempts: 2})[retry.JobID]; !ok || j.FailureReason != FailureNoBridgeOnline {
			t.Fatalf("requeued job unclaimed for the queue timeout = %+v (swept %v)", j, ok)
		}
	})
}

// One sweep fails at most MaxSweptJobs, oldest first; the next pass takes
// the rest.
func TestContractBridgeJobStoreSweepIsBounded(t *testing.T) {
	const now = int64(600_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		mine := map[string]bool{}
		for i := int64(0); i <= MaxSweptJobs; i++ {
			mine[r.put(t, i+1, 1).JobID] = true
		}
		req := SweepRequest{NowMs: now, QueuedTimeoutMs: 1000}
		first, err := b.BridgeJobs.SweepAgentJobs(context.Background(), req)
		if err != nil || len(first) != MaxSweptJobs {
			t.Fatalf("first sweep failed %d jobs, %v; want MaxSweptJobs (%d)", len(first), err, MaxSweptJobs)
		}
		for i := 1; i < len(first); i++ {
			if a, c := first[i-1], first[i]; a.CreatedAtMs > c.CreatedAtMs || (a.CreatedAtMs == c.CreatedAtMs && a.JobID > c.JobID) {
				t.Fatalf("sweep order: %s (%d) before %s (%d)", a.JobID, a.CreatedAtMs, c.JobID, c.CreatedAtMs)
			}
		}
		rest := sweepAll(t, b.BridgeJobs, req)
		for _, j := range first {
			rest[j.JobID] = j
		}
		for id := range mine {
			if _, ok := rest[id]; !ok {
				t.Fatalf("job %s never swept", id)
			}
		}
	})
}

// A job enqueued without a timestamp is new, not "queued since the epoch".
func TestContractBridgeJobStoreSweepKeepsAJobWithoutTimestamp(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		job := r.put(t, 1, 0)
		swept := sweepAll(t, b.BridgeJobs, SweepRequest{NowMs: job.CreatedAtMs + 1000})
		if _, ok := swept[job.JobID]; ok {
			t.Fatal("swept a job queued 1s ago")
		}
	})
}

// A bridge id is bound to its first owner; BridgeOwner answers for it from
// any tenant, and the heartbeat moves the bridge to the reporting tenant.
func TestContractBridgeJobStoreHeartbeatsAndOwner(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		r.beat(t, BridgeOnline, 10)
		if err := r.jobs().PutBridgeHeartbeat(r.ctx, BridgeHeartbeat{BridgeID: r.bridge, OwnerUserID: "mallory", Status: BridgeOnline, LastSeenAtMs: 11}); !errors.Is(err, ErrBridgeOwnerMismatch) {
			t.Fatalf("re-own heartbeat = %v, want ErrBridgeOwnerMismatch", err)
		}
		// The owner is case-sensitive (D2): "MAYA-…" is another member.
		if err := r.jobs().PutBridgeHeartbeat(r.ctx, BridgeHeartbeat{BridgeID: r.bridge, OwnerUserID: strings.ToUpper(r.owner), Status: BridgeOnline, LastSeenAtMs: 11}); !errors.Is(err, ErrBridgeOwnerMismatch) {
			t.Fatalf("case-variant owner heartbeat = %v, want ErrBridgeOwnerMismatch", err)
		}
		for _, ctx := range []context.Context{r.ctx, tenantCtx(uniq("t-other-")), context.Background()} {
			if owner, err := r.jobs().BridgeOwner(ctx, r.bridge); err != nil || owner != r.owner {
				t.Fatalf("BridgeOwner = %q, %v; want %s from any tenant", owner, err, r.owner)
			}
		}
		if _, err := r.jobs().BridgeOwner(r.ctx, uniq("never-")); !errors.Is(err, ErrBridgeNotFound) {
			t.Fatalf("unknown bridge = %v, want ErrBridgeNotFound", err)
		}
		for _, status := range []BridgeStatus{"", "Online", "gone"} {
			if err := r.jobs().PutBridgeHeartbeat(r.ctx, BridgeHeartbeat{BridgeID: uniq("br-bad-"), OwnerUserID: r.owner, Status: status, LastSeenAtMs: 1}); !errors.Is(err, ErrBridgeStatusInvalid) {
				t.Fatalf("heartbeat with status %q = %v, want ErrBridgeStatusInvalid", status, err)
			}
		}
		// The owner's refresh from another tenant moves the bridge there.
		moved := tenantCtx(uniq("t-moved-"))
		if err := r.jobs().PutBridgeHeartbeat(moved, BridgeHeartbeat{BridgeID: r.bridge, OwnerUserID: r.owner, Status: BridgeDraining, LastSeenAtMs: 12, AppInstanceID: "app", Version: "1.2"}); err != nil {
			t.Fatal(err)
		}
		hb, found, err := r.jobs().LatestBridgeForOwner(context.Background(), r.owner)
		if err != nil || !found || hb.TenantID != tenantOf(moved) || hb.Status != BridgeDraining || hb.LastSeenAtMs != 12 || hb.Version != "1.2" || hb.AppInstanceID != "app" {
			t.Fatalf("heartbeat after refresh = %+v, %v, %v", hb, found, err)
		}
	})
}

func tenantOf(ctx context.Context) string { id, _ := tenant.Require(ctx); return id }

// LatestBridgeForOwner is the owner's most recently seen bridge across
// tenants (the greater bridge id on a tie), and none for an owner whose
// desktop never connected.
func TestContractBridgeJobStoreLatestBridgeForOwner(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		owner := uniq("latest-")
		for i, seen := range []int64{500, 900, 100, 900} {
			ctx := tenantCtx(uniq("t-latest-"))
			if err := b.BridgeJobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: owner + "-" + string(rune('a'+i)), OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: seen}); err != nil {
				t.Fatal(err)
			}
		}
		hb, found, err := b.BridgeJobs.LatestBridgeForOwner(bctx(), owner)
		if err != nil || !found || hb.BridgeID != owner+"-d" || hb.LastSeenAtMs != 900 {
			t.Fatalf("latest = %+v, %v, %v; want %s-d (seen 900, greater id)", hb, found, err, owner)
		}
		if _, found, err := b.BridgeJobs.LatestBridgeForOwner(bctx(), uniq("never-")); err != nil || found {
			t.Fatalf("an owner with no bridge: found=%v err=%v", found, err)
		}
	})
}

// ListBridgeAgentsForOwner lists one owner's enabled agents of live bridges,
// every target, in (core, provider, target, bridge) order, from any tenant.
func TestContractBridgeJobStoreListBridgeAgentsForOwner(t *testing.T) {
	const now = int64(2_000_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		if got := must(r.jobs().ListBridgeAgentsForOwner(r.ctx, r.owner, now))(t); got == nil || len(got) != 0 {
			t.Fatalf("no agents = %#v, want []", got)
		}
		r.beat(t, BridgeOnline, now)
		if err := r.jobs().ReplaceBridgeAgents(r.ctx, r.bridge, []BridgeAgent{
			{OwnerUserID: r.owner, Core: "b-core", Provider: "codex", TargetID: "t2", ModelOptionsJSON: []byte(`{"models":["m"]}`)},
			{OwnerUserID: r.owner, Core: "b-core", Provider: "codex", TargetID: "t1"},
			{OwnerUserID: r.owner, Core: "a-core", Provider: "codex", TargetID: "t9"},
			{OwnerUserID: r.owner, Core: "c-core", Provider: "codex", TargetID: "off", Disabled: true},
			{OwnerUserID: "someone-else", Core: "a-core", Provider: "codex", TargetID: "x"},
		}); err != nil {
			t.Fatal(err)
		}
		// A second bridge of the owner, draining: not listed.
		dr := &bjRig{b: b, ctx: r.ctx, owner: r.owner, bridge: uniq("br-drain-"), core: "a-core"}
		dr.beat(t, BridgeDraining, now)
		dr.advertise(t, "t0")
		// A re-advertisement replaces the set (and is a refresh, not a failure).
		for i := 0; i < 2; i++ {
			got := must(r.jobs().ListBridgeAgentsForOwner(tenantCtx(uniq("t-any-")), r.owner, now))(t)
			var keys []string
			for _, a := range got {
				keys = append(keys, a.Core+"/"+a.TargetID)
				if a.BridgeID != r.bridge || a.TenantID != r.tid {
					t.Fatalf("agent %+v: want bridge %s in tenant %s", a, r.bridge, r.tid)
				}
			}
			if want := []string{"a-core/t9", "b-core/t1", "b-core/t2"}; !slices.Equal(keys, want) {
				t.Fatalf("listed %v, want %v", keys, want)
			}
			if !strings.Contains(string(got[2].ModelOptionsJSON), `"m"`) {
				t.Fatalf("model options = %s", got[2].ModelOptionsJSON)
			}
		}
		if got := must(r.jobs().ListBridgeAgentsForOwner(r.ctx, r.owner, now+BridgeOfflineAfterMs))(t); len(got) != 0 {
			t.Fatalf("agents of an expired bridge = %d", len(got))
		}
		r.advertise(t)
		if got := must(r.jobs().ListBridgeAgentsForOwner(r.ctx, r.owner, now))(t); len(got) != 0 {
			t.Fatalf("after an empty advertisement = %d agents", len(got))
		}
	})
}

// CountOnlineBridges counts live bridges of every tenant (BridgeLive).
func TestContractBridgeJobStoreCountOnlineBridges(t *testing.T) {
	// Far beyond any other test's heartbeat, so only this test's count.
	const now = int64(9_000_000_000_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		before := must(b.BridgeJobs.CountOnlineBridges(context.Background(), now))(t)
		for _, hb := range []struct {
			status BridgeStatus
			seen   int64
		}{
			{BridgeOnline, now},
			{BridgeOnline, now - BridgeOfflineAfterMs + 1},
			{BridgeOnline, now - BridgeOfflineAfterMs}, // expired
			{BridgeDraining, now},
			{BridgeOffline, now},
		} {
			r := newBJRig(b)
			r.beat(t, hb.status, hb.seen)
		}
		if got := must(b.BridgeJobs.CountOnlineBridges(context.Background(), now))(t); got-before != 2 {
			t.Fatalf("online bridges = %d more, want 2", got-before)
		}
	})
}

// PendingAgentJobs counts one owner's not-started jobs (queued, claimed) per
// (core, provider) across tenants, ordered.
func TestContractBridgeJobStorePendingAgentJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		if got := must(r.jobs().PendingAgentJobs(r.ctx, r.owner))(t); got == nil || len(got) != 0 {
			t.Fatalf("no jobs = %#v, want []", got)
		}
		r.put(t, 1, 1, func(j *AgentJob) { j.Provider = "zeta" })
		r.put(t, 2, 2)
		r.put(t, 3, 3)
		other := newBJRig(b)
		other.owner, other.core = r.owner, r.core
		other.put(t, 1, 4) // another tenant
		r.beat(t, BridgeOnline, 10)
		r.advertise(t, "t1")
		claimed := r.claim(t, 20, 2) // the two oldest codex jobs: seq 2, then seq 3
		if _, err := r.jobs().UpdateAgentJobStatus(r.ctx, JobStatusUpdate{JobID: claimed[0].JobID, BridgeID: r.bridge, Status: AgentJobRunning, NowMs: 30}); err != nil {
			t.Fatal(err)
		}
		got := must(r.jobs().PendingAgentJobs(context.Background(), r.owner))(t)
		want := []AgentJobCount{{Core: r.core, Provider: "codex", Count: 2}, {Core: r.core, Provider: "zeta", Count: 1}}
		if !slices.Equal(got, want) {
			t.Fatalf("pending = %+v, want %+v", got, want)
		}
	})
}

// Every job status is exactly one of active or terminal, and the SQL
// IN-lists are generated from the Go sets.
func TestJobStatusSetsPartitionTheStatuses(t *testing.T) {
	all := []AgentJobStatus{AgentJobQueued, AgentJobClaimed, AgentJobRunning, AgentJobReflowing, AgentJobReplied, AgentJobFailed, AgentJobTimedOut, AgentJobCancelled}
	for _, s := range all {
		if slices.Contains(activeJobStatuses, s) == s.Terminal() {
			t.Fatalf("%s: active=%v terminal=%v; want exactly one", s, slices.Contains(activeJobStatuses, s), s.Terminal())
		}
		if s.Live() && (!slices.Contains(activeJobStatuses, s) || s == AgentJobQueued) {
			t.Fatalf("%s: live but not an active leased phase", s)
		}
	}
	if len(activeJobStatuses)+len(terminalJobStatuses) != len(all) {
		t.Fatalf("status sets cover %d statuses, want %d", len(activeJobStatuses)+len(terminalJobStatuses), len(all))
	}
	if sqlTerminalJobStatuses != "'replied','failed','timed_out','cancelled'" {
		t.Fatalf("terminal IN-list = %s", sqlTerminalJobStatuses)
	}
}

// Bridge and advertisement keys are exact (D2): a bridge id embeds its
// owner's subject, so two members whose subjects differ only in case run two
// bridges, and a core's target ids differing only in case are two targets.
func TestContractBridgeJobStoreKeysAreCaseSensitive(t *testing.T) {
	const now = int64(3_000_000)
	forEachBackend(t, func(t *testing.T, b *Backends) {
		lower := newBJRig(b)
		upper := &bjRig{b: b, ctx: lower.ctx, tid: lower.tid, owner: strings.ToUpper(lower.owner), bridge: strings.ToUpper(lower.bridge), core: lower.core, room: lower.room}
		lower.beat(t, BridgeOnline, now)
		upper.beat(t, BridgeOnline, now)
		for _, r := range []*bjRig{lower, upper} {
			if owner, err := r.jobs().BridgeOwner(r.ctx, r.bridge); err != nil || owner != r.owner {
				t.Fatalf("BridgeOwner(%s) = %q, %v; want %s", r.bridge, owner, err, r.owner)
			}
		}
		lower.advertise(t, "t1", "T1")
		got := must(lower.jobs().ListBridgeAgentsForOwner(lower.ctx, lower.owner, now))(t)
		var targets []string
		for _, a := range got {
			targets = append(targets, a.TargetID)
		}
		slices.Sort(targets)
		if want := []string{"T1", "t1"}; !slices.Equal(targets, want) {
			t.Fatalf("advertised targets = %v, want %v", targets, want)
		}
		if got := must(upper.jobs().ListBridgeAgentsForOwner(upper.ctx, upper.owner, now))(t); len(got) != 0 {
			t.Fatalf("the case-variant owner lists %d agents of the other bridge", len(got))
		}
	})
}
