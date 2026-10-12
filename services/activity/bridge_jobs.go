package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type heartbeatBody struct {
	BridgeID       string            `json:"bridgeId"`
	OwnerUserID    string            `json:"ownerUserId"`
	AppInstanceID  string            `json:"appInstanceId"`
	Version        string            `json:"version"`
	ServeAllOwners bool              `json:"serveAllOwners"`
	Agents         []bridgeAgentBody `json:"agents"`
	Metadata       json.RawMessage   `json:"metadata"`
}

type bridgeAgentBody struct {
	Core             string          `json:"core"`
	Provider         string          `json:"provider"`
	TargetID         string          `json:"targetId"`
	ModelOptionsJSON json.RawMessage `json:"modelOptions"`
	Disabled         bool            `json:"disabled"`
}

func (s *Service) handleBridgeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body heartbeatBody
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &body) {
		return
	}
	if body.OwnerUserID == "" {
		body.OwnerUserID = caller
	}
	if body.OwnerUserID != caller {
		httpx.Error(w, "bridge owner mismatch", http.StatusForbidden)
		return
	}
	now := time.Now().UnixMilli()
	body.BridgeID = resolveBridgeID(body.BridgeID, caller)
	serveAll := s.serveAll(body.ServeAllOwners, caller)
	if err := s.BridgeJobs.PutBridgeHeartbeat(r.Context(), store.BridgeHeartbeat{BridgeID: body.BridgeID, OwnerUserID: body.OwnerUserID, AppInstanceID: body.AppInstanceID, Version: body.Version, Status: store.BridgeOnline, LastSeenAtMs: now, MetadataJSON: body.Metadata, ServeAllOwners: serveAll}); err != nil {
		if errors.Is(err, store.ErrBridgeOwnerMismatch) {
			httpx.Error(w, "bridge owner mismatch", http.StatusForbidden)
			return
		}
		obs.ServerError(w, "bridge heartbeat failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	agents := make([]store.BridgeAgent, 0, len(body.Agents))
	for _, a := range body.Agents {
		if a.Core == "" || a.Provider == "" {
			continue
		}
		agents = append(agents, store.BridgeAgent{BridgeID: body.BridgeID, OwnerUserID: body.OwnerUserID, Core: a.Core, Provider: a.Provider, TargetID: a.TargetID, ModelOptionsJSON: a.ModelOptionsJSON, Disabled: a.Disabled, LastSeenAtMs: now})
	}
	if err := s.BridgeJobs.ReplaceBridgeAgents(r.Context(), body.BridgeID, agents); err != nil {
		obs.ServerError(w, "bridge heartbeat failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Metrics.CounterInc(metricBridgeHeartbeats)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"bridgeId": body.BridgeID, "agents": len(agents), "serveAllOwners": serveAll})
}

// resolveBridgeID applies the default bridge id for a caller that sends none.
func resolveBridgeID(bridgeID, caller string) string {
	if bridgeID = strings.TrimSpace(bridgeID); bridgeID != "" {
		return bridgeID
	}
	return fmt.Sprintf("bridge-%s", caller)
}

// serveAll resolves a bridge's serve-all request against the server-side gate.
// A request the service is not configured to honor is downgraded (the bridge
// serves its own agents only) and logged, never trusted from the body. The
// heartbeat reports the effective value back (serveAllOwners) and the claim
// path applies the same gate without re-logging on every poll.
func (s *Service) serveAll(requested bool, caller string) bool {
	if requested && !s.AllowServeAllOwners {
		slog.Warn("bnbloc activity refused bridge serve-all request (BNBLOC_ACTIVITY_ALLOW_SERVE_ALL unset)",
			"event", "bnbloc.activity.serve_all_refused", "owner", caller)
		return false
	}
	return requested
}

type claimBody struct {
	BridgeID       string `json:"bridgeId"`
	OwnerUserID    string `json:"ownerUserId"`
	MaxJobs        int    `json:"maxJobs"`
	ServeAllOwners bool   `json:"serveAllOwners"`
}

func (s *Service) handleBridgeJobClaim(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body claimBody
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &body) {
		return
	}
	if body.OwnerUserID == "" {
		body.OwnerUserID = caller
	}
	if body.OwnerUserID != caller {
		httpx.Error(w, "bridge owner mismatch", http.StatusForbidden)
		return
	}
	// A claim goes through a bridge the caller registered (heartbeat). The
	// bridge id is not a credential: claiming through another member's bridge
	// would hand over that member's queued prompts.
	body.BridgeID = resolveBridgeID(body.BridgeID, caller)
	if !s.requireBridgeOwner(w, r, body.BridgeID, caller) {
		return
	}
	jobs, err := s.BridgeJobs.ClaimAgentJobs(r.Context(), store.ClaimRequest{BridgeID: body.BridgeID, OwnerUserID: caller, MaxJobs: body.MaxJobs, NowMs: time.Now().UnixMilli(), LeaseMs: 60000, ServeAllOwners: body.ServeAllOwners && s.AllowServeAllOwners})
	if err != nil {
		obs.ServerError(w, "bridge job claim failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The claim is the last server step before a bridge drives: a reached
	// hard-stop usage budget refuses the job here (R12).
	jobs = s.refuseOverBudget(r.Context(), body.BridgeID, jobs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

type jobStatusBody struct {
	BridgeID        string                      `json:"bridgeId"`
	Status          store.AgentJobStatus        `json:"status"`
	FailureReason   store.AgentJobFailureReason `json:"failureReason"`
	DiagnosticsJSON json.RawMessage             `json:"diagnostics"`
}

func validJobStatus(s store.AgentJobStatus) bool {
	switch s {
	case store.AgentJobClaimed, store.AgentJobRunning, store.AgentJobReflowing,
		store.AgentJobReplied, store.AgentJobFailed, store.AgentJobTimedOut, store.AgentJobCancelled:
		return true
	}
	return false
}

func (s *Service) handleBridgeJobStatus(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body jobStatusBody
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &body) {
		return
	}
	jobID := r.PathValue("jobId")
	if body.BridgeID == "" || jobID == "" {
		httpx.Error(w, "bridgeId and jobId required", http.StatusBadRequest)
		return
	}
	if !validJobStatus(body.Status) {
		httpx.Error(w, "unsupported job status", http.StatusBadRequest)
		return
	}
	// A status report may only come from the member who registered that bridge.
	// Without this any room member knowing a bridgeId could drive another
	// member's job to a terminal state.
	if !s.requireBridgeOwner(w, r, body.BridgeID, caller) {
		return
	}
	now := time.Now().UnixMilli()
	job, err := s.BridgeJobs.UpdateAgentJobStatus(r.Context(), store.JobStatusUpdate{JobID: jobID, BridgeID: body.BridgeID, Status: body.Status, FailureReason: body.FailureReason, DiagnosticsJSON: body.DiagnosticsJSON, NowMs: now, LeaseMs: store.DefaultLeaseMs, MaxAttempts: store.DefaultMaxJobAttempts})
	if err != nil {
		writeJobStoreError(w, "job status update failed", err)
		return
	}
	s.publishJob(r.Context(), job, now)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "status": job.Status, "leaseExpiresAtMs": job.LeaseExpiresAtMs})
}

type leaseBody struct {
	BridgeID string `json:"bridgeId"`
}

// handleBridgeJobLease extends the lease of a job whose turn is still running.
// A long agent turn would otherwise outlive its lease and be re-claimed by
// another bridge, running the same prompt twice.
func (s *Service) handleBridgeJobLease(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body leaseBody
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &body) {
		return
	}
	jobID := r.PathValue("jobId")
	if body.BridgeID == "" || jobID == "" {
		httpx.Error(w, "bridgeId and jobId required", http.StatusBadRequest)
		return
	}
	if !s.requireBridgeOwner(w, r, body.BridgeID, caller) {
		return
	}
	now := time.Now().UnixMilli()
	if err := s.BridgeJobs.RenewAgentJobLease(r.Context(), store.LeaseRenewal{JobID: jobID, BridgeID: body.BridgeID, NowMs: now, LeaseMs: store.DefaultLeaseMs}); err != nil {
		writeJobStoreError(w, "job lease renewal failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "leaseExpiresAtMs": now + store.DefaultLeaseMs})
}

// publishJob fans a job transition to the room that owns it. The room id must
// come from the stored job: publishing without it strands the event and the
// composer never learns the turn ended.
func (s *Service) publishJob(ctx context.Context, job store.AgentJob, now int64) {
	if s.Publisher == nil || job.RoomID == "" {
		return
	}
	// The event belongs to the job's tenant (the sweeper runs tenantless).
	s.Publisher.Publish(tenant.NewContext(ctx, job.TenantID), wire.Event{RoomID: job.RoomID, ChannelID: job.ChannelID, Kind: wire.EventAgentJobUpdated, Payload: job, AtUnixMs: now})
}

// SweepAgentJobs fails jobs no worker can finish and notifies their rooms. The
// daemon calls it periodically so a member never waits on a job whose bridge
// went away.
func (s *Service) SweepAgentJobs(ctx context.Context) {
	if s.BridgeJobs == nil {
		return
	}
	now := time.Now().UnixMilli()
	jobs, err := s.BridgeJobs.SweepAgentJobs(ctx, store.SweepRequest{NowMs: now, QueuedTimeoutMs: store.DefaultQueuedTimeoutMs, MaxAttempts: store.DefaultMaxJobAttempts})
	if err != nil {
		return
	}
	for _, job := range jobs {
		s.publishJob(ctx, job, now)
		s.postJobNotice(ctx, job, sweptFailureText(job), now)
	}
}

// sweptFailureText explains a server-side failure in the room feed. A swept job
// has no worker left to post its own error, so without this the turn ends
// silently and the member only sees their own message.
func sweptFailureText(job store.AgentJob) string {
	agent := strings.TrimSpace(job.Provider)
	if agent == "" {
		agent = "agent"
	}
	switch job.FailureReason {
	case store.FailureNoBridgeOnline:
		return "The " + agent + " agent is offline: no bridge picked up this request."
	case store.FailureTurnTimeout:
		return "The " + agent + " agent stopped responding and the request was retried without success."
	default:
		return "The " + agent + " agent could not complete this request."
	}
}

// postJobNotice records a visible assistant message (text) for a server-failed
// job, in the same store the bridge writes replies to, and publishes it to the
// originating room/channel.
func (s *Service) postJobNotice(ctx context.Context, job store.AgentJob, text string, now int64) {
	if s.Store == nil || s.Publisher == nil || job.RoomID == "" {
		return
	}
	// The sweeper runs on a background context with no tenant, but the activity
	// store is tenant-scoped: writing without one fails tenant.Require and the
	// message is silently dropped. Each job carries its own tenant, so the write
	// happens in that scope.
	ctx = tenant.NewContext(ctx, job.TenantID)
	// Messages are cursor-tracked per agent session. A swept job never bound one,
	// so its notice goes to the job's own notice session (shared namespace with
	// the bridge's job notices) rather than an unrelated session's cursor.
	in := wire.JobNotice{
		RoomID: job.RoomID, ChannelID: job.ChannelID, JobID: job.JobID, SourceSeq: job.MessageSeq,
		Kind: wire.NoticeJobFailed, Text: text, OwnerUserID: job.OwnerUserID, Provider: job.Provider,
		FailReason: string(job.FailureReason), AtUnixMs: now,
	}.Report()
	fresh, _, err := s.Store.AppendMessages(ctx, job.RoomID, in)
	if err != nil {
		slog.Warn("bnbloc activity job failure message not recorded",
			"event", "bnbloc.activity.job_failure_message_failed", "job", job.JobID, "room", job.RoomID, "error", err)
		return
	}
	if len(fresh) == 0 {
		return
	}
	out := in
	out.Messages = fresh
	s.Publisher.Publish(ctx, wire.Event{RoomID: job.RoomID, ChannelID: job.ChannelID, Kind: wire.KindAgentMessages, Payload: out, AtUnixMs: now})
}

// handleCancelAgentJob stops a job a member no longer wants. Unlike a worker
// report this is a room action: it is authorized by room membership, so a
// member can stop a runaway turn even while another member's bridge holds the
// lease, and it works whether the job is still queued or already running.
func (s *Service) handleCancelAgentJob(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		httpx.Error(w, "jobId required", http.StatusBadRequest)
		return
	}
	now := time.Now().UnixMilli()
	job, err := s.BridgeJobs.CancelAgentJob(r.Context(), store.CancelRequest{
		JobID:           jobID,
		RoomID:          roomID,
		RequestedByUser: svcauth.Subject(r),
		NowMs:           now,
	})
	if err != nil {
		writeJobStoreError(w, "job cancel failed", err)
		return
	}
	s.publishJob(r.Context(), job, now)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobId": job.JobID, "status": job.Status})
}

// writeJobStoreError is the one mapping of a bridge-job store error: only the
// lease sentinel is a 409 (the job is no longer the caller's to move), an
// unknown job is a 404, and anything else is a logged 500 — a database failure
// must never read as "lease lost" or leak its text to the caller.
func writeJobStoreError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, store.ErrAgentJobLeaseMismatch):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrAgentJobNotFound):
		httpx.Error(w, err.Error(), http.StatusNotFound)
	default:
		obs.ServerError(w, what, err)
		httpx.Error(w, what, http.StatusInternalServerError)
	}
}

