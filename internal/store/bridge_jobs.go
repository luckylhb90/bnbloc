package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var ErrAgentJobNotFound = errors.New("store: agent job not found")
var ErrAgentJobLeaseMismatch = errors.New("store: agent job lease mismatch")
var ErrBridgeNotFound = errors.New("store: bridge not found")

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

// Terminal reports whether a job reached a final state. A terminal job is never
// claimed, renewed, swept, or overwritten again.
func (s AgentJobStatus) Terminal() bool {
	switch s {
	case AgentJobReplied, AgentJobFailed, AgentJobTimedOut, AgentJobCancelled:
		return true
	}
	return false
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
	FailureTurnTimeout  AgentJobFailureReason = "turn_timeout"
	// FailureReplyTimeout: the turn ran and produced no visible reply;
	// final for the same reason.
	FailureReplyTimeout AgentJobFailureReason = "reply_timeout"
	FailureCancelled    AgentJobFailureReason = "cancelled"
	// FailureBudgetExhausted: the agent owner's (or the tenant's) monthly
	// usage budget is reached with hard stop on (R12); final, not retried.
	FailureBudgetExhausted AgentJobFailureReason = "budget_exhausted"
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

// Retryable reports whether a failed job may be attempted again: only when
// no agent work can have started (the core was unreachable, the start
// request provably never took effect) or the lease lapsed without a report.
// Everything else is terminal on the first failure: a deterministic refusal
// fails the same way again, and a started or ambiguous turn would run the
// member's prompt twice.
func (r AgentJobFailureReason) Retryable() bool {
	switch r {
	case FailureRunnerStartFailed, FailureCoreUnavailable, FailureTurnTimeout:
		return true
	default:
		return false
	}
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
	case FailureTurnTimeout:
		return the + " stopped responding and the request was retried without success."
	case FailureReplyTimeout:
		return the + " stopped responding."
	case FailureBudgetExhausted:
		return the + " did not run: a monthly budget is reached."
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

// AgentJob is a durable request for a bridge to drive one agent turn.
type AgentJob struct {
	JobID            string                `json:"jobId"`
	TenantID         string                `json:"tenantId"`
	RoomID           string                `json:"roomId"`
	ChannelID        string                `json:"channelId"`
	MessageSeq       int64                 `json:"messageSeq"`
	ClientMsgID      string                `json:"clientMsgId"`
	RequesterUserID  string                `json:"requesterUserId"`
	OwnerUserID      string                `json:"ownerUserId"`
	Core             string                `json:"core"`
	Provider         string                `json:"provider"`
	TargetID         string                `json:"targetId"`
	MessageText      string                `json:"messageText"`
	AttachmentIDs    []string              `json:"attachmentIds,omitempty"`
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

type ClaimRequest struct {
	BridgeID       string
	OwnerUserID    string
	MaxJobs        int
	NowMs          int64
	LeaseMs        int64
	ServeAllOwners bool
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
	LeaseMs int64
	// MaxAttempts bounds how often a retryable failure is requeued instead of
	// becoming terminal. 0 uses DefaultMaxJobAttempts.
	MaxAttempts int
}

// LeaseRenewal extends a running job's lease without changing its status. A
// worker renews while the agent turn is still alive; without it a turn longer
// than the lease becomes claimable again and the same prompt runs twice.
type LeaseRenewal struct {
	JobID    string
	BridgeID string
	NowMs    int64
	LeaseMs  int64
}

// SweepRequest fails jobs no worker can finish: queued past QueuedTimeoutMs
// (nothing advertised the agent) and expired leases past MaxAttempts. Sweeping
// is cross-tenant maintenance and carries no tenant scope.
type SweepRequest struct {
	NowMs           int64
	QueuedTimeoutMs int64
	MaxAttempts     int
}

const (
	// DefaultLeaseMs bounds how long a claim survives without progress before
	// another bridge may take the job over.
	DefaultLeaseMs = 60_000
	// DefaultQueuedTimeoutMs fails a job nothing ever claimed, so a member sees
	// no_bridge_online instead of a job queued forever.
	DefaultQueuedTimeoutMs = 120_000
	// DefaultMaxJobAttempts caps lease-expiry re-claims so a job that kills its
	// worker every time fails instead of looping.
	DefaultMaxJobAttempts = 3
)

type BridgeJobStore interface {
	PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error)
	ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) []AgentJob
	ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error)
	UpdateAgentJobStatus(ctx context.Context, upd JobStatusUpdate) (AgentJob, error)
	CancelAgentJob(ctx context.Context, req CancelRequest) (AgentJob, error)
	RenewAgentJobLease(ctx context.Context, ren LeaseRenewal) error
	SweepAgentJobs(ctx context.Context, req SweepRequest) ([]AgentJob, error)
	PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error
	LookupBridge(ctx context.Context, bridgeID string) (BridgeHeartbeat, error)
	ReplaceBridgeAgents(ctx context.Context, bridgeID string, agents []BridgeAgent) error
	ListBridgeAgents(ctx context.Context, roomID string) []BridgeAgent
	// ListBridgeAgentsForOwner returns one owner's advertised agents across
	// all rooms/tenants (owner identity is global), liveness-joined — the
	// "my agents" surface for a member's own view.
	ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string) []BridgeAgent
	// LatestBridgeForOwner returns the owner's most recently seen bridge
	// heartbeat across tenants (owner identity is global), and false when no
	// desktop bridge of theirs ever connected. Heartbeat rows are never
	// deleted, so this tells "installed but closed" from "never installed".
	LatestBridgeForOwner(ctx context.Context, ownerUserID string) (BridgeHeartbeat, bool, error)
	// CountOnlineBridges counts bridges of every tenant online at nowMs
	// (metrics).
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

