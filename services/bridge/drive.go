package bridge

import (
	"context"
	"log/slog"
	"strings"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The drive pipeline. A claimed job runs through eight named stages, in
// order:
//
//	1 admit                  text, core, disabled provider: owner and core
//	2 resolve content        expert, skill packs, MCP tools (unmet: missing)
//	3 snapshot capabilities  the core's capabilities, asked once per drive
//	4 plan execution         a team needs the room tools; host or sandbox
//	5 authorize              a borrow's reservation (a platform agent's
//	                         sandbox requirement re-checked)
//	6 bind target            the core-local target; MCP servers fitted to it
//	7 drive                  under the drive lock: prepare the sandbox,
//	                         ensure the session, send, reflow
//	8 settle                 settle the borrow, report, publish deliverables
//
// Stages 1–6 fill in the drive or return a refusal (job_failure.go): nothing
// ran, and the job fails final with a notice. Stage 7's error is a drive
// failure, classified by the stage it reached (fail). The own, borrowed,
// platform, sandboxed and dispatched drives all take this one path: they
// differ only in the inputs to stages 4 and 5.

// drive is one claimed job's plan, filled in stage by stage.
type drive struct {
	job   bridgeJob
	owner string // the agent's owner
	// seatOwner is the owner when the agent is not the bridge owner's own
	// (a bridge serving every owner): the slot is that owner's (driveKey).
	seatOwner string
	// run is the job's registration (a cancel interrupts the bound turn).
	run *runningJob
	// startMs is when the drive began: deliverable publishing is scoped to
	// what this turn wrote.
	startMs int64

	text     string // stage 1
	core     string // stage 1
	borrowed bool   // stage 1: the requester is not the agent's owner

	content *jobContent // stage 2
	caps    driveCaps   // stage 3

	// prepareExec readies a sandboxed turn (nil: the drive runs on the
	// host); unsandboxed says why a borrowed drive is not sandboxed (stage 4).
	prepareExec func(context.Context) (*corepkg.Execution, error)
	// startGate is the workspace gate the turn's start passes (nil when
	// the drive holds it for the whole turn; turn_gates.go).
	startGate   gate
	unsandboxed string
	// notReady: the sandbox this drive must run in is not up yet (stage 4);
	// the job fails sandbox_starting (retried) before stage 5 reserves it.
	notReady error
	// ownSandbox: the sandboxed drive runs on its owner's own behalf and
	// gets the owner's forwarded environment (sandboxDecision.ownerEnv);
	// ownMachine: it runs on the owner's own machine (driveKey.ownMachine).
	ownSandbox bool
	ownMachine bool

	reservationID string // stage 5: the borrow's reservation ("" = none)
	target        string // stage 6

	// edit is the room file the request edits, placed in the workspace with
	// the attachments (file_edit.go; nil = none).
	edit *editTarget
}

// stage is one refusable stage of the pipeline.
type stage struct {
	name string
	run  func(*Bridge, context.Context, *drive) *refusal
}

// refusableStages are stages 1–6, in order.
var refusableStages = []stage{
	{"admit", (*Bridge).admit},
	{"resolve content", (*Bridge).resolveContent},
	{"snapshot capabilities", (*Bridge).snapshotCapabilities},
	{"plan execution", (*Bridge).planExecution},
	{"authorize", (*Bridge).authorize},
	{"bind target", (*Bridge).bindTarget},
}

// ref is the job's drive identity.
func (d *drive) ref() jobRef {
	return jobRef{ID: d.job.JobID, MessageSeq: d.job.MessageSeq, RequesterUserID: d.job.RequesterUserID, TenantID: d.job.TenantID, RoomID: d.job.RoomID, ChannelID: d.job.ChannelID, content: d.content}
}

// key is the drive's slot: its session and drive lock.
func (d *drive) key() driveKey {
	return driveKey{room: d.job.RoomID, core: d.core, provider: d.job.Provider, sandboxed: d.prepareExec != nil, ownMachine: d.ownMachine, owner: d.seatOwner, requester: d.seatRequester()}
}

// seatRequester is the requester whose seat the drive takes: none for the
// owner's own turn, the requester for a borrowed one.
func (d *drive) seatRequester() string {
	if d.borrowed {
		return d.job.RequesterUserID
	}
	return ""
}

// agent is the core/provider pair of the job's diagnostics.
func (d *drive) agent() map[string]any {
	return map[string]any{"core": firstNonEmpty(d.core, d.job.Core), "provider": d.job.Provider}
}

// runBridgeJob drives one claimed job through the pipeline.
func (b *Bridge) runBridgeJob(ctx context.Context, job bridgeJob) {
	// Hold the lease for as long as the turn actually runs. Without renewal a
	// turn longer than the server lease is reclaimed by another bridge and the
	// same prompt executes twice.
	ctx, stopDrive := context.WithCancel(ctx)
	defer stopDrive()
	// Register the job so a cancellation arriving over the room stream can
	// interrupt this turn at once. The lease loop is the fallback for when the
	// stream is down: slower, but it still stops the work.
	d := &drive{job: job, owner: firstNonEmpty(job.OwnerUserID, b.cfg.OwnerSubject), run: &runningJob{stop: stopDrive}, startMs: time.Now().UnixMilli()}
	if d.owner != b.cfg.OwnerSubject {
		d.seatOwner = d.owner
	}
	defer b.trackJob(job.JobID, d.run)()
	go b.renewLeaseLoop(ctx, job.JobID, func() { b.abortJob(job.JobID, "lease_lost") })
	for _, st := range refusableStages {
		if r := st.run(b, ctx, d); r != nil {
			slog.Info("bnbloc bridge job refused",
				"event", "bnbloc.bridge.job_refused", "job", job.JobID, "stage", st.name, "reason", string(r.kind.reason))
			b.refuse(ctx, d, r)
			return
		}
		if d.notReady != nil {
			slog.Info("bnbloc bridge job waits for the agent sandbox",
				"event", "bnbloc.bridge.job_waits_for_sandbox", "job", job.JobID, "stage", st.name, "error", d.notReady)
			b.fail(ctx, d, atStage(stagePrepare, d.notReady), b.sandboxDiagnostics(d))
			return
		}
	}
	res, err := b.driveTurn(ctx, d)
	b.settle(ctx, d, res, err)
}

// admit is stage 1: the job carries something to say, names a core this
// bridge runs (a named core is never redirected to another: that would
// answer with a different agent), and a provider the member did not turn
// off.
func (b *Bridge) admit(_ context.Context, d *drive) *refusal {
	d.text = strings.TrimSpace(d.job.MessageText)
	if d.text == "" && len(d.job.AttachmentIDs) == 0 {
		return refuseInvalidJob.refused("This message to "+d.job.Provider+" has no text or attachments, so nothing ran.", nil)
	}
	core, ok := b.resolveCore(d.job.Core)
	if ok && b.providerDisabled(core, d.job.Provider) {
		ok = false
	}
	if !ok {
		agent := d.job.Provider
		if d.job.Core != "" {
			agent = corepkg.QualifyTargetID(d.job.Core, d.job.Provider)
		}
		return refuseNotAdvertised.refused("The "+agent+" agent is not available on "+d.owner+"'s machine, so nothing ran.", d.agent())
	}
	d.core = core
	d.borrowed = isBorrow(d.job.RequesterUserID, d.owner)
	return nil
}

// resolveContent is stage 2: the expert and skill packs the turn wears,
// resolved and verified before anything else (content from someone else
// that runs scripts forces the sandbox, decided in stage 4). A job naming an
// expert it cannot wear is refused; any other resolution failure drives the
// turn without content (logged): room and member skill packs are
// best-effort, never a reason to drop a turn.
func (b *Bridge) resolveContent(ctx context.Context, d *drive) *refusal {
	content, err := b.resolveJobContent(ctx, d.job, d.owner)
	switch {
	case err == nil:
		d.content = content
	case d.job.ExpertID == "":
		slog.Warn("bnbloc bridge content resolution failed; driving without content",
			"event", "bnbloc.bridge.content_unresolved", "job", d.job.JobID, "error", err)
		d.content = &jobContent{}
	default:
		return refuseContentUnavailable.refused(contentUnavailableText(d.job, err), d.agent())
	}
	return nil
}

// driveCaps is the core's capability verdicts, asked once per drive (stage
// 3): every later stage reads these, so a drive never sees a capability
// appear or vanish halfway.
type driveCaps struct {
	// reachable: the core's endpoint is known, so the verdicts were asked.
	reachable bool
	// unknown: the core could not say what it offers (its discovery
	// failed). The drive fails retryably at its prepare instead of running
	// a session without the profile, room tools and records the core does
	// offer.
	unknown error
	// profile: agentProfile (an agent profile, MCP servers, room tools).
	profile bool
	// workdir: sandboxExec (a workspace created in the sandbox workdir).
	workdir corepkg.WorkdirWorkspaceCreator
	// writer / bytesWriter: writeFile (.room/ context, attachments,
	// fallback skill files).
	writer      corepkg.WorkspaceFileWriter
	bytesWriter corepkg.WorkspaceFileBytesWriter
	// maxInput is the core's stated request body limit (0 = unstated):
	// a file whose write would exceed it is reported, never sent.
	maxInput int64
	// refresher: executionRefresh (a running sandboxed turn's exec token
	// is renewed; renewExecution).
	refresher corepkg.ExecutionRefresher
	// maxTurns is how many turns the core states it runs at once (0 =
	// unstated; turn_gates.go).
	maxTurns int
}

// execTokenTTL is a sandboxed turn's first exec token lifetime: short
// when the core takes renewals, long enough for long turns when it cannot.
func (d *drive) execTokenTTL() time.Duration {
	if d.caps.refresher != nil {
		return sandboxExecTokenTTL
	}
	return sandboxExecTokenNoRefreshTTL
}

// snapshotCapabilities is stage 3.
func (b *Bridge) snapshotCapabilities(ctx context.Context, d *drive) *refusal {
	d.caps = b.coreCaps(ctx, d.core)
	return nil
}

// coreCaps asks a core's capabilities once (zero when its endpoint is not
// known yet).
func (b *Bridge) coreCaps(ctx context.Context, coreName string) driveCaps {
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return driveCaps{}
	}
	p, ep := entry.provider, entry.endpoint
	if err := corepkg.CapabilitiesKnown(ctx, p, ep); err != nil {
		return driveCaps{reachable: true, unknown: err}
	}
	caps := driveCaps{reachable: true, profile: corepkg.Supports(ctx, p, ep, corepkg.CapabilityAgentProfile)}
	caps.workdir, _ = corepkg.As[corepkg.WorkdirWorkspaceCreator](ctx, p, ep, corepkg.CapabilitySandboxExec)
	caps.writer, _ = corepkg.As[corepkg.WorkspaceFileWriter](ctx, p, ep, corepkg.CapabilityWriteFile)
	caps.bytesWriter, _ = corepkg.As[corepkg.WorkspaceFileBytesWriter](ctx, p, ep, corepkg.CapabilityWriteFile)
	caps.maxInput = corepkg.MaxInputBytes(ctx, p, ep)
	caps.maxTurns = corepkg.MaxConcurrentTurns(ctx, p, ep)
	caps.refresher, _ = corepkg.As[corepkg.ExecutionRefresher](ctx, p, ep, corepkg.CapabilityExecutionRefresh)
	return caps
}

