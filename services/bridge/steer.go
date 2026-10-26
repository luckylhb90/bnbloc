package bridge

import (
	"context"
	"log/slog"
	"strings"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A message sent while the agent works joins that work when its core can take
// it (docs/prd/2026-10-25-steer-a-running-turn.md). One message stays one job:
// the job is admitted, audited and billed as any other, and settles at once as
// steered into the running job; the running job's reply answers both. Anything
// else (no running turn of the message's own session, a core that cannot steer,
// a refusal, an error) is the queue, as before: nothing a core cannot do is
// attempted, and no core is worse off.

// steeredInto is the diagnostics key of a job that joined a running one (the
// room reads it for the "joined the running task" marker).
const steeredInto = wire.DiagSteeredInto

func (r *runningJob) setKey(k sessionKey) {
	r.mu.Lock()
	r.key, r.hasKey = k, true
	r.mu.Unlock()
}

// runningTurnOf is another job of this bridge that has a turn running in the
// session sk: bound to its core session, not stopped. Only the same session
// (same requester, channel and agent) is found, which is the privacy rule of
// multi-member rooms: a person's text never enters another's session.
// decision: steer PRD §5.1 (the session key alone: another member's or channel's turn is never found).
func (b *Bridge) runningTurnOf(sk sessionKey, except string) (jobID, core string, rs *roomSession) {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	for id, rj := range b.running {
		if id == except {
			continue
		}
		rj.mu.Lock()
		match := rj.hasKey && rj.key == sk && rj.session != nil && !rj.abort
		c, s := rj.core, rj.session
		rj.mu.Unlock()
		if match {
			return id, c, s
		}
	}
	return "", "", nil
}

// steerable is whether the job's message can join a turn as plain text: a
// person's own message, with nothing that needs the workspace prepared
// (attachments, a file to edit) or another expert worn.
// decision: steer PRD §5.1 (a person's plain message only; anything else queues).
func (d *drive) steerable() bool {
	j := d.job
	return j.byRequester() && len(j.AttachmentIDs) == 0 && j.FileRef == nil && j.ExpertID == "" && strings.TrimSpace(d.text) != ""
}

// steerInto is the one place that decides whether the job's message joins the
// running turn of its own session. It reports true when it did and the job is
// settled; false leaves the job to the ordinary drive (idle, unsupported, an
// error, no capability: the queue).
func (b *Bridge) steerInto(ctx context.Context, d *drive) bool {
	if !d.steerable() {
		return false
	}
	runID, coreName, rs := b.runningTurnOf(d.sessionKey(), d.job.JobID)
	if rs == nil {
		return false
	}
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return false
	}
	sctx, cancel := context.WithTimeout(ctx, corePollTimeout)
	defer cancel()
	steerer, ok := corepkg.As[corepkg.Steerer](sctx, entry.provider, entry.endpoint, corepkg.CapabilitySteer)
	if !ok {
		return false
	}
	text := roomlog.SteerText(b.personLabel(ctx, d.job.RequesterUserID), d.text)
	outcome, err := steerer.Steer(sctx, entry.endpoint, rs.workspaceID, rs.sessionID, text)
	if err != nil {
		slog.Warn("bnbloc bridge steer failed; queueing the message",
			"event", "bnbloc.bridge.steer_failed", "job", d.job.JobID, "core", coreName, "error", err)
		return false
	}
	if outcome != corepkg.SteerInjected {
		return false // idle: an ordinary input; unsupported: the queue
	}
	slog.Info("bnbloc bridge message joined the running turn",
		"event", "bnbloc.bridge.steered", "job", d.job.JobID, "into", runID, "core", coreName)
	b.settleSteered(ctx, d, runID)
	return true
}

// settleSteered ends a job whose message joined a running turn: its borrow
// (when it holds one) is settled with no usage of its own (the running job's
// reply reports the turn's), and it reports replied, naming the job it joined.
func (b *Bridge) settleSteered(ctx context.Context, d *drive, runID string) {
	if d.reservationID != "" {
		go b.settleBorrow(b.detached(ctx), d.job.RoomID, d.owner, d.core, d.job.Provider, d.reservationID, nil)
	}
	diag := d.agent()
	diag["targetId"] = d.target
	diag[steeredInto] = runID
	b.reportJobStatus(ctx, d.job.JobID, store.AgentJobReplied, "", diag)
}
