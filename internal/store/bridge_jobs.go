package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

var ErrAgentJobNotFound = errors.New("store: agent job not found")
var ErrAgentJobLeaseMismatch = errors.New("store: agent job lease mismatch")
var ErrBridgeNotFound = errors.New("store: bridge not found")

// ErrAgentJobIDTaken refuses a new job whose id another job (of another
// origin, in any tenant) already holds: job ids are global.
var ErrAgentJobIDTaken = errors.New("store: agent job id is taken")

// ErrBridgeOwnerMismatch rejects a heartbeat for a bridge id already registered
// to a different owner. A bridge id is bound to the member who first registered
// it; re-owning it would let any member take over another member's bridge
// (its advertised agents and every job it claims).
var ErrBridgeOwnerMismatch = errors.New("store: bridge is registered to another owner")

type AgentJobStatus string

const (
	AgentJobQueued    AgentJobStatus = "queued"
	AgentJobClaimed   AgentJobStatus = "claimed"
	AgentJobRunning   AgentJobStatus = "running"
	AgentJobReflowing AgentJobStatus = "reflowing"
	AgentJobReplied   AgentJobStatus = "replied"
	AgentJobFailed    AgentJobStatus = "failed"
	AgentJobTimedOut  AgentJobStatus = "timed_out"
	AgentJobCancelled AgentJobStatus = "cancelled"
)

// The job status sets, each spelled once. Go asks the methods below; SQL
// uses the IN-lists generated from the same slices (sqlJobStatuses), so a
// new status cannot be terminal in one backend and live in the other.
var (
	// terminalJobStatuses are final: never claimed, renewed, swept or
	// overwritten again.
	terminalJobStatuses = []AgentJobStatus{AgentJobReplied, AgentJobFailed, AgentJobTimedOut, AgentJobCancelled}
	// liveJobStatuses are the leased phases: a bridge holds the job.
	liveJobStatuses = []AgentJobStatus{AgentJobClaimed, AgentJobRunning, AgentJobReflowing}
	// pendingJobStatuses have not started yet: queued, or claimed by a
	// bridge that has not reported it running.
	pendingJobStatuses = []AgentJobStatus{AgentJobQueued, AgentJobClaimed}
	// activeJobStatuses are every status that is not terminal.
	activeJobStatuses = append([]AgentJobStatus{AgentJobQueued}, liveJobStatuses...)
)

// The SQL IN-lists of the status sets ('a','b',…).
var (
	sqlTerminalJobStatuses = sqlJobStatuses(terminalJobStatuses)
	sqlLiveJobStatuses     = sqlJobStatuses(liveJobStatuses)
	sqlPendingJobStatuses  = sqlJobStatuses(pendingJobStatuses)
	sqlActiveJobStatuses   = sqlJobStatuses(activeJobStatuses)
	// sqlClaimableJob is claimableJob over agent_jobs columns; its one
	// argument is the claim's nowMs. idx_agent_jobs_owner_status (owner,
	// status, core, provider, not_before_ms) serves it for an owner's
	// bridge; the queued set a serve-all claim reads is small.
	sqlClaimableJob = "status = '" + string(AgentJobQueued) + "' AND not_before_ms <= ?"
)

// sqlJobStatuses renders a status set as a SQL IN-list body.
func sqlJobStatuses(set []AgentJobStatus) string {
	quoted := make([]string, len(set))
	for i, s := range set {
		quoted[i] = "'" + string(s) + "'"
	}
	return strings.Join(quoted, ",")
}

// Terminal reports whether a job reached a final state. A terminal job is never
// claimed, renewed, swept, or overwritten again.
func (s AgentJobStatus) Terminal() bool { return slices.Contains(terminalJobStatuses, s) }

// Live reports a leased phase (claimed, running or reflowing).
func (s AgentJobStatus) Live() bool { return slices.Contains(liveJobStatuses, s) }

// Pending reports a job not started yet (queued, or claimed and not running).
func (s AgentJobStatus) Pending() bool { return slices.Contains(pendingJobStatuses, s) }

// claimableJob reports whether a bridge may take j at nowMs: it is queued
// and its retry delay has passed (NotBeforeMs; SQL sqlClaimableJob). A live
// job whose lease lapsed is not: the sweep puts it back on the queue first
// and records the lost attempt (SweepRequest.swept).
func claimableJob(j AgentJob, nowMs int64) bool {
	return j.Status == AgentJobQueued && j.NotBeforeMs <= nowMs
}

type AgentJobFailureReason string

