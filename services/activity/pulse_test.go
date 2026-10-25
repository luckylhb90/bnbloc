package activity

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/pulse/pulsetest"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func pulseSvc(t *testing.T) (*Service, *pulsetest.Recorder) {
	t.Helper()
	e, rec := pulsetest.New(t)
	return &Service{Pulse: e}, rec
}

func TestPulseJobClaimedNamesTheFamilyNotTheProviderText(t *testing.T) {
	s, rec := pulseSvc(t)
	s.pulseJobClaimed(store.AgentJob{JobID: "j1", TenantID: "t1", Provider: "Claude", Attempt: 1})
	ev := rec.Wait(t, 1)[0]
	if ev["kind"] != pulse.KindJobClaimed || ev["tenant"] != "t1" || ev["id"] != "job:j1:claimed:1" {
		t.Fatalf("event = %v", ev)
	}
	if pulsetest.Attr(ev, "core") != "claude-code" {
		t.Fatalf("core = %v, want claude-code", pulsetest.Attr(ev, "core"))
	}
}

func TestPulseJobFinishedOnlyForTerminalStatuses(t *testing.T) {
	s, rec := pulseSvc(t)
	s.pulseJobFinished(store.AgentJob{JobID: "j1", TenantID: "t1", Status: store.AgentJobStatus("running")})
	s.pulseJobFinished(store.AgentJob{JobID: "j2", TenantID: "t1", Status: store.AgentJobStatus("queued")})
	s.pulseJobFinished(store.AgentJob{JobID: "j3", TenantID: "t1", Provider: "codex",
		Status: store.AgentJobStatus("replied"), StartedAtMs: 1000, FinishedAtMs: 1750})
	ev := rec.Wait(t, 1)[0]
	if ev["id"] != "job:j3:finished:replied" || pulsetest.Attr(ev, "durationMs") != float64(750) {
		t.Fatalf("finished event = %v", ev)
	}
	if n := len(rec.Events()); n != 1 {
		t.Fatalf("%d events; running and queued must emit nothing", n)
	}
}

func TestPulseJobFinishedMapsFailureReasons(t *testing.T) {
	s, rec := pulseSvc(t)
	s.pulseJobFinished(store.AgentJob{JobID: "j1", TenantID: "t1", Provider: "codex",
		Status: store.AgentJobStatus("failed"), FailureReason: store.FailureRunnerFailed})
	s.pulseJobFinished(store.AgentJob{JobID: "j2", TenantID: "t1", Provider: "codex",
		Status: store.AgentJobStatus("failed"), FailureReason: store.AgentJobFailureReason("reason_from_the_future")})
	evs := rec.Wait(t, 2)
	if pulsetest.Attr(evs[0], "failureReason") != "runner_failed" {
		t.Fatalf("known reason = %v", pulsetest.Attr(evs[0], "failureReason"))
	}
	if pulsetest.Attr(evs[1], "failureReason") != "other" {
		t.Fatalf("unknown reason must become other, got %v", pulsetest.Attr(evs[1], "failureReason"))
	}
}

// Every store failure reason must be in the pulse set, or a reason the store
// starts using would silently lose its label. Listing the constants (not their
// strings) makes a rename a compile error here as well.
func TestPulseAcceptsEveryStoreFailureReason(t *testing.T) {
	for _, r := range []store.AgentJobFailureReason{
		store.FailureNoBridgeOnline, store.FailureAgentNotAdvertised, store.FailureBorrowNotAuthorized,
		store.FailureCoreUnavailable, store.FailureRunnerStartFailed, store.FailureRunnerFailed,
		store.FailureAgentError, store.FailureContextOverflow, store.FailureContextReset,
		store.FailureLeaseExpired, store.FailureSandboxStarting, store.FailureReplyTimeout, store.FailureNoReply,
		store.FailureCancelled, store.FailureBudgetExhausted, store.FailureBudgetUnavailable,
		store.FailureSandboxRequired, store.FailureContentUnavailable, store.FailureTeamUnsupported,
		store.FailureInvalidJob,
	} {
		if !pulse.KnownFailureReason(string(r)) {
			t.Errorf("store reason %q is not in the pulse failure-reason set", r)
		}
	}
}

func TestPulseSweptReasonIsTheSweepsOwn(t *testing.T) {
	s, rec := pulseSvc(t)
	s.pulseJobSwept(store.AgentJob{JobID: "j1", TenantID: "t1", Provider: "codex", Attempt: 2, FailureReason: store.FailureLeaseExpired})
	s.pulseJobSwept(store.AgentJob{JobID: "j2", TenantID: "t1", Attempt: 1, FailureReason: store.FailureRunnerFailed})
	evs := rec.Wait(t, 2)
	if ev := evs[0]; ev["id"] != "job:j1:swept:2" || pulsetest.Attr(ev, "reason") != "lease_expired" {
		t.Fatalf("swept event = %v", ev)
	}
	if pulsetest.Attr(evs[1], "reason") != "other" {
		t.Fatalf("a reason the sweep does not produce must be other, got %v", pulsetest.Attr(evs[1], "reason"))
	}
}

func TestPulseReplyUsageCountsAgentRepliesOnly(t *testing.T) {
	s, rec := pulseSvc(t)
	ctx := tenant.NewContext(context.Background(), "t1")
	withUsage := wire.Message{Seq: 7, Payload: map[string]any{
		"provider": "codex", "metrics": map[string]any{"inputTokens": 10, "outputTokens": 5}}}
	withoutUsage := wire.Message{Seq: 8, Payload: map[string]any{"provider": "codex"}}
	notice := wire.Message{Seq: 9, Payload: map[string]any{"provider": "codex"}}

	s.pulseReplyUsage(ctx, "r1", "sess1", withUsage)
	s.pulseReplyUsage(ctx, "r1", "sess1", withoutUsage)
	s.pulseReplyUsage(ctx, "r1", wire.JobNoticeSessionID("j1"), notice)

	evs := rec.Wait(t, 2)
	if len(rec.Events()) != 2 {
		t.Fatalf("a job notice is not an agent reply: %d events", len(rec.Events()))
	}
	if ev := evs[0]; ev["id"] != "reply:r1:sess1:7" || pulsetest.Attr(ev, "reported") != true ||
		pulsetest.Attr(ev, "tokensIn") != float64(10) || pulsetest.Attr(ev, "tokensOut") != float64(5) {
		t.Fatalf("reply with usage = %v", ev)
	}
	if ev := evs[1]; pulsetest.Attr(ev, "reported") != false || pulsetest.Attr(ev, "tokensIn") != nil {
		t.Fatalf("reply without usage must report false and no tokens: %v", ev)
	}
}

// With Pulse off (a nil emitter) every hook is a silent no-op.
func TestPulseHooksAreNoopWithoutAnEmitter(t *testing.T) {
	s := &Service{}
	ctx := tenant.NewContext(context.Background(), "t1")
	s.pulseJobClaimed(store.AgentJob{JobID: "j"})
	s.pulseJobFinished(store.AgentJob{JobID: "j", Status: store.AgentJobStatus("replied")})
	s.pulseJobSwept(store.AgentJob{JobID: "j"})
	s.pulseReplyUsage(ctx, "r", "s", wire.Message{Seq: 1})
}
