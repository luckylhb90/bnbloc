package bridge

import (
	"context"
	"errors"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Job failure classification. This file is the one place the bridge decides
// which failure reason a job reports (TestDriveRefusalsAreFinal forbids a
// failed status anywhere else), and store.AgentJobFailureReason.Retryable
// decides what that reason means. The rule: a job is retried only when no
// agent work can have started. A deterministic refusal fails the same way
// again, and a started (or possibly started) turn would run the member's
// prompt twice, charging a borrow twice.

// refusal is a deterministic "nothing ran" outcome: the job's final failure
// reason and, when the member must see why, the room notice kind.
type refusal struct {
	reason store.AgentJobFailureReason
	// notice is the room notice kind refuse posts; "" when there is none,
	// or the caller posts its own (a refused borrow names the deny reason).
	notice string
}

var (
	refuseInvalidJob         = refusal{reason: store.FailureInvalidJob}
	refuseNotAdvertised      = refusal{reason: store.FailureAgentNotAdvertised}
	refuseContentUnavailable = refusal{reason: store.FailureContentUnavailable, notice: "content-unavailable"}
	refuseTeamUnsupported    = refusal{reason: store.FailureTeamUnsupported, notice: "team-unsupported"}
	refuseSandboxRequired    = refusal{reason: store.FailureSandboxRequired, notice: "sandbox-required"}
	refuseBorrowDenied       = refusal{reason: store.FailureBorrowNotAuthorized}
)

// driveRefusals lists every refusal above (TestDriveRefusalsAreFinal checks
// each is final, and that none is declared elsewhere).
var driveRefusals = []refusal{
	refuseInvalidJob, refuseNotAdvertised, refuseContentUnavailable,
	refuseTeamUnsupported, refuseSandboxRequired, refuseBorrowDenied,
}

// refuse ends a job that must not run: it posts r's notice (text, owner,
// provider and failReason r.reason) when r has one, then reports r's final
// reason with diagnostics (text as "error" when set).
func (b *Bridge) refuse(ctx context.Context, job bridgeJob, owner string, r refusal, text string, diagnostics map[string]any) {
	if r.notice != "" {
		b.postNotice(ctx, job.ref(), job.RoomID, job.ChannelID, r.notice, map[string]any{
			"text":        text,
			"ownerUserId": owner,
			"provider":    job.Provider,
			"failReason":  string(r.reason),
		})
	}
	if diagnostics == nil {
		diagnostics = map[string]any{}
	}
	if text != "" {
		diagnostics["error"] = text
	}
	b.failJob(ctx, job.JobID, r.reason, diagnostics)
}

// failJob reports a job failed: the only failed-status report the bridge
// sends.
func (b *Bridge) failJob(ctx context.Context, jobID string, reason store.AgentJobFailureReason, diagnostics map[string]any) {
	_ = b.reportJobStatus(ctx, jobID, store.AgentJobFailed, reason, diagnostics)
}

// driveStage is how far a drive got before it failed.
type driveStage int

const (
	// stagePrepare: readying the drive (sandbox space and token, the core's
	// endpoint, the workspace). Nothing that starts agent work was sent.
	stagePrepare driveStage = iota
	// stageStart: the request that starts the turn (session create with the
	// initial prompt, or follow-up input).
	stageStart
	// stageRun: the turn started; its reply was being collected.
	stageRun
)

// driveError is a drive failure tagged with the stage it happened in.
type driveError struct {
	stage driveStage
	err   error
}

func (e *driveError) Error() string { return e.err.Error() }
func (e *driveError) Unwrap() error { return e.err }

// atStage tags err with stage unless it already carries one (an inner
// stage, e.g. a session create inside the ensure step, is the precise one).
func atStage(stage driveStage, err error) error {
	var de *driveError
	if err == nil || errors.As(err, &de) {
		return err
	}
	return &driveError{stage: stage, err: err}
}

// coreNotRunningError: the core's daemon has not been found on this
// machine (yet); nothing was sent to it.
type coreNotRunningError struct{ core string }

func (e coreNotRunningError) Error() string {
	return "the " + e.core + " core is not running on this machine yet"
}

// driveFailureReason maps a failed drive to its job failure reason by stage.
// Before the turn starts, an unreachable core is core_unavailable; a start
// request that provably never took effect (core.Retryable for a request
// that is not idempotent) is runner_start_failed. Both retry. Anything
// else — a started turn, an ambiguous start, a deterministic failure, an
// error of unknown stage — is runner_failed, which never retries.
func driveFailureReason(err error) store.AgentJobFailureReason {
	var de *driveError
	if !errors.As(err, &de) {
		return store.FailureRunnerFailed
	}
	switch de.stage {
	case stagePrepare:
		var notRunning coreNotRunningError
		if errors.As(err, &notRunning) || corepkg.Retryable(err, false) {
			return store.FailureCoreUnavailable
		}
	case stageStart:
		if corepkg.Retryable(err, false) {
			return store.FailureRunnerStartFailed
		}
	}
	return store.FailureRunnerFailed
}