// planExecution is stage 4: a team expert needs the room tools (its lead
// dispatches to the roles), and where the drive runs — host or agent
// sandbox — is decided before any quota is reserved: a borrowed drive the
// tenant requires to be sandboxed, or a turn wearing someone else's
// script-bearing content, is refused here when the sandbox cannot take it.
func (b *Bridge) planExecution(ctx context.Context, d *drive) *refusal {
	// An unreachable core's capabilities are unknown: the drive fails
	// retryably as core_unavailable instead.
	if team := d.content.teamRoster(); team != nil && d.caps.reachable && d.caps.unknown == nil && !d.caps.profile {
		return refuseTeamUnsupported.refused("The team "+firstNonEmpty(team.Name, "expert")+" needs an agent that can use the room tools to ask its members; "+d.job.Provider+" cannot. Nothing ran.", d.agent())
	}
	if refuse := b.planSandbox(ctx, d); refuse != "" {
		return refuseSandboxRequired.refused(refuse, b.sandboxDiagnostics(d))
	}
	return nil
}

func (b *Bridge) sandboxDiagnostics(d *drive) map[string]any {
	diag := d.agent()
	diag["requesterUserId"] = d.job.RequesterUserID
	return diag
}

// authorize is stage 5: a borrow runs only on a positive authorization.
// When this bridge acts as the owner it reserves the run (R8: the
// reservation counts against the quota before anything is driven) and
// settles it after the drive. The reservation says whether the drive must
// be sandboxed (a platform agent): one that is not is refused like the
// tenant policy's, before anything runs.
func (b *Bridge) authorize(ctx context.Context, d *drive) *refusal {
	if !d.borrowed {
		return nil
	}
	rsv := b.authorizeBorrow(ctx, d.job, d.owner, d.core, d.prepareExec != nil)
	switch rsv.DenyReason {
	case "":
	case shareagent.DenySandboxRequired:
		return refuseSandboxRequired.refused(platformSandboxPrefix+d.unsandboxed, b.sandboxDiagnostics(d))
	default:
		diag := b.sandboxDiagnostics(d)
		diag["denyReason"] = rsv.DenyReason
		text := borrowDeniedText(d.owner, d.job.Provider, rsv.DenyReason)
		if rsv.AccessRequested {
			text += accessRequestedText
		}
		r := refuseBorrowDenied.refused(text, diag)
		r.notice = map[string]any{"denyReason": rsv.DenyReason}
		if rsv.AccessRequested {
			r.notice["accessRequested"] = true
		}
		return r
	}
	d.reservationID = rsv.ID
	// A platform agent's turn that named no model runs with its host's
	// default model, exactly as if the member had picked it: the session
	// options, the run report and the settle all read job.Model.
	if d.job.Model == "" {
		d.job.Model = rsv.DefaultModel
	}
	return nil
}

