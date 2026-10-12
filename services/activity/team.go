package activity

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team agent dashboard (F1): a read-only aggregation over data the bridge, job
// queue, and reply ingest already persist — advertised agents + bridge
// liveness, the durable job queue, and the metrics agents report on replies.
//
// Two scopes, two paths, one handler:
//   - GET /v1/team/agents         — the caller's own agents (any member);
//   - GET /v1/tenant/team/agents  — every member's agents in the ctx tenant.
//     The gateway gates /v1/tenant/* on manage_org (org_admin, super_admin),
//     and a super_admin's acting tenant is the signed ctx tenant, so this
//     service needs no role knowledge of its own.

// Team scope values (wire.TeamAgentsReply.Scope).
const (
	teamScopeSelf   = "self"
	teamScopeTenant = "tenant"
)

// maxTZOffsetMin bounds the client's timezone offset (UTC-14..UTC+14).
const maxTZOffsetMin = 14 * 60

// teamSince resolves the range start. "today" starts at the caller's local
// midnight: tzOffsetMin is the browser's Date.getTimezoneOffset() (minutes,
// UTC minus local), absent = UTC. "7d" is the trailing seven days.
func teamSince(rng, tz string, now time.Time) (string, int64, bool) {
	if rng == "" {
		rng = wire.TeamRangeToday
	}
	switch rng {
	case wire.TeamRange7d:
		return rng, now.Add(-7 * 24 * time.Hour).UnixMilli(), true
	case wire.TeamRangeToday:
		offset := 0
		if tz != "" {
			n, err := strconv.Atoi(tz)
			if err != nil || n < -maxTZOffsetMin || n > maxTZOffsetMin {
				return "", 0, false
			}
			offset = n
		}
		const day = int64(24 * 60 * 60 * 1000)
		shift := int64(offset) * 60 * 1000
		local := now.UnixMilli() - shift
		return rng, local - local%day + shift, true
	}
	return "", 0, false
}

func (s *Service) handleTeamAgents(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
		if caller == "" {
			httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
			return
		}
		if s.Team == nil {
			httpx.Unavailable(w, "team dashboard")
			return
		}
		now := time.Now()
		if s.Now != nil {
			now = s.Now()
		}
		rng, since, ok := teamSince(r.URL.Query().Get("range"), r.URL.Query().Get("tzOffsetMin"), now)
		if !ok {
			httpx.Error(w, "range must be today or 7d (tzOffsetMin within ±840)", http.StatusBadRequest)
			return
		}
		owner := caller
		if scope == teamScopeTenant {
			owner = ""
		}
		httpx.WriteJSON(w, http.StatusOK, wire.TeamAgentsReply{
			Scope:   scope,
			Range:   rng,
			SinceMs: since,
			Agents:  s.teamAgents(r, owner, since, now.UnixMilli()),
		})

	}
}

// teamAgents joins advertised agents, jobs, and reported usage into one row
// per (owner, core, provider). An agent seen only in jobs or usage (its bridge
// no longer advertises it) still gets a row, offline.
func (s *Service) teamAgents(r *http.Request, owner string, since, nowMs int64) []wire.TeamAgent {
	ctx := r.Context()
	rows := map[string]*wire.TeamAgent{}
	row := func(o, core, provider string) *wire.TeamAgent {
		k := o + "\x00" + core + "\x00" + provider
		if a := rows[k]; a != nil {
			return a
		}
		a := &wire.TeamAgent{Owner: o, Core: core, Provider: provider}
		rows[k] = a
		return a
	}
	for _, ta := range s.Team.TenantTeamAgents(ctx, owner, nowMs) {
		a := row(ta.OwnerUserID, ta.Core, ta.Provider)
		// Several bridges may advertise the same agent: online if any is, last
		// seen at the most recent heartbeat.
		a.Online = a.Online || ta.Online
		if ta.LastSeenAtMs > a.LastSeenAtMs {
			a.LastSeenAtMs = ta.LastSeenAtMs
		}
		if a.TargetID == "" {
			a.TargetID = ta.TargetID
		}
	}
	rooms := roomNamer{s: s, r: r, names: map[string]string{}}
	// Jobs arrive newest-first, so the first active / first terminal job per
	// agent is its current / last job.
	for _, j := range s.Team.TenantTeamJobs(ctx, owner, since) {
		a := row(j.OwnerUserID, j.Core, j.Provider)
		if !j.Status.Terminal() {
			if a.CurrentJob == nil {
				a.CurrentJob = rooms.job(j)
			}
			continue
		}
		if a.LastJob == nil {
			a.LastJob = rooms.job(j)
		}
		if j.CreatedAtMs < since {
			continue
		}
		switch j.Status {
		case store.AgentJobReplied:
			a.Turns++
			if j.RequesterUserID != j.OwnerUserID {
				a.BorrowedRuns++
			}
		case store.AgentJobFailed, store.AgentJobTimedOut:
			a.FailedTurns++
		}
	}
	if s.Usage != nil {
		s.attachUsage(rows, row, s.Usage.AgentUsage(ctx, owner, since))
	}
	out := make([]wire.TeamAgent, 0, len(rows))
	for _, a := range rows {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Owner != out[k].Owner {
			return out[i].Owner < out[k].Owner
		}
		if out[i].Core != out[k].Core {
			return out[i].Core < out[k].Core
		}
		return out[i].Provider < out[k].Provider
	})
	return out
}

// attachUsage puts each (owner, provider) usage total on the matching agent
// rows. Replies carry the provider but not the core, so when an owner exposes
// the same provider under two cores the total cannot be split: every such row
// shows it, marked Shared.
func (s *Service) attachUsage(rows map[string]*wire.TeamAgent, row func(o, core, provider string) *wire.TeamAgent, usage []store.AgentTurnUsage) {
	for _, u := range usage {
		var matches []*wire.TeamAgent
		for _, a := range rows {
			if a.Owner == u.OwnerUserID && a.Provider == u.Provider {
				matches = append(matches, a)
			}
		}
		if len(matches) == 0 {
			matches = append(matches, row(u.OwnerUserID, "", u.Provider))
		}
		for _, a := range matches {
			tu := &wire.TeamAgentUsage{
				ReportedTurns: u.ReportedTurns,
				InputTokens:   u.InputTokens,
				OutputTokens:  u.OutputTokens,
				Shared:        len(matches) > 1,
			}
			if u.CostReportedBy > 0 {
				c := u.CostUsd
				tu.CostUsd = &c
			}
			a.Usage = tu
		}
	}
}

// roomNamer resolves room names for the jobs shown, once per room.
type roomNamer struct {
	s     *Service
	r     *http.Request
	names map[string]string
}

// name returns a room's name ("" when unknown or no room store).
func (n roomNamer) name(roomID string) string {
	name, ok := n.names[roomID]
	if !ok && n.s.Rooms != nil {
		if room, err := n.s.Rooms.GetRoom(n.r.Context(), roomID); err == nil {
			name = room.Name
		}
		n.names[roomID] = name
	}
	return name
}

func (n roomNamer) job(j store.AgentJob) *wire.TeamJob {
	name := n.name(j.RoomID)
	return &wire.TeamJob{
		JobID:         j.JobID,
		RoomID:        j.RoomID,
		RoomName:      name,
		ChannelID:     j.ChannelID,
		Requester:     j.RequesterUserID,
		Status:        string(j.Status),
		FailureReason: string(j.FailureReason),
		CreatedAtMs:   j.CreatedAtMs,
		StartedAtMs:   j.StartedAtMs,
		FinishedAtMs:  j.FinishedAtMs,
	}
}