const (
	FailureNoBridgeOnline      AgentJobFailureReason = "no_bridge_online"
	FailureAgentNotAdvertised  AgentJobFailureReason = "agent_not_advertised"
	FailureBorrowNotAuthorized AgentJobFailureReason = "borrow_not_authorized"
	// FailureCoreUnavailable: the core could not be reached before any
	// agent work started (retryable).
	FailureCoreUnavailable AgentJobFailureReason = "core_unavailable"
	// FailureRunnerStartFailed: the request that starts the turn provably
	// never took effect (retryable).
	FailureRunnerStartFailed AgentJobFailureReason = "runner_start_failed"
	// FailureRunnerFailed: the turn started, or may have (an ambiguous
	// start); final: a retry would run the prompt twice.
	FailureRunnerFailed AgentJobFailureReason = "runner_failed"
	// FailureAgentError: the agent or its core reported the started turn
	// failed (Core Protocol PollTurn failed + failMessage: a CLI that could
	// not start, a provider error); final for the same reason.
	FailureAgentError AgentJobFailureReason = "agent_error"
	// FailureContextOverflow: the conversation grew past the model's
	// context window, and again in the fresh session the bridge continued
	// it in (a turn that overflows once is retried once by the bridge
	// itself); final.
	FailureContextOverflow AgentJobFailureReason = "context_overflow"
	// FailureContextReset: the conversation grew past the model's context
	// window after the turn had already acted (a tool step, a reply, a
	// file), so it was not rerun: the conversation was reset and the
	// request can be sent again; final.
	FailureContextReset AgentJobFailureReason = "context_reset"
	// FailureLeaseExpired: the bridge holding the job stopped renewing its
	// lease (it quit, restarted or lost the network) and the sweep took the
	// job back; retryable, final once the attempts are spent.
	FailureLeaseExpired AgentJobFailureReason = "lease_expired"
	// FailureSandboxStarting: the agent sandbox the drive must run in was
	// not up yet (the desktop is still starting its machine); nothing ran,
	// so it retries.
	FailureSandboxStarting AgentJobFailureReason = "sandbox_starting"
	// FailureReplyTimeout: the turn ran and produced no visible reply;
	// final for the same reason.
	FailureReplyTimeout AgentJobFailureReason = "reply_timeout"
	FailureCancelled    AgentJobFailureReason = "cancelled"
	// FailureBudgetExhausted: the agent owner's (or the tenant's) monthly
	// usage budget is reached with hard stop on (R12); final, not retried.
	FailureBudgetExhausted AgentJobFailureReason = "budget_exhausted"
	// FailureBudgetUnavailable: the hard-stop budget could not be read at
	// claim time, so the job was refused rather than run unbudgeted;
	// retryable (nothing ran).
	FailureBudgetUnavailable AgentJobFailureReason = "budget_unavailable"
	// FailureSandboxRequired: the tenant requires borrowed drives to run in
	// the owner's agent sandbox and it is off / not ready / unsupported by
	// the agent's core; final (a new message retries once the owner fixes it).
	FailureSandboxRequired AgentJobFailureReason = "sandbox_required"
	// FailureContentUnavailable: the job names an expert its requester can
	// no longer wear; final.
	FailureContentUnavailable AgentJobFailureReason = "content_unavailable"
	// FailureTeamUnsupported: a team job on an agent that cannot use the
	// room tools; final.
	FailureTeamUnsupported AgentJobFailureReason = "team_unsupported"
	// FailureInvalidJob: the job itself cannot be driven (no text and no
	// attachment); final.
	FailureInvalidJob AgentJobFailureReason = "invalid_job"
)

// retryBackoff is how long a requeued job waits before its next attempt:
// Base after the first failed attempt, doubling each attempt, at most Max.
type retryBackoff struct{ Base, Max time.Duration }

// retryBackoffs is the one retry table: a reason retries exactly when it
// has a row here (Retryable), and its row is the wait before the next
// attempt (retryDelayMs). Only reasons where no agent work can have started
// are listed: the core was unreachable, the sandbox was still starting (a
// booting machine takes about a minute), the start request provably never
// took effect, the budget could not be checked, or the lease lapsed without
// a report. Everything else is terminal on the first failure: a
// deterministic refusal fails the same way again, and a started or
// ambiguous turn would run the member's prompt twice.
var retryBackoffs = map[AgentJobFailureReason]retryBackoff{
	FailureSandboxStarting:   {Base: 15 * time.Second, Max: time.Minute},
	FailureCoreUnavailable:   {Base: 10 * time.Second, Max: time.Minute},
	FailureRunnerStartFailed: {Base: 10 * time.Second, Max: time.Minute},
	FailureBudgetUnavailable: {Base: 10 * time.Second, Max: time.Minute},
	FailureLeaseExpired:      {Base: 5 * time.Second, Max: 5 * time.Second},
}

// Retryable reports whether a failed job may be attempted again
// (retryBackoffs).
func (r AgentJobFailureReason) Retryable() bool {
	_, ok := retryBackoffs[r]
	return ok
}

// retryDelayMs is how long a job whose attempt-th attempt failed for reason
// waits before it may be claimed again (retryBackoffs; 0 for a reason that
// does not retry). SQL reads it through sqlRetryDelay.
func retryDelayMs(reason AgentJobFailureReason, attempt int) int64 {
	b, ok := retryBackoffs[reason]
	if !ok {
		return 0
	}
	d := b.Base
	for i := 1; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	return min(d, b.Max).Milliseconds()
}

// sqlRetryDelay is retryDelayMs(reason, attempt) over the agent_jobs
// attempt column, generated from the same table: a CASE over the attempts
// before the delay reaches its cap.
func sqlRetryDelay(reason AgentJobFailureReason) string {
	capMs := retryDelayMs(reason, math.MaxInt32)
	var b strings.Builder
	for k := 1; retryDelayMs(reason, k) < capMs; k++ {
		fmt.Fprintf(&b, " WHEN attempt <= %d THEN %d", k, retryDelayMs(reason, k))
	}
	if b.Len() == 0 {
		return strconv.FormatInt(capMs, 10)
	}
	return "(CASE" + b.String() + " ELSE " + strconv.FormatInt(capMs, 10) + " END)"
}

// Text is the ONE sentence a member reads about a failed job (a room
// notice, a notification), naming the agent (its provider; "agent" when
// unknown). Reasons the bridge refuses with its own explanation fall back
// to the generic sentence.
func (r AgentJobFailureReason) Text(agent string) string {
	name := "agent"
	if agent = strings.TrimSpace(agent); agent != "" {
		name = agent + " agent"
	}
	the := "The " + name
	switch r {
	case FailureNoBridgeOnline, FailureAgentNotAdvertised:
		return the + " is offline: no bridge picked up this request."
	case FailureBorrowNotAuthorized:
		return "The owner has not authorized borrowing the " + name + "."
	case FailureLeaseExpired:
		return the + " stopped responding and the request was retried without success."
	case FailureSandboxStarting:
		return the + " did not run: its agent sandbox was still starting."
	case FailureReplyTimeout:
		return the + " stopped responding."
	case FailureAgentError:
		return the + " reported an error and stopped."
	case FailureContextReset:
		return the + " stopped: its conversation grew past its model's context window while it was working, so the conversation was reset. What it did so far stays; send the request again to continue."
	case FailureContextOverflow:
		return the + " stopped: the request needs more than its model can read at once, even in a fresh session. Split it into smaller steps, or pick a model with a larger context window."
	case FailureBudgetExhausted:
		return the + " did not run: a monthly budget is reached."
	case FailureBudgetUnavailable:
		return the + " did not run: its usage budget could not be checked."
	case FailureSandboxRequired:
		return the + " did not run: this organization requires a sandbox for borrowed agents."
	case FailureCancelled:
		return "The request was cancelled."
	default:
		return the + " could not complete this request."
	}
}