// requireBridgeOwner refuses (403) a bridge the caller did not register, and
// reports a failed lookup as a 500 rather than as a refusal.
func (s *Service) requireBridgeOwner(w http.ResponseWriter, r *http.Request, bridgeID, caller string) bool {
	hb, err := s.BridgeJobs.LookupBridge(r.Context(), bridgeID)
	if err != nil && !errors.Is(err, store.ErrBridgeNotFound) {
		obs.ServerError(w, "bridge lookup failed", err)
		httpx.Error(w, "bridge lookup failed", http.StatusInternalServerError)
		return false
	}
	if err != nil || hb.OwnerUserID != caller {
		httpx.Error(w, "bridge owner mismatch", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Service) handleRoomAgentJobs(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobs": s.BridgeJobs.ListAgentJobs(r.Context(), roomID, r.URL.Query().Get("channelId"), 100)})
}

// handleMyAgents returns the CALLER's own bridge-advertised agents across all
// rooms (owner = authenticated subject, liveness-joined). It powers the
// browser Agents page: the same member sees the same agent list as their
// desktop app, because both render what their bridge advertises.
func (s *Service) handleMyAgents(w http.ResponseWriter, r *http.Request) {
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	agents := s.BridgeJobs.ListBridgeAgentsForOwner(r.Context(), caller)
	type out struct {
		Core         string                  `json:"core"`
		Provider     string                  `json:"provider"`
		TargetID     string                  `json:"targetId"`
		Online       bool                    `json:"online"`
		ModelOptions *wire.AgentModelOptions `json:"modelOptions,omitempty"`
	}
	rows := make([]out, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, out{Core: a.Core, Provider: a.Provider, TargetID: a.TargetID, Online: !a.Disabled, ModelOptions: modelOptions(a)})
	}
	// desktop is the member's own desktop app, independent of agents: a
	// bridge with no core yet is still connected, and lastSeenAtMs > 0 means
	// the app was installed and connected at least once (0 = never).
	type desktop struct {
		Online       bool  `json:"online"`
		LastSeenAtMs int64 `json:"lastSeenAtMs"`
	}
	var d desktop
	hb, found, err := s.BridgeJobs.LatestBridgeForOwner(r.Context(), caller)
	if err != nil {
		obs.ServerError(w, "read the member's latest bridge", err)
		httpx.Error(w, "could not read the desktop connection", http.StatusInternalServerError)
		return
	}
	if found {
		d = desktop{Online: store.BridgeLive(hb, time.Now().UnixMilli()), LastSeenAtMs: hb.LastSeenAtMs}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"agents": rows, "desktop": d})
}