// bindTarget is stage 6: the core-local target the session is created for
// (a qualified job target of this core names it; realtime's core/provider
// placeholder resolves through the core), and the content's MCP servers
// fitted to what that target can attach.
func (b *Bridge) bindTarget(ctx context.Context, d *drive) *refusal {
	d.target = b.resolveTarget(ctx, d.core, d.job.Provider)
	if core, raw := corepkg.SplitTargetID(strings.TrimSpace(d.job.TargetID)); raw != "" && raw != d.job.Provider && (core == "" || core == d.core) {
		d.target = raw
	}
	if len(d.content.mcp.servers) > 0 {
		d.content.fitMCPToTarget(d.caps.profile, b.targetTransports(d.core, d.target, d.job.Provider))
	}
	return nil
}

// driveTurn is stage 7: report the job running, then drive it.
func (b *Bridge) driveTurn(ctx context.Context, d *drive) (driveResult, error) {
	defer b.turnRunning(d.core, d.ownMachine)() // the desktop applies core updates (and own-machine changes) only when idle
	running := d.agent()
	running["targetId"], running["sandboxed"] = d.target, d.prepareExec != nil
	if c := d.content; c.expert != nil {
		running["expert"] = expertLabel(c.expert)
	}
	if c := d.content; c.roomExpert != nil {
		running["roomExpert"] = expertLabel(c.roomExpert)
	}
	if len(d.content.missing) > 0 {
		running["contentMissing"] = d.content.missing
	}
	if err := b.reportJobStatus(ctx, d.job.JobID, store.AgentJobRunning, "", d.content.withTools(running, wire.AgentRunTools{})); err != nil {
		slog.Debug("bnbloc bridge job running status failed", "event", "bnbloc.bridge.job_status_failed", "job", d.job.JobID, "error", err)
	}
	return b.driveClaim(ctx, d, func(rs *roomSession) {
		// Publish the bound session so a cancel arriving mid-turn can interrupt it.
		d.run.bind(d.core, rs)
		diag := d.content.withTools(d.agent(), wire.AgentRunTools{})
		diag["workspaceId"], diag["sessionId"] = rs.workspaceID, rs.sessionID
		b.reportJobStatus(ctx, d.job.JobID, store.AgentJobReflowing, "", diag)
	})
}