type BridgeStatus string

// BridgeOfflineAfterMs is how long a bridge may go without a heartbeat before
// its advertised capabilities stop counting. The worker heartbeats every 10s, so
// this tolerates two missed beats before a bridge is treated as gone.
const BridgeOfflineAfterMs = 30_000

const (
	BridgeOnline   BridgeStatus = "online"
	BridgeOffline  BridgeStatus = "offline"
	BridgeDraining BridgeStatus = "draining"
)

// ErrBridgeStatusInvalid refuses a heartbeat whose status is none of
// BridgeOnline, BridgeOffline, BridgeDraining.
var ErrBridgeStatusInvalid = errors.New("store: unknown bridge status")

// valid reports a known bridge status.
func (s BridgeStatus) valid() bool {
	switch s {
	case BridgeOnline, BridgeOffline, BridgeDraining:
		return true
	}
	return false
}

// BridgeLive is the one bridge-liveness rule: a bridge is live at nowMs when
// it reports online (not draining or offline) and its last heartbeat has not
// expired; a heartbeat expires BridgeOfflineAfterMs after it was seen
// (liveAt). A bridge with no heartbeat row is never live. SQL spells it
// liveBridgePred.
func BridgeLive(hb BridgeHeartbeat, nowMs int64) bool {
	return hb.Status == BridgeOnline && liveAt(hb.LastSeenAtMs+BridgeOfflineAfterMs, nowMs)
}

// liveBridgePred is BridgeLive over bridge_heartbeats aliased h; its one
// argument is bridgeLiveSince(nowMs). The caller always passes nowMs: a
// store never reads the clock to decide liveness.
const liveBridgePred = "(h.status = '" + string(BridgeOnline) + "' AND h.last_seen_at_ms > ?)"

// bridgeLiveSince is liveBridgePred's argument at nowMs.
func bridgeLiveSince(nowMs int64) int64 { return nowMs - BridgeOfflineAfterMs }

// AgentJob is a durable request for a bridge to drive one agent turn.
type AgentJob struct {
	JobID           string   `json:"jobId"`
	TenantID        string   `json:"tenantId"`
	RoomID          string   `json:"roomId"`
	ChannelID       string   `json:"channelId"`
	MessageSeq      int64    `json:"messageSeq"`
	ClientMsgID     string   `json:"clientMsgId"`
	RequesterUserID string   `json:"requesterUserId"`
	OwnerUserID     string   `json:"ownerUserId"`
	Core            string   `json:"core"`
	Provider        string   `json:"provider"`
	TargetID        string   `json:"targetId"`
	MessageText     string   `json:"messageText"`
	AttachmentIDs   []string `json:"attachmentIds,omitempty"`
	// FileRef is the room file the request edits (the message's fileRef,
	// validated when it was posted); nil = none.
	FileRef          *wire.FileRef         `json:"fileRef,omitempty"`
	Model            string                `json:"model,omitempty"`
	ReasoningEffort  string                `json:"reasoningEffort,omitempty"`
	Status           AgentJobStatus        `json:"status"`
	FailureReason    AgentJobFailureReason `json:"failureReason"`
	Attempt          int                   `json:"attempt"`
	LeaseBridgeID    string                `json:"leaseBridgeId"`
	LeaseExpiresAtMs int64                 `json:"leaseExpiresAtMs"`
	CreatedAtMs      int64                 `json:"createdAtMs"`
	UpdatedAtMs      int64                 `json:"updatedAtMs"`
	// ClaimedAtMs is when a bridge last claimed the job (0 = never claimed).
	ClaimedAtMs     int64           `json:"claimedAtMs,omitempty"`
	StartedAtMs     int64           `json:"startedAtMs"`
	FinishedAtMs    int64           `json:"finishedAtMs"`
	DiagnosticsJSON json.RawMessage `json:"diagnostics"`
	// DispatchedByJobID is the job whose agent dispatched this one through
	// the room tools (ask_agent); "" for a job a person (or a schedule)
	// started. DispatchDepth counts the hops from that person's message
	// (0 = not dispatched). See agent_dispatch.go.
	DispatchedByJobID string `json:"dispatchedByJobId,omitempty"`
	DispatchDepth     int    `json:"dispatchDepth,omitempty"`
	// ExpertID / ExpertVersion / ExpertName are the expert the chat asked
	// the agent to wear (docs/prd/2026-10-12-experts-and-teams.md): the
	// version is pinned when the chat is posted, and the name is kept for
	// the run record ("expert X v1.2 on agent Y"). Empty = no expert.
	ExpertID      string `json:"expertId,omitempty"`
	ExpertVersion string `json:"expertVersion,omitempty"`
	ExpertName    string `json:"expertName,omitempty"`
	// RoomExpertID / RoomExpertVersion / RoomExpertName are the room's
	// expert the turn wore, recorded when the bridge running the job
	// resolves what it wears (agent_room_expert.go). Empty = none.
	RoomExpertID      string `json:"roomExpertId,omitempty"`
	RoomExpertVersion string `json:"roomExpertVersion,omitempty"`
	RoomExpertName    string `json:"roomExpertName,omitempty"`
	// Attempts are the job's earlier attempts that failed and were
	// requeued, oldest first, at most MaxRecordedAttempts (requeued).
	Attempts []AgentJobAttempt `json:"attempts,omitempty"`
	// NotBeforeMs is when a requeued job may be claimed again (its retry
	// delay, retryDelayMs); 0 = at once.
	NotBeforeMs int64 `json:"notBeforeMs,omitempty"`
}

