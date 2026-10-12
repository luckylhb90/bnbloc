package activity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/expertteam"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team plan (docs/prd/2026-10-12-experts-and-teams.md §8, step 6): which
// agent takes each role of an expert team in a room, and what one team
// request is likely to cost. The roles are staffed by internal/expertteam
// from the agents the room's members may address (the room agent
// catalogs' rule), and each seat's cost is the average reported cost of its
// agent's last costSampleRuns finished runs in the organization.
//
// POST /v1/rooms/{roomId}/team-plan answers the composer (the caller is the
// requester; the body names the lead agent they picked) and the lead's
// bridge (?job=: the job must be live and leased by a bridge of the caller,
// store.JobLeasedBy; the lead and the requester are the job's).

// costSampleRuns is how many of an agent's latest finished runs the
// estimate averages: recent enough to follow model and price changes,
// enough to smooth one long run.
const costSampleRuns = 20

// handleTeamPlan serves POST /v1/rooms/{roomId}/team-plan.
func (s *Service) handleTeamPlan(w http.ResponseWriter, r *http.Request) {
	caller := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if s.BridgeJobs == nil || s.Rooms == nil || s.Runs == nil {
		obs.ServerError(w, "team plan not configured", nil)
		writeErr(w, http.StatusNotImplemented, "team plan not configured")
		return
	}
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	if s.Membership == nil || !s.Membership.IsMember(roomID, caller, tenant.FromContext(ctx)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	var in wire.TeamPlanRequest
	if !decode(w, r, &in) {
		return
	}
	if len(in.Members) == 0 || len(in.Members) > wire.MaxDispatchFanOut {
		writeErr(w, http.StatusBadRequest, "a team plan names 1 to 8 roles")
		return
	}
	for _, m := range in.Members {
		if !contentpkg.ValidName(m.Role) {
			writeErr(w, http.StatusBadRequest, "invalid role "+m.Role)
			return
		}
	}

	requester, lead := caller, wire.PlanAgent{}
	if jobID := strings.TrimSpace(r.URL.Query().Get("job")); jobID != "" {
		job, ok := s.planJob(w, r, roomID, caller, jobID)
		if !ok {
			return
		}
		requester = job.RequesterUserID
		lead = wire.PlanAgent{OwnerID: job.OwnerUserID, Core: job.Core, Provider: job.Provider}
	} else {
		if in.Lead == nil || strings.TrimSpace(in.Lead.OwnerID) == "" || strings.TrimSpace(in.Lead.Provider) == "" {
			writeErr(w, http.StatusBadRequest, "lead ownerId and provider are required")
			return
		}
		lead = wire.PlanAgent{OwnerID: strings.TrimSpace(in.Lead.OwnerID), Core: strings.TrimSpace(in.Lead.Core), Provider: strings.TrimSpace(in.Lead.Provider)}
	}

	addressable := s.roomAddressableAgents(r, roomID)
	agents := make([]wire.PlanAgent, 0, len(addressable))
	for _, a := range addressable {
		agents = append(agents, wire.PlanAgent{OwnerID: a.OwnerUserID, Core: a.Core, Provider: a.Provider, Platform: a.Platform})
	}
	expertteam.Name(agents)
	leadOnline := false
	for _, a := range agents {
		if expertteam.Same(a, lead) {
			lead, leadOnline = a, true
			break
		}
	}
	if !leadOnline {
		named := []wire.PlanAgent{lead}
		expertteam.Name(named)
		lead = named[0]
	}

	costs := map[string]*wire.AgentCostSample{}
	sample := func(a *wire.PlanAgent) (*wire.AgentCostSample, error) {
		key := a.OwnerID + "\x00" + a.Core + "\x00" + a.Provider
		if c, ok := costs[key]; ok {
			return c, nil
		}
		c, err := s.agentCost(ctx, *a)
		if err != nil {
			return nil, err
		}
		costs[key] = c
		return c, nil
	}
	reply := wire.TeamPlanReply{Lead: wire.TeamPlanEntry{Agent: &lead, Online: leadOnline}}
	if !leadOnline {
		reply.Lead.Reason = lead.Handle + " is not online in this room"
	}
	if reply.Lead.Cost, err = sample(&lead); err != nil {
		obs.ServerError(w, "team plan cost", err)
		writeErr(w, http.StatusInternalServerError, "team plan cost failed")
		return
	}
	reply.Roles = expertteam.Resolve(requester, lead, agents, in.Members)
	for i := range reply.Roles {
		if reply.Roles[i].Agent == nil {
			continue
		}
		if reply.Roles[i].Cost, err = sample(reply.Roles[i].Agent); err != nil {
			obs.ServerError(w, "team plan cost", err)
			writeErr(w, http.StatusInternalServerError, "team plan cost failed")
			return
		}
	}
	reply.Estimate = expertteam.Estimate(reply.Lead, reply.Roles)
	writeJSON(w, http.StatusOK, reply)
}

// planJob reads the lead's job for a bridge's team plan: it must be in the
// room, live and leased by a bridge of the caller.
func (s *Service) planJob(w http.ResponseWriter, r *http.Request, roomID, caller, jobID string) (store.AgentJob, bool) {
	ctx := r.Context()
	job, err := s.Runs.GetAgentRun(ctx, jobID)
	switch {
	case errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && job.RoomID != roomID):
		writeErr(w, http.StatusNotFound, "job not found in this room")
		return store.AgentJob{}, false
	case err != nil:
		obs.ServerError(w, "team plan job read", err)
		writeErr(w, http.StatusInternalServerError, "job read failed")
		return store.AgentJob{}, false
	}
	if s.Leases == nil {
		obs.ServerError(w, "team plan leases not configured", nil)
		writeErr(w, http.StatusNotImplemented, "team plan for a job not configured")
		return store.AgentJob{}, false
	}
	switch err := store.JobLeasedBy(ctx, s.Leases, job, caller); {
	case errors.Is(err, store.ErrJobNotLive), errors.Is(err, store.ErrJobNotCallers):
		writeErr(w, http.StatusForbidden, "the job is not running on the caller's bridge")
		return store.AgentJob{}, false
	case err != nil:
		obs.ServerError(w, "team plan lease check", err)
		writeErr(w, http.StatusInternalServerError, "lease check failed")
		return store.AgentJob{}, false
	}
	return job, true
}

// agentCost averages the reported cost of an agent's latest finished runs
// in the ctx tenant (runs that reported no cost do not count).
func (s *Service) agentCost(ctx context.Context, a wire.PlanAgent) (*wire.AgentCostSample, error) {
	runs, err := s.Runs.ListAgentRuns(ctx, store.AgentRunQuery{
		Owner: a.OwnerID, Core: a.Core, Provider: a.Provider,
		Statuses: []store.AgentJobStatus{store.AgentJobReplied}, Limit: costSampleRuns,
	})
	if err != nil {
		return nil, err
	}
	out := &wire.AgentCostSample{}
	total := 0.0
	for _, j := range runs {
		var diag map[string]any
		if len(j.DiagnosticsJSON) == 0 || json.Unmarshal(j.DiagnosticsJSON, &diag) != nil {
			continue
		}
		u := diagObject[wire.AgentRunUsage](diag, wire.RunDiagUsage)
		if u == nil || u.CostUsd == nil {
			continue
		}
		total += *u.CostUsd
		out.Runs++
	}
	if out.Runs > 0 {
		avg := total / float64(out.Runs)
		out.AvgCostUsd = &avg
	}
	return out, nil
}