// settle is stage 8. A reserved borrow is settled once the drive ends —
// whatever its outcome; the run was counted at reserve time — with the
// usage the turn's replies reported. An aborted job is no longer ours: the
// server already recorded the cancel and cleared the lease, so no outcome
// is reported. Otherwise the job reports replied, or fails by the stage its
// drive reached; a turn its agent or core reported failed fails even when
// it posted replies (agentFailed). Before the outcome is reported, the files
// the turn changed become artifact versions, once per turn (publishTurn):
// whoever reads the job's outcome — a member, a dispatching agent's
// ask_agent — finds its deliverables
// (docs/prd/2026-10-14-multi-member-rooms.md rule 7).
func (b *Bridge) settle(ctx context.Context, d *drive, res driveResult, err error) {
	if d.reservationID != "" {
		go b.settleBorrow(b.detached(ctx), d.job.RoomID, d.owner, d.core, d.job.Provider, d.reservationID, res.usage.recordFields(d.job.Model))
	}
	// The room tool calls are taken once, whatever the outcome: an aborted
	// job's are dropped with it.
	calls := b.roomToolCalls(d.job.JobID)
	if d.run.aborted() {
		slog.Info("bnbloc bridge job abandoned after abort",
			"event", "bnbloc.bridge.job_abandoned", "job", d.job.JobID)
		return
	}
	b.publishTurn(ctx, d)
	diag := d.content.withTools(d.agent(), calls)
	if d.prepareExec != nil {
		// The member's card reads it (find-a-way's allow-host).
		diag["sandboxed"] = wire.SandboxedShared
		if d.ownSandbox {
			diag["sandboxed"] = wire.SandboxedOwn
		}
	}
	if res.sessionID != "" {
		diag["workspaceId"], diag["sessionId"] = res.workspaceID, res.sessionID
	}
	if res.replied && !agentFailed(err) {
		if err != nil {
			diag["warning"] = cleanDriveError(err)
		}
		b.reportJobStatus(ctx, d.job.JobID, store.AgentJobReplied, "", res.usage.withRunReport(diag, d.job.Model))
		return
	}
	if err == nil {
		// The turn ran (it may have acted) without a visible reply: final.
		err = errNoReply
	}
	diag["targetId"] = d.target
	diag["error"] = cleanDriveError(err)
	b.fail(ctx, d, err, res.usage.withRunReport(diag, d.job.Model))
}