// BridgeHeartbeat records one desktop bridge's liveness in a tenant.
type BridgeHeartbeat struct {
	BridgeID       string
	TenantID       string
	OwnerUserID    string
	AppInstanceID  string
	Version        string
	Status         BridgeStatus
	LastSeenAtMs   int64
	MetadataJSON   json.RawMessage
	ServeAllOwners bool
}

// BridgeAgent records a concrete agent target advertised by one bridge.
type BridgeAgent struct {
	BridgeID         string
	TenantID         string
	OwnerUserID      string
	Core             string
	Provider         string
	TargetID         string
	ModelOptionsJSON json.RawMessage
	Disabled         bool
	LastSeenAtMs     int64
}

// ClaimRequest takes up to MaxJobs claimable jobs (oldest first) for a live
// bridge of OwnerUserID.
type ClaimRequest struct {
	BridgeID    string
	OwnerUserID string
	// MaxJobs is clamped to 1..MaxJobsPerClaim (0 = 1).
	MaxJobs int
	NowMs   int64
	// LeaseMs 0 = DefaultLeaseMs.
	LeaseMs        int64
	ServeAllOwners bool
}

// MaxJobsPerClaim bounds one claim.
const MaxJobsPerClaim = 32

// normalize applies the request defaults, once for both backends.
func (r ClaimRequest) normalize() ClaimRequest {
	r.MaxJobs = clampLimit(r.MaxJobs, 1, MaxJobsPerClaim)
	r.LeaseMs = leaseMs(r.LeaseMs)
	return r
}

// through reports whether the claim may go through hb: the bridge belongs to
// the claiming owner (a bridge id is not a credential) and is live.
func (r ClaimRequest) through(hb BridgeHeartbeat) bool {
	return hb.OwnerUserID == r.OwnerUserID && BridgeLive(hb, r.NowMs)
}

// claimed is j leased to the claiming bridge (both backends write it).
func (r ClaimRequest) claimed(j AgentJob) AgentJob {
	j.Status = AgentJobClaimed
	j.LeaseBridgeID = r.BridgeID
	j.LeaseExpiresAtMs = r.NowMs + r.LeaseMs
	j.Attempt++
	j.UpdatedAtMs = r.NowMs
	j.ClaimedAtMs = r.NowMs
	return j
}

// leaseMs resolves a caller's lease length, falling back to DefaultLeaseMs.
func leaseMs(ms int64) int64 {
	if ms <= 0 {
		return DefaultLeaseMs
	}
	return ms
}

type JobStatusUpdate struct {
	JobID           string
	BridgeID        string
	Status          AgentJobStatus
	FailureReason   AgentJobFailureReason
	DiagnosticsJSON json.RawMessage
	NowMs           int64
	// LeaseMs extends the lease when the new status is non-terminal, so a turn
	// that keeps reporting progress is never reclaimed under its own worker.
	// 0 = DefaultLeaseMs (a progress report always renews the lease).
	LeaseMs int64
	// MaxAttempts bounds how often a retryable failure is requeued instead of
	// becoming terminal. 0 uses DefaultMaxJobAttempts.
	MaxAttempts int
}

// normalize applies the update defaults, once for both backends.
func (u JobStatusUpdate) normalize() JobStatusUpdate {
	u.LeaseMs = leaseMs(u.LeaseMs)
	u.MaxAttempts = maxAttempts(u.MaxAttempts)
	return u
}

// requeues reports a retryable failure: it goes back on the queue while the
// job has attempts left (retries).
func (u JobStatusUpdate) requeues() bool {
	return u.Status == AgentJobFailed && u.FailureReason.Retryable()
}

// retries is the one requeue rule: a job that failed for reason on its
// attempt-th attempt goes back on the queue when the reason is retryable
// and the attempt budget is not spent. SQL guards the same budget with
// "attempt < ?".
func retries(reason AgentJobFailureReason, attempt, maxAttempts int) bool {
	return reason.Retryable() && attempt < maxAttempts
}

// AgentJobAttempt is one earlier attempt of a job: the attempt number, why
// it failed (a retryable reason) and when it was requeued.
type AgentJobAttempt struct {
	Attempt int                   `json:"attempt"`
	Reason  AgentJobFailureReason `json:"reason"`
	AtMs    int64                 `json:"atMs"`
}

// MaxRecordedAttempts bounds a job's attempt history (the newest are kept).
const MaxRecordedAttempts = 5

// requeued is j put back on the queue after its current attempt failed for
// reason at nowMs: queued, lease released, not claimable before its retry
// delay (retryDelayMs), the attempt recorded (the last MaxRecordedAttempts
// kept). It is the one requeue, for a bridge's retryable report and the
// sweep's lapsed lease alike; the MySQL store writes it with sqlRequeue in
// the statement that requeues (TestJobRequeueOwned). Diagnostics are the
// caller's.
func (j AgentJob) requeued(reason AgentJobFailureReason, nowMs int64) AgentJob {
	j.Status = AgentJobQueued
	j.FailureReason = reason
	j.UpdatedAtMs = nowMs
	j.LeaseBridgeID = ""
	j.LeaseExpiresAtMs = 0
	j.NotBeforeMs = nowMs + retryDelayMs(reason, j.Attempt)
	kept := j.Attempts
	if len(kept) >= MaxRecordedAttempts {
		kept = kept[len(kept)-MaxRecordedAttempts+1:]
	}
	j.Attempts = append(slices.Clone(kept), AgentJobAttempt{Attempt: j.Attempt, Reason: reason, AtMs: nowMs})
	return j
}