// pendingJob reports whether a job has not started yet: queued, or claimed
// by a bridge that has not reported it running.
func pendingJob(j AgentJob) bool {
	return j.Status == AgentJobQueued || j.Status == AgentJobClaimed
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

type MemoryBridgeJobs struct {
	mu         sync.Mutex
	jobs       map[string]AgentJob
	heartbeats map[string]BridgeHeartbeat
	agents     map[string]map[string]BridgeAgent
	origin     map[string]string
}

func NewMemoryBridgeJobs() *MemoryBridgeJobs {
	return &MemoryBridgeJobs{jobs: map[string]AgentJob{}, heartbeats: map[string]BridgeHeartbeat{}, agents: map[string]map[string]BridgeAgent{}, origin: map[string]string{}}
}

var _ BridgeJobStore = (*MemoryBridgeJobs)(nil)

func jobOrigin(tid string, j AgentJob) string {
	return tid + "\x00" + j.RoomID + "\x00" + strconv.FormatInt(j.MessageSeq, 10) + "\x00" + j.OwnerUserID + "\x00" + j.Core + "\x00" + j.Provider
}

func (s *MemoryBridgeJobs) PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j.TenantID = tid
	if j.Status == "" {
		j.Status = AgentJobQueued
	}
	// Queue lifetime is decided from CreatedAtMs, so it must be a real server
	// clock. A zero/absent value would otherwise read as "queued since the
	// epoch" and be swept the moment it is enqueued.
	if j.CreatedAtMs <= 0 {
		j.CreatedAtMs = time.Now().UnixMilli()
	}
	if j.UpdatedAtMs <= 0 {
		j.UpdatedAtMs = j.CreatedAtMs
	}
	key := jobOrigin(tid, j)
	if existingID := s.origin[key]; existingID != "" {
		return s.jobs[existingID], nil
	}
	s.jobs[j.JobID] = j
	s.origin[key] = j.JobID
	return j, nil
}

func (s *MemoryBridgeJobs) ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) []AgentJob {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentJob
	for _, j := range s.jobs {
		if j.TenantID != tid || j.RoomID != roomID {
			continue
		}
		if channelID != "" && j.ChannelID != channelID {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtMs < out[j].CreatedAtMs })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (s *MemoryBridgeJobs) PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
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
		m[a.OwnerUserID+"\x00"+a.Core+"\x00"+a.Provider] = a
	}
	s.agents[bridgeID] = m
	return nil
}