// publishWait bounds how long a job's outcome waits for its deliverables'
// publish; a slower publish finishes after the outcome is reported.
const publishWait = time.Minute

// publishTurn publishes what the turn changed in its session's workspace as
// artifact versions, once per turn, after the turn settled (reflow
// returned) and before its outcome is reported (bounded by publishWait):
// each site or file the turn changed gets exactly one new version however
// often the agent rewrote it, and an unchanged one none. A drive that never
// bound a session started no turn and publishes nothing. Presentation only:
// a failed publish never marks the turn failed.
func (b *Bridge) publishTurn(ctx context.Context, d *drive) {
	_, rs := d.run.bound()
	if rs == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.publishDeliverables(b.detached(ctx), d.job.RoomID, d.job.ChannelID, rs,
			deliverableTurn{StartMs: d.startMs, JobID: d.job.JobID, SourceSeq: d.job.MessageSeq, edit: d.edit})
		b.noticeEditOutcome(b.detached(ctx), d)
	}()
	t := time.NewTimer(b.publishWaitFor())
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		slog.Warn("bnbloc bridge deliverables still publishing; reporting the job first",
			"event", "bnbloc.bridge.publish_slow", "job", d.job.JobID, "room", d.job.RoomID)
	case <-ctx.Done():
	}
}

