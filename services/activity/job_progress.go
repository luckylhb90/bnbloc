package activity

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleBridgeJobProgress fans a live job's turn activity to its room
// (POST /v1/bridge/jobs/{jobId}/progress, docs/prd/2026-10-13-live-turns.md
// §3): only the bridge holding the job reports it, the room comes from the
// stored job, and nothing is stored — agent_job_progress is transient.
func (s *Service) handleBridgeJobProgress(w http.ResponseWriter, r *http.Request) {
	if s.BridgeJobs == nil {
		httpx.Unavailable(w, "bridge jobs")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body wire.JobProgressInput
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
	job, err := s.BridgeJobs.HeldAgentJob(r.Context(), jobID, body.BridgeID)
	if err != nil {
		writeJobStoreError(w, "job lookup failed", err)
		return
	}
	now := s.clock().UnixMilli()
	progress := wire.AgentJobProgress{JobID: job.JobID, StartedAtMs: body.StartedAtMs, Activity: jobActivity(body.Activity), AtMs: now}
	if s.Publisher != nil && job.Status.Live() {
		s.Publisher.Publish(tenant.NewContext(r.Context(), job.TenantID), wire.Event{RoomID: job.RoomID, ChannelID: job.ChannelID, Kind: wire.EventAgentJobProgress, Payload: progress, AtUnixMs: now})
	}
	w.WriteHeader(http.StatusNoContent)
}

// jobActivity holds a reported activity to the wire's bounds: known kinds
// only, titles cut to MaxJobActivityTitle runes, the newest MaxJobActivity
// steps.
func jobActivity(in []wire.JobActivity) []wire.JobActivity {
	out := make([]wire.JobActivity, 0, len(in))
	for _, a := range in {
		switch a.Kind {
		case wire.JobActivityTool, wire.JobActivityThinking, wire.JobActivityText:
		default:
			continue
		}
		a.Title = strings.TrimSpace(a.Title)
		if utf8.RuneCountInString(a.Title) > wire.MaxJobActivityTitle {
			a.Title = string([]rune(a.Title)[:wire.MaxJobActivityTitle-1]) + "…"
		}
		out = append(out, a)
	}
	if len(out) > wire.MaxJobActivity {
		out = out[len(out)-wire.MaxJobActivity:]
	}
	return out
}