// sqlRequeue is AgentJob.requeued over agent_jobs columns: the SET list of
// a requeue for reason, its arguments requeueArgs. The retry delay (from
// the attempt column, sqlRetryDelay) and the attempt record (appended to
// attempts_json, the oldest dropped at the cap) are written in the same
// statement, so no claim or report can slip between the requeue and them.
func sqlRequeue(reason AgentJobFailureReason) string {
	return "status='" + string(AgentJobQueued) + "', failure_reason=?, updated_at_ms=?, lease_bridge_id='', lease_expires_at_ms=0, " +
		"not_before_ms=? + " + sqlRetryDelay(reason) + ", " +
		"attempts_json=JSON_ARRAY_APPEND(IF(JSON_LENGTH(COALESCE(attempts_json, JSON_ARRAY())) >= " + strconv.Itoa(MaxRecordedAttempts) +
		", JSON_REMOVE(attempts_json, '$[0]'), COALESCE(attempts_json, JSON_ARRAY())), '$', JSON_OBJECT('attempt', attempt, 'reason', ?, 'atMs', ?))"
}

// requeueArgs are sqlRequeue's arguments.
func requeueArgs(reason AgentJobFailureReason, nowMs int64) []any {
	return []any{string(reason), nowMs, nowMs, string(reason), nowMs}
}

// leaseExpiresAt is the job's lease after the update: none once terminal,
// else renewed from NowMs.
func (u JobStatusUpdate) leaseExpiresAt() int64 {
	if u.Status.Terminal() {
		return 0
	}
	return u.NowMs + u.LeaseMs
}

// LeaseRenewal extends a running job's lease without changing its status. A
// worker renews while the agent turn is still alive; without it a turn longer
// than the lease becomes claimable again and the same prompt runs twice.
type LeaseRenewal struct {
	JobID    string
	BridgeID string
	NowMs    int64
	// LeaseMs 0 = DefaultLeaseMs.
	LeaseMs int64
}

// normalize applies the renewal defaults, once for both backends.
func (r LeaseRenewal) normalize() LeaseRenewal {
	r.LeaseMs = leaseMs(r.LeaseMs)
	return r
}

// SweepRequest takes back jobs no worker is finishing: queued past
// QueuedTimeoutMs (nothing advertised the agent) fail, and an expired lease
// goes back on the queue while attempts remain (else fails). Sweeping is
// cross-tenant maintenance and carries no tenant scope.
type SweepRequest struct {
	NowMs int64
	// QueuedTimeoutMs 0 = DefaultQueuedTimeoutMs; MaxAttempts 0 =
	// DefaultMaxJobAttempts.
	QueuedTimeoutMs int64
	MaxAttempts     int
}

// normalize applies the sweep defaults, once for both backends.
func (r SweepRequest) normalize() SweepRequest {
	if r.QueuedTimeoutMs <= 0 {
		r.QueuedTimeoutMs = DefaultQueuedTimeoutMs
	}
	r.MaxAttempts = maxAttempts(r.MaxAttempts)
	return r
}

// sweepReason is why a sweep takes j at the (normalized) request's time,
// or "" when j stays: queued for the queue timeout (no bridge took it since
// it became claimable: entered the queue, created or requeued, and its
// retry delay passed), or live with an expired lease (its worker stopped
// renewing it).
func (r SweepRequest) sweepReason(j AgentJob) AgentJobFailureReason {
	switch {
	case j.Status == AgentJobQueued && max(j.UpdatedAtMs, j.NotBeforeMs) <= r.queuedBefore():
		return FailureNoBridgeOnline
	case j.Status.Live() && !liveAt(j.LeaseExpiresAtMs, r.NowMs):
		return FailureLeaseExpired
	}
	return ""
}

// swept is a stuck job as the sweep leaves it (both backends write it):
// requeued while its reason retries, else failed.
func (r SweepRequest) swept(j AgentJob) AgentJob {
	reason := r.sweepReason(j)
	if retries(reason, j.Attempt, r.MaxAttempts) {
		return j.requeued(reason, r.NowMs)
	}
	j.FailureReason = reason
	j.Status = AgentJobFailed
	j.UpdatedAtMs = r.NowMs
	j.FinishedAtMs = r.NowMs
	j.LeaseExpiresAtMs = 0
	return j
}

// queuedBefore: a job queued at or before it has waited out the queue.
func (r SweepRequest) queuedBefore() int64 { return r.NowMs - r.QueuedTimeoutMs }

// MaxSweptJobs bounds the jobs one SweepAgentJobs call fails (oldest first);
// the next pass takes the rest.
const MaxSweptJobs = 200

const (
	// DefaultLeaseMs bounds how long a claim survives without progress before
	// another bridge may take the job over.
	DefaultLeaseMs = 60_000
	// DefaultQueuedTimeoutMs fails a job no bridge claimed this long after it
	// entered the queue (created or requeued), so a member sees
	// no_bridge_online instead of a job queued forever.
	DefaultQueuedTimeoutMs = 120_000
	// DefaultMaxJobAttempts caps a job's attempts (retryable failures and
	// lapsed leases), so a job that kills its worker every time fails
	// instead of looping.
	DefaultMaxJobAttempts = 3
)