// publishWaitFor is publishWait (Bridge.publishWaitOverride in tests).
func (b *Bridge) publishWaitFor() time.Duration {
	if b.publishWaitOverride > 0 {
		return b.publishWaitOverride
	}
	return publishWait
}

// detached is the context for work that outlives a drive (a borrow settle,
// a deliverable publish): the bridge's root context, never the drive's,
// which is cancelled the moment the drive returns.
func (b *Bridge) detached(ctx context.Context) context.Context {
	if b.driveCtx != nil {
		return b.driveCtx
	}
	return context.WithoutCancel(ctx)
}

// driveKey names one agent's drive slot in a room: its bound session and
// its drive lock. A sandboxed drive has a slot apart from the host one: a
// session's workspace lives either on the host or under the sandbox
// workspaces root, so a change of execution kind (mode switch, sandbox
// ready/unavailable, own vs borrowed) starts a separate session and
// workspace instead of moving one.
//
// A slot is also one requester's seat
// (docs/prd/2026-10-14-multi-member-rooms.md rule 1): the owner's own turns
// use the slot with no requester; a member borrowing the agent (and every
// member using a platform agent) gets a slot of their own — workspace,
// sessions, sandbox space, drive lock — so the owner's conversation,
// attachments and Library listing never reach a borrower's turn, nor the
// reverse. owner is set only when the agent is not the bridge owner's (a
// bridge serving every owner), so two owners' agents never share a slot.
type driveKey struct {
	room, core, provider string
	sandboxed            bool
	// ownMachine: the sandboxed slot runs on the member's own machine
	// (SandboxConfig.OwnMachine, the private hosts they allowed), never the
	// shared one: a session, workspace and space of its own.
	ownMachine       bool
	owner, requester string
}

// sessionKey is one channel's session of a drive slot
// (docs/prd/2026-10-13-sessions-per-channel.md): an agent has a session per
// channel, so a channel's conversation is its own session's memory, and
// one workspace (and drive lock) per drive slot, shared by those sessions.
type sessionKey struct {
	driveKey
	channel string
}

// sessionKey is the drive's channel session.
func (d *drive) sessionKey() sessionKey {
	return sessionKey{driveKey: d.key(), channel: d.job.ChannelID}
}

// Where a slot's commands run (driveKey.place): on the host, in the shared
// sandbox machine, or in the member's own machine.
const (
	placeHost       = "host"
	placeSandbox    = "sandbox"
	placeOwnSandbox = "own-sandbox"
)

// place is where slot k's commands run.
func (k driveKey) place() string {
	switch {
	case !k.sandboxed:
		return placeHost
	case k.ownMachine:
		return placeOwnSandbox
	}
	return placeSandbox
}

// agentID is the (room, core, provider) identity both execution kinds of
// an agent share (its sandbox space is named from it).
func (k driveKey) agentID() string { return k.room + "\x00" + k.core + "\x00" + k.provider }

// seatID is what tells the slot's seat apart within its agent: "" for the
// owner's own seat (the identity every own-seat name and id is derived
// from, unchanged), else the owner and requester.
func (k driveKey) seatID() string {
	if k.owner == "" && k.requester == "" {
		return ""
	}
	return k.owner + "\x00" + k.requester
}

// seatAgentID is the identity of one seat of the agent: agentID for the
// owner's own seat, else agentID with the seat appended (the sandbox space
// and a deliverable id are named from it).
func (k driveKey) seatAgentID() string {
	id := driveKey{room: k.room, core: k.core, provider: k.provider}.agentID()
	if seat := k.seatID(); seat != "" {
		id += "\x00" + seat
	}
	return id
}
