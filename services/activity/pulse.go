package activity

import (
	"context"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Pulse hooks (docs/prd/2026-10-24-executive-pulse-backend.md, section 4). Each
// hook runs after the store has committed the change it describes and only
// hands an event to the emitter. A nil emitter (Pulse not configured) makes
// every hook a no-op, and Emit never blocks the request.

// pulseFailure maps a store failure reason to the pulse set; "" means none.
func pulseFailure(r store.AgentJobFailureReason) string {
	if r == "" {
		return ""
	}
	if pulse.KnownFailureReason(string(r)) {
		return string(r)
	}
	return "other"
}

// pulseJobClaimed: a bridge claimed the job (its lease is set).
func (s *Service) pulseJobClaimed(job store.AgentJob) {
	s.Pulse.Emit(pulse.Event{
		ID:     "job:" + job.JobID + ":claimed:" + strconv.Itoa(job.Attempt),
		Tenant: job.TenantID,
		Kind:   pulse.KindJobClaimed,
	}.With(pulse.Enum("core", pulse.CoreFamily(job.Provider))).With(pulse.Enum("phase", "claimed")).With(pulse.Hash("job", pulse.Digest(job.JobID))))
}

// pulseJobFinished: the job reached a terminal status. Any other status is not a
// finish and emits nothing.
func (s *Service) pulseJobFinished(job store.AgentJob) {
	if !job.Status.Terminal() {
		return
	}
	ev := pulse.Event{
		ID:     "job:" + job.JobID + ":finished:" + string(job.Status),
		Tenant: job.TenantID,
		Kind:   pulse.KindJobFinished,
	}.With(pulse.Enum("core", pulse.CoreFamily(job.Provider))).With(pulse.Enum("status", string(job.Status))).With(pulse.Hash("job", pulse.Digest(job.JobID)))
	if r := pulseFailure(job.FailureReason); r != "" {
		ev = ev.With(pulse.Enum("failureReason", r))
	}
	if job.StartedAtMs > 0 && job.FinishedAtMs >= job.StartedAtMs {
		ev = ev.With(pulse.Num("durationMs", float64(job.FinishedAtMs-job.StartedAtMs)))
	}
	s.Pulse.Emit(ev)
}

// pulseJobSwept: the sweeper took back a stuck job. Its reason is the one the
// sweep recorded on the job (no_bridge_online or lease_expired).
func (s *Service) pulseJobSwept(job store.AgentJob) {
	s.Pulse.Emit(pulse.Event{
		ID:     "job:" + job.JobID + ":swept:" + strconv.Itoa(job.Attempt),
		Tenant: job.TenantID,
		Kind:   pulse.KindJobSwept,
	}.With(pulse.Enum("core", pulse.CoreFamily(job.Provider))).With(pulse.Enum("reason", pulseSweepReason(job.FailureReason))).With(pulse.Hash("job", pulse.Digest(job.JobID))))
}

func pulseSweepReason(r store.AgentJobFailureReason) string {
	if s := string(r); s == "no_bridge_online" || s == "lease_expired" {
		return s
	}
	return "other"
}

// pulseReplyUsage: one agent reply was stored. Every reply gets an event, with
// its usage when it reported some, so coverage (replies that report usage) is a
// count of events. Job notices are the product's own messages, not agent
// replies, and are skipped.
func (s *Service) pulseReplyUsage(ctx context.Context, roomID, sessionID string, m wire.Message) {
	if wire.IsJobNoticeSession(sessionID) {
		return
	}
	u, reported := store.ReplyUsageOf(m.Payload)
	ev := pulse.Event{
		ID:     "reply:" + roomID + ":" + sessionID + ":" + strconv.FormatInt(m.Seq, 10),
		Tenant: tenant.FromContext(ctx),
		Kind:   pulse.KindReplyUsage,
	}.With(pulse.Enum("core", pulse.CoreFamily(u.Provider))).With(pulse.Flag("reported", reported))
	if reported {
		ev = ev.With(pulse.Num("tokensIn", float64(u.InputTokens))).With(pulse.Num("tokensOut", float64(u.OutputTokens)))
		if u.CostUsd != nil {
			ev = ev.With(pulse.Num("costUsd", *u.CostUsd))
		}
	}
	s.Pulse.Emit(ev)
}