// BridgeJobStore is the durable job queue the desktop bridges drain. Job ids
// and bridge ids are global: a job lives in its room's tenant, and a bridge
// (bound to its first owner) claims and reports jobs of every tenant its
// owner is in, so the bridge-driven calls (claim, status, lease, the
// owner-wide reads) carry no tenant scope; the room-facing calls (put, list,
// cancel) are tenant-scoped.
type BridgeJobStore interface {
	// PutAgentJob enqueues a job, idempotently by origin (tenant, room,
	// message, owner, core, provider): a re-put of an origin returns the
	// stored job. A job id held by another origin is ErrAgentJobIDTaken.
	PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error)
	// ListAgentJobs returns the ctx tenant's newest jobs of a room (and
	// channel, when set), newest first (created_at_ms, then job id,
	// descending); limit is clamped to 1..MaxAgentJobsListed (0 =
	// DefaultAgentJobsListed).
	ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) ([]AgentJob, error)
	// ClaimAgentJobs leases up to req.MaxJobs claimable jobs to a live bridge
	// of req.OwnerUserID (ClaimRequest.through), oldest first (created_at_ms,
	// then job id), matching what the bridge advertises (core, provider).
	// Nothing (an empty list) when the bridge is not the owner's or not live.
	ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error)
	// UpdateAgentJobStatus moves a job the reporting bridge holds:
	// ErrAgentJobNotFound for an unknown job, ErrAgentJobLeaseMismatch when
	// another bridge holds it or it is terminal.
	UpdateAgentJobStatus(ctx context.Context, upd JobStatusUpdate) (AgentJob, error)
	// CancelAgentJob cancels a live job of the ctx tenant (and room, when
	// set): ErrAgentJobNotFound / ErrAgentJobLeaseMismatch (terminal).
	CancelAgentJob(ctx context.Context, req CancelRequest) (AgentJob, error)
	// RenewAgentJobLease extends the lease the bridge holds (errors as
	// UpdateAgentJobStatus).
	RenewAgentJobLease(ctx context.Context, ren LeaseRenewal) error
	// HeldAgentJob returns a live job the bridge holds, from any tenant (the
	// lease authorizes it, as RenewAgentJobLease): ErrAgentJobNotFound /
	// ErrAgentJobLeaseMismatch (another bridge holds it, or it is terminal).
	HeldAgentJob(ctx context.Context, jobID, bridgeID string) (AgentJob, error)
	// SweepAgentJobs takes back up to MaxSweptJobs stuck jobs (oldest
	// first) of every tenant, requeued or failed (SweepRequest.swept), and
	// returns them.
	SweepAgentJobs(ctx context.Context, req SweepRequest) ([]AgentJob, error)
	// PutBridgeHeartbeat records a bridge's liveness (the bridge moves to the
	// ctx tenant); ErrBridgeOwnerMismatch when another owner holds the id,
	// ErrBridgeStatusInvalid for an unknown status.
	PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error
	// BridgeOwner is the one answer to "whose bridge is this": the member a
	// bridge id is registered to, from any tenant (ErrBridgeNotFound when it
	// never heartbeat).
	BridgeOwner(ctx context.Context, bridgeID string) (string, error)
	// ReplaceBridgeAgents replaces what a bridge advertises; one row per
	// (core, provider, target id).
	ReplaceBridgeAgents(ctx context.Context, bridgeID string, agents []BridgeAgent) error
	// ListBridgeAgentsForOwner returns one owner's enabled agents advertised
	// by a bridge live at nowMs, across all rooms/tenants (owner identity is
	// global), ordered by (core, provider, target id, bridge id): the "my
	// agents" surface for a member's own view.
	ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string, nowMs int64) ([]BridgeAgent, error)
	// LatestBridgeForOwner returns the owner's most recently seen bridge
	// heartbeat across tenants (owner identity is global), and false when no
	// desktop bridge of theirs ever connected. Heartbeat rows are never
	// deleted, so this tells "installed but closed" from "never installed".
	LatestBridgeForOwner(ctx context.Context, ownerUserID string) (BridgeHeartbeat, bool, error)
	// CountOnlineBridges counts bridges of every tenant live at nowMs
	// (BridgeLive; metrics).
	CountOnlineBridges(ctx context.Context, nowMs int64) (int, error)
	// PendingAgentJobs counts one owner's jobs not started yet (queued, or
	// claimed and not running), per (core, provider), across every tenant (a
	// host serves rooms of every organization): the work ahead of a new job
	// for that agent (the platform agents' "queued").
	PendingAgentJobs(ctx context.Context, ownerUserID string) ([]AgentJobCount, error)
}

// AgentJobCount is a job count of one agent (core, provider).
type AgentJobCount struct {
	Core     string
	Provider string
	Count    int
}

// maxAttempts resolves a caller's attempt budget, falling back to the default.
func maxAttempts(n int) int {
	if n <= 0 {
		return DefaultMaxJobAttempts
	}
	return n
}

// CancelRequest stops a job a member no longer wants. Cancellation is a room
// action, not a worker report: it must succeed whether the job is still queued
// or already leased, so the caller is identified by user rather than bridge.
type CancelRequest struct {
	JobID           string
	RoomID          string
	RequestedByUser string
	NowMs           int64
}

// The ListAgentJobs page size.
const (
	DefaultAgentJobsListed = 100
	MaxAgentJobsListed     = 200
)

// normalize fills a new job's defaults for tenant tid, once for both
// backends. Queue lifetime is decided from CreatedAtMs, so a missing one is
// the server clock: a zero value would read as "queued since the epoch" and
// be swept the moment it is enqueued.
func (j AgentJob) normalize(tid string) AgentJob {
	j.TenantID = tid
	if j.Status == "" {
		j.Status = AgentJobQueued
	}
	if j.CreatedAtMs <= 0 {
		j.CreatedAtMs = time.Now().UnixMilli()
	}
	if j.UpdatedAtMs <= 0 {
		j.UpdatedAtMs = j.CreatedAtMs
	}
	return j
}

// sortJobsOldestFirst orders jobs by (created_at_ms, job_id) ascending: the
// claim and sweep order.
func sortJobsOldestFirst(js []AgentJob) {
	sort.Slice(js, func(i, k int) bool {
		if js[i].CreatedAtMs != js[k].CreatedAtMs {
			return js[i].CreatedAtMs < js[k].CreatedAtMs
		}
		return js[i].JobID < js[k].JobID
	})
}

