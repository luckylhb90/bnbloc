package bridge

import (
	"context"
	"errors"
	"strings"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Job failure classification. This file is the one place the bridge decides
// which failure reason a job reports and which room notice says why
// (TestDriveRefusalsAreFinal forbids a failed status or a refusal anywhere
// else); store.AgentJobFailureReason.Retryable decides what that reason
// means. The rule: a job is retried only when no agent work can have
// started. A deterministic refusal fails the same way again, and a started
// (or possibly started) turn would run the member's prompt twice, charging
// a borrow twice. Every refusal and every drive failure posts one notice, so
// the member always sees why nothing (or nothing more) came back.

// refusalKind is one way a drive stage refuses a job: the room notice kind
// (wire.JobNotice.Kind) and the job's final failure reason.
type refusalKind struct {
	notice string
	reason store.AgentJobFailureReason
}

var (
	refuseInvalidJob         = refusalKind{notice: "invalid-job", reason: store.FailureInvalidJob}
	refuseNotAdvertised      = refusalKind{notice: "agent-unavailable", reason: store.FailureAgentNotAdvertised}
	refuseContentUnavailable = refusalKind{notice: "content-unavailable", reason: store.FailureContentUnavailable}
	refuseTeamUnsupported    = refusalKind{notice: "team-unsupported", reason: store.FailureTeamUnsupported}
	refuseSandboxRequired    = refusalKind{notice: "sandbox-required", reason: store.FailureSandboxRequired}
	refuseBorrowDenied       = refusalKind{notice: "borrow-denied", reason: store.FailureBorrowNotAuthorized}
)

// refusalKinds lists every refusal kind above (TestDriveRefusalsAreFinal
// checks each is final and posts a notice, and that none is declared
// elsewhere).
var refusalKinds = []refusalKind{
	refuseInvalidJob, refuseNotAdvertised, refuseContentUnavailable,
	refuseTeamUnsupported, refuseSandboxRequired, refuseBorrowDenied,
}

// refusal is a drive stage's deterministic "nothing ran" outcome: the room
// notice (kind and text), the final failure reason, the job diagnostics, and
// kind-specific notice fields (a refused borrow's denyReason).
type refusal struct {
	kind        refusalKind
	text        string
	diagnostics map[string]any
	notice      map[string]any
}

// refused builds a refusal of kind k: text is the member-readable notice
// (and the diagnostics' error).
func (k refusalKind) refused(text string, diagnostics map[string]any) *refusal {
	return &refusal{kind: k, text: text, diagnostics: diagnostics}
}

// refuse ends a job a drive stage refused: its notice, then its final
// reason with the refusal's diagnostics (the text as "error").
func (b *Bridge) refuse(ctx context.Context, d *drive, r *refusal) {
	b.postNotice(ctx, d.ref(), wire.JobNotice{
		Kind: r.kind.notice, Text: r.text, OwnerUserID: d.owner, Provider: d.job.Provider,
		FailReason: string(r.kind.reason), Extra: r.notice,
	})
	diagnostics := r.diagnostics
	if diagnostics == nil {
		diagnostics = map[string]any{}
	}
	diagnostics["error"] = r.text
	b.failJob(ctx, d.job.JobID, r.kind.reason, diagnostics)
}

// fail ends a job whose drive failed (err from the drive stage): the notice
// for the stage it reached and the reason driveFailureReason gives it.
// diagnostics are the run report the caller assembled.
func (b *Bridge) fail(ctx context.Context, d *drive, err error, diagnostics map[string]any) {
	reason := driveFailureReason(err)
	b.postNotice(ctx, d.ref(), wire.JobNotice{
		Kind: driveFailureNotice(err), Text: driveErrorText(d.job.Provider, err),
		OwnerUserID: d.owner, Provider: d.job.Provider, FailReason: string(reason),
	})
	b.failJob(ctx, d.job.JobID, reason, diagnostics)
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

// turnFailedError: the core reports the started turn failed (detail is the
// provider's message, e.g. a missing API key).
type turnFailedError struct{ detail string }

func (e turnFailedError) Error() string { return "turn failed: " + e.detail }

// errNoReply: the turn ran (it may have acted) and ended without a visible
// reply. Final: a retry would run the prompt again.
var errNoReply = errors.New("agent turn completed without a visible reply")

// driveFailureReason maps a failed drive to its job failure reason by stage.
// Before the turn starts, an unreachable core is core_unavailable; a start
// request that provably never took effect (core.Retryable for a request
// that is not idempotent) is runner_start_failed. Both retry. A turn that
// ran without a reply is reply_timeout. Anything else — a started turn, an
// ambiguous start, a deterministic failure, an error of unknown stage — is
// runner_failed, which never retries.
func driveFailureReason(err error) store.AgentJobFailureReason {
	if errors.Is(err, errNoReply) {
		return store.FailureReplyTimeout
	}
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

// driveFailureNotice is the notice kind of a failed drive: the agent could
// not be reached (nothing started), or the started turn failed.
func driveFailureNotice(err error) string {
	var de *driveError
	if errors.As(err, &de) && de.stage == stageRun || errors.Is(err, errNoReply) {
		return "turn-failed"
	}
	return "drive-failed"
}

// driveErrorText is the member-readable notice of a failed drive, rendered
// from the drive ERROR: the stage it reached and the core's own detail (a
// missing API key, a disconnected runtime). It is not a failure-reason
// sentence: the reason's member-facing sentence (notifications, activity's
// notices) is store.AgentJobFailureReason.Text, which knows no error detail.
func driveErrorText(provider string, err error) string {
	var failed turnFailedError
	switch {
	case errors.Is(err, errNoReply):
		return "The " + provider + " agent finished without a visible reply."
	case errors.As(err, &failed):
		return withDetail("The agent could not complete this request", failed.detail)
	case driveFailureNotice(err) == "turn-failed":
		return withDetail("The "+provider+" agent stopped before it replied", cleanDriveError(err))
	default:
		return withDetail("The "+provider+" agent could not be reached", cleanDriveError(err))
	}
}

func withDetail(text, detail string) string {
	if d := strings.TrimSpace(detail); d != "" {
		return text + ": " + d
	}
	return text
}