// ListBridgeAgents returns capabilities advertised by bridges that are still
// alive. A bridge that stopped heartbeating leaves its rows behind, and serving
// them would offer members an agent nobody can drive: the composer lists it and
// the job then waits out the queue timeout instead of being refused up front.
func (s *MemoryBridgeJobs) ListBridgeAgents(ctx context.Context, roomID string) []BridgeAgent {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	_ = roomID
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UnixMilli() - BridgeOfflineAfterMs
	var out []BridgeAgent
	for bridgeID, byAgent := range s.agents {
		if hb, ok := s.heartbeats[bridgeID]; ok && hb.LastSeenAtMs < cutoff {
			continue // bridge is gone; its advertisement is stale
		}
		for _, a := range byAgent {
			if a.TenantID == tid && !a.Disabled {
				out = append(out, a)
			}
		}
	}
	return out
}

func (s *MemoryBridgeJobs) ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error) {
	if req.MaxJobs <= 0 {
		req.MaxJobs = 1
	}
	if req.LeaseMs <= 0 {
		req.LeaseMs = 60000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A bridge must be currently alive to take work. Its rows outlive it, so
	// without this a dead bridge keeps winning jobs it can never run.
	// It must also belong to the claiming owner: a bridge id is not a
	// credential, so claiming through someone else's bridge yields nothing.
	if hb, ok := s.heartbeats[req.BridgeID]; !ok || hb.OwnerUserID != req.OwnerUserID || hb.LastSeenAtMs < req.NowMs-BridgeOfflineAfterMs {
		return nil, nil
	}
	// Capability + job matching is CROSS-TENANT: a job lives in its room's
	// tenant, which may differ from the tenant the bridge advertised in. bridge
	// id is globally unique, so ownership + liveness (not tenant) authorize the
	// claim; the returned job carries its own tenant for the bridge to drive in.
	capable := map[string]bool{}
	for _, a := range s.agents[req.BridgeID] {
		if a.OwnerUserID == req.OwnerUserID && !a.Disabled {
			capable[a.Core+"\x00"+a.Provider] = true
		}
	}
	var ids []string
	for id, j := range s.jobs {
		if len(ids) >= req.MaxJobs {
			break
		}
		if !req.ServeAllOwners && j.OwnerUserID != req.OwnerUserID {
			continue
		}
		if !capable[j.Core+"\x00"+j.Provider] {
			continue
		}
		claimable := j.Status == AgentJobQueued || ((j.Status == AgentJobClaimed || j.Status == AgentJobRunning || j.Status == AgentJobReflowing) && j.LeaseExpiresAtMs < req.NowMs)
		if claimable {
			ids = append(ids, id)
		}
	}
	out := make([]AgentJob, 0, len(ids))
	for _, id := range ids {
		j := s.jobs[id]
		j.Status = AgentJobClaimed
		j.LeaseBridgeID = req.BridgeID
		j.LeaseExpiresAtMs = req.NowMs + req.LeaseMs
		j.Attempt++
		j.UpdatedAtMs = req.NowMs
		j.ClaimedAtMs = req.NowMs
		s.jobs[id] = j
		out = append(out, j)
	}
	return out, nil
}

func (s *MemoryBridgeJobs) UpdateAgentJobStatus(ctx context.Context, upd JobStatusUpdate) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[upd.JobID]
	if !ok || j.TenantID != tid {
		return AgentJob{}, ErrAgentJobNotFound
	}
	// Only the bridge holding the lease may move a job, and a terminal job is
	// never rewritten: a late report from a superseded worker must not overwrite
	// the outcome the current worker already recorded.
	if j.LeaseBridgeID != upd.BridgeID || j.Status.Terminal() {
		return AgentJob{}, ErrAgentJobLeaseMismatch
	}
	// A retryable cause (the runner died, the core blipped, the turn timed out)
	// goes back on the queue while attempts remain: the next worker may well
	// succeed. A non-retryable cause — an unauthorized borrow, an agent nobody
	// advertises, an explicit cancel — is final on the first report, because no
	// retry can change the outcome.
	if upd.Status == AgentJobFailed && upd.FailureReason.Retryable() && j.Attempt < maxAttempts(upd.MaxAttempts) {
		j.Status = AgentJobQueued
		j.FailureReason = upd.FailureReason
		j.DiagnosticsJSON = upd.DiagnosticsJSON
		j.UpdatedAtMs = upd.NowMs
		j.LeaseBridgeID = ""
		j.LeaseExpiresAtMs = 0
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
	switch {
	case upd.Status.Terminal():
		j.FinishedAtMs = upd.NowMs
		j.LeaseExpiresAtMs = 0
	case upd.LeaseMs > 0:
		j.LeaseExpiresAtMs = upd.NowMs + upd.LeaseMs
	}
	s.jobs[upd.JobID] = j
	return j, nil
}

func (s *MemoryBridgeJobs) RenewAgentJobLease(ctx context.Context, ren LeaseRenewal) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if ren.LeaseMs <= 0 {
		ren.LeaseMs = DefaultLeaseMs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[ren.JobID]
	if !ok || j.TenantID != tid {
		return ErrAgentJobNotFound
	}
	if j.LeaseBridgeID != ren.BridgeID || j.Status.Terminal() {
		return ErrAgentJobLeaseMismatch
	}
	j.LeaseExpiresAtMs = ren.NowMs + ren.LeaseMs
	j.UpdatedAtMs = ren.NowMs
	s.jobs[ren.JobID] = j
	return nil
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
	if req.QueuedTimeoutMs <= 0 {
		req.QueuedTimeoutMs = DefaultQueuedTimeoutMs
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = DefaultMaxJobAttempts
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentJob
	for id, j := range s.jobs {
		if j.Status.Terminal() {
			continue
		}
		var reason AgentJobFailureReason
		switch {
		case j.Status == AgentJobQueued && j.CreatedAtMs+req.QueuedTimeoutMs <= req.NowMs:
			reason = FailureNoBridgeOnline
		case j.Status != AgentJobQueued && j.LeaseExpiresAtMs < req.NowMs && j.Attempt >= req.MaxAttempts:
			reason = FailureTurnTimeout
		default:
			continue
		}
		j.Status = AgentJobFailed
		j.FailureReason = reason
		j.UpdatedAtMs = req.NowMs
		j.FinishedAtMs = req.NowMs
		j.LeaseExpiresAtMs = 0
		s.jobs[id] = j
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtMs < out[j].CreatedAtMs })
	return out, nil
}

func (s *MemoryBridgeJobs) LookupBridge(ctx context.Context, bridgeID string) (BridgeHeartbeat, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return BridgeHeartbeat{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hb, ok := s.heartbeats[bridgeID]
	if !ok || hb.TenantID != tid {
		return BridgeHeartbeat{}, ErrBridgeNotFound
	}
	return hb, nil
}

// ListBridgeAgentsForOwner returns the owner's advertised agents joined to a
// live heartbeat (memory impl; see MySQLBridgeJobs for the contract).
func (s *MemoryBridgeJobs) ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string) []BridgeAgent {
	if _, err := tenant.Require(ctx); err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UnixMilli() - BridgeOfflineAfterMs
	var out []BridgeAgent
	for bridgeID, byAgent := range s.agents {
		if hb, ok := s.heartbeats[bridgeID]; !ok || hb.LastSeenAtMs < cutoff {
			continue
		}
		for _, a := range byAgent {
			if a.OwnerUserID == ownerUserID && !a.Disabled {
				out = append(out, a)
			}
		}
	}
	return out
}

// LatestBridgeForOwner returns the owner's latest heartbeat (memory impl; see
// BridgeJobStore).
func (s *MemoryBridgeJobs) LatestBridgeForOwner(_ context.Context, ownerUserID string) (BridgeHeartbeat, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest BridgeHeartbeat
	found := false
	for _, hb := range s.heartbeats {
		if hb.OwnerUserID == ownerUserID && (!found || hb.LastSeenAtMs > latest.LastSeenAtMs) {
			latest, found = hb, true
		}
	}
	return latest, found, nil
}

// BridgeLive reports whether hb is a live bridge at nowMs: reported online
// and seen within BridgeOfflineAfterMs (the same rule the agent listings use).
func BridgeLive(hb BridgeHeartbeat, nowMs int64) bool {
	return hb.Status == BridgeOnline && hb.LastSeenAtMs >= nowMs-BridgeOfflineAfterMs
}

// PendingAgentJobs counts the owner's not-yet-started jobs (memory impl; see
// BridgeJobStore).
func (s *MemoryBridgeJobs) PendingAgentJobs(_ context.Context, ownerUserID string) ([]AgentJobCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[[2]string]int{}
	for _, j := range s.jobs {
		if j.OwnerUserID == ownerUserID && pendingJob(j) {
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