// sortJobsNewestFirst orders jobs by (created_at_ms, job_id) descending: the
// listing order.
func sortJobsNewestFirst(js []AgentJob) {
	sort.Slice(js, func(i, k int) bool {
		if js[i].CreatedAtMs != js[k].CreatedAtMs {
			return js[i].CreatedAtMs > js[k].CreatedAtMs
		}
		return js[i].JobID > js[k].JobID
	})
}

// sortBridgeAgents orders advertised agents by (owner, core, provider,
// target id, bridge id): the order of every advertisement listing.
func sortBridgeAgents(as []BridgeAgent) {
	sort.Slice(as, func(i, k int) bool {
		a, b := as[i], as[k]
		for _, c := range [][2]string{{a.OwnerUserID, b.OwnerUserID}, {a.Core, b.Core}, {a.Provider, b.Provider}, {a.TargetID, b.TargetID}} {
			if c[0] != c[1] {
				return c[0] < c[1]
			}
		}
		return a.BridgeID < b.BridgeID
	})
}

type MemoryBridgeJobs struct {
	mu         sync.Mutex
	jobs       map[string]AgentJob
	heartbeats map[string]BridgeHeartbeat
	// agents is each bridge's advertisement by bridgeAgentKey (the
	// bridge_agents primary key after the bridge id).
	agents map[string]map[string]BridgeAgent
	origin map[string]string
}

func NewMemoryBridgeJobs() *MemoryBridgeJobs {
	return &MemoryBridgeJobs{jobs: map[string]AgentJob{}, heartbeats: map[string]BridgeHeartbeat{}, agents: map[string]map[string]BridgeAgent{}, origin: map[string]string{}}
}

var _ BridgeJobStore = (*MemoryBridgeJobs)(nil)

func jobOrigin(tid string, j AgentJob) string {
	return tid + "\x00" + j.RoomID + "\x00" + strconv.FormatInt(j.MessageSeq, 10) + "\x00" + j.OwnerUserID + "\x00" + j.Core + "\x00" + j.Provider
}

// bridgeAgentKey is one advertised target of a bridge: several targets share
// a core/provider (codex, claude-code, opencode all run on one core).
func bridgeAgentKey(a BridgeAgent) string {
	return a.Core + "\x00" + a.Provider + "\x00" + a.TargetID
}

func (s *MemoryBridgeJobs) PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j = j.normalize(tid)
	key := jobOrigin(tid, j)
	if existingID := s.origin[key]; existingID != "" {
		return s.jobs[existingID], nil
	}
	if _, taken := s.jobs[j.JobID]; taken {
		return AgentJob{}, ErrAgentJobIDTaken
	}
	s.jobs[j.JobID] = j
	s.origin[key] = j.JobID
	return j, nil
}

func (s *MemoryBridgeJobs) ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	limit = clampLimit(limit, DefaultAgentJobsListed, MaxAgentJobsListed)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AgentJob{}
	for _, j := range s.jobs {
		if j.TenantID != tid || j.RoomID != roomID {
			continue
		}
		if channelID != "" && j.ChannelID != channelID {
			continue
		}
		out = append(out, j)
	}
	sortJobsNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryBridgeJobs) PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if !hb.Status.valid() {
		return ErrBridgeStatusInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.heartbeats[hb.BridgeID]; ok && prev.OwnerUserID != hb.OwnerUserID {
		return ErrBridgeOwnerMismatch
	}
	hb.TenantID = tid
	s.heartbeats[hb.BridgeID] = hb
	return nil
}

func (s *MemoryBridgeJobs) ReplaceBridgeAgents(ctx context.Context, bridgeID string, agents []BridgeAgent) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]BridgeAgent{}
	for _, a := range agents {
		a.TenantID = tid
		a.BridgeID = bridgeID
		m[bridgeAgentKey(a)] = a
	}
	s.agents[bridgeID] = m
	return nil
}

// ClaimAgentJobs leases claimable jobs to a live bridge (see BridgeJobStore).
// Capability + job matching is cross-tenant: a job lives in its room's
// tenant, which may differ from the tenant the bridge advertised in. Bridge
// ids are global, so ownership + liveness (not tenant) authorize the claim;
// the returned job carries its own tenant for the bridge to drive in.
func (s *MemoryBridgeJobs) ClaimAgentJobs(_ context.Context, req ClaimRequest) ([]AgentJob, error) {
	req = req.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AgentJob{}
	if hb, ok := s.heartbeats[req.BridgeID]; !ok || !req.through(hb) {
		return out, nil
	}
	capable := map[string]bool{}
	for _, a := range s.agents[req.BridgeID] {
		if a.OwnerUserID == req.OwnerUserID && !a.Disabled {
			capable[a.Core+"\x00"+a.Provider] = true
		}
	}
	var cands []AgentJob
	for _, j := range s.jobs {
		if (req.ServeAllOwners || j.OwnerUserID == req.OwnerUserID) && capable[j.Core+"\x00"+j.Provider] && claimableJob(j, req.NowMs) {
			cands = append(cands, j)
		}
	}
	sortJobsOldestFirst(cands)
	if len(cands) > req.MaxJobs {
		cands = cands[:req.MaxJobs]
	}
	for _, j := range cands {
		j = req.claimed(j)
		s.jobs[j.JobID] = j
		out = append(out, j)
	}
	return out, nil
}

// heldJobLocked returns the job a bridge-driven write may move: any tenant's
// (job ids are global; the lease authorizes), held by bridgeID and not
// terminal. Caller holds s.mu.
func (s *MemoryBridgeJobs) heldJobLocked(jobID, bridgeID string) (AgentJob, error) {
	j, ok := s.jobs[jobID]
	if !ok {
		return AgentJob{}, ErrAgentJobNotFound
	}
	// Only the bridge holding the lease may move a job, and a terminal job is
	// never rewritten: a late report from a superseded worker must not overwrite
	// the outcome the current worker already recorded.
	if j.LeaseBridgeID != bridgeID || j.Status.Terminal() {
		return AgentJob{}, ErrAgentJobLeaseMismatch
	}
	return j, nil
}

func (s *MemoryBridgeJobs) UpdateAgentJobStatus(_ context.Context, upd JobStatusUpdate) (AgentJob, error) {
	upd = upd.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.heldJobLocked(upd.JobID, upd.BridgeID)
	if err != nil {
		return AgentJob{}, err
	}
	// A retryable cause (the runner died, the core blipped, the turn timed out)
	// goes back on the queue while attempts remain: the next worker may well
	// succeed. A non-retryable cause — an unauthorized borrow, an agent nobody
	// advertises, an explicit cancel — is final on the first report, because no
	// retry can change the outcome.
	if upd.requeues() && retries(upd.FailureReason, j.Attempt, upd.MaxAttempts) {
		j = j.requeued(upd.FailureReason, upd.NowMs)
		j.DiagnosticsJSON = upd.DiagnosticsJSON
		s.jobs[upd.JobID] = j
		return j, nil
	}
	j.Status = upd.Status
	j.FailureReason = upd.FailureReason
	j.DiagnosticsJSON = upd.DiagnosticsJSON
	j.UpdatedAtMs = upd.NowMs
	if upd.Status == AgentJobRunning && j.StartedAtMs == 0 {
		j.StartedAtMs = upd.NowMs
	}
	if upd.Status.Terminal() {
		j.FinishedAtMs = upd.NowMs
	}
	j.LeaseExpiresAtMs = upd.leaseExpiresAt()
	s.jobs[upd.JobID] = j
	return j, nil
}

func (s *MemoryBridgeJobs) RenewAgentJobLease(_ context.Context, ren LeaseRenewal) error {
	ren = ren.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.heldJobLocked(ren.JobID, ren.BridgeID)
	if err != nil {
		return err
	}
	j.LeaseExpiresAtMs = ren.NowMs + ren.LeaseMs
	j.UpdatedAtMs = ren.NowMs
	s.jobs[ren.JobID] = j
	return nil
}

func (s *MemoryBridgeJobs) HeldAgentJob(_ context.Context, jobID, bridgeID string) (AgentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldJobLocked(jobID, bridgeID)
}

// CancelAgentJob moves a live job to cancelled. It is scoped by room so a
// caller cannot cancel a job outside the room they were authorized for, and an
// already-terminal job is refused rather than rewritten.
func (s *MemoryBridgeJobs) CancelAgentJob(ctx context.Context, req CancelRequest) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[req.JobID]
	if !ok || j.TenantID != tid || (req.RoomID != "" && j.RoomID != req.RoomID) {
		return AgentJob{}, ErrAgentJobNotFound
	}
	if j.Status.Terminal() {
		return AgentJob{}, ErrAgentJobLeaseMismatch
	}
	j.Status = AgentJobCancelled
	j.FailureReason = FailureCancelled
	j.UpdatedAtMs = req.NowMs
	j.FinishedAtMs = req.NowMs
	// Release the lease so the running worker's next report is rejected and no
	// other bridge picks the job up again.
	j.LeaseExpiresAtMs = 0
	j.LeaseBridgeID = ""
	s.jobs[req.JobID] = j
	return j, nil
}

func (s *MemoryBridgeJobs) SweepAgentJobs(_ context.Context, req SweepRequest) ([]AgentJob, error) {
	req = req.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	var stuck []AgentJob
	for _, j := range s.jobs {
		if req.sweepReason(j) != "" {
			stuck = append(stuck, j)
		}
	}
	sortJobsOldestFirst(stuck)
	if len(stuck) > MaxSweptJobs {
		stuck = stuck[:MaxSweptJobs]
	}
	out := make([]AgentJob, 0, len(stuck))
	for _, j := range stuck {
		j = req.swept(j)
		s.jobs[j.JobID] = j
		out = append(out, j)
	}
	return out, nil
}

// ListBridgeAgentsForOwner returns the owner's advertised agents of live
// bridges (memory impl; see BridgeJobStore).
func (s *MemoryBridgeJobs) ListBridgeAgentsForOwner(_ context.Context, ownerUserID string, nowMs int64) ([]BridgeAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []BridgeAgent{}
	for bridgeID, byAgent := range s.agents {
		if hb, ok := s.heartbeats[bridgeID]; !ok || !BridgeLive(hb, nowMs) {
			continue
		}
		for _, a := range byAgent {
			if a.OwnerUserID == ownerUserID && !a.Disabled {
				out = append(out, a)
			}
		}
	}
	sortBridgeAgents(out)
	return out, nil
}

// LatestBridgeForOwner returns the owner's latest heartbeat (memory impl; see
// BridgeJobStore).
func (s *MemoryBridgeJobs) LatestBridgeForOwner(_ context.Context, ownerUserID string) (BridgeHeartbeat, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest BridgeHeartbeat
	found := false
	for _, hb := range s.heartbeats {
		if hb.OwnerUserID != ownerUserID {
			continue
		}
		// Latest seen, then the greatest bridge id (MySQL's ORDER BY).
		if !found || hb.LastSeenAtMs > latest.LastSeenAtMs || (hb.LastSeenAtMs == latest.LastSeenAtMs && hb.BridgeID > latest.BridgeID) {
			latest, found = hb, true
		}
	}
	return latest, found, nil
}

// PendingAgentJobs counts the owner's not-yet-started jobs (memory impl; see
// BridgeJobStore).
func (s *MemoryBridgeJobs) PendingAgentJobs(_ context.Context, ownerUserID string) ([]AgentJobCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[[2]string]int{}
	for _, j := range s.jobs {
		if j.OwnerUserID == ownerUserID && j.Status.Pending() {
			counts[[2]string{j.Core, j.Provider}]++
		}
	}
	out := make([]AgentJobCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, AgentJobCount{Core: k[0], Provider: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Core != out[j].Core {
			return out[i].Core < out[j].Core
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}
