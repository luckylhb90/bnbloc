// Package expertteam staffs an expert team (docs/prd/2026-10-12-experts-and-
// teams.md §8): it picks the agent that takes each role of a team from the
// agents a room can address, estimates what one team request costs from
// the agents' recent reported runs, and writes the roster section the lead
// wears. It is pure: activity feeds it the room's addressable agents and
// run costs (POST /v1/rooms/{roomId}/team-plan), the bridge the plan and
// the resolved member experts.
package expertteam

import (
	"fmt"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MaxCoversBytes bounds each role's description in the lead's instructions:
// the team's and the role experts' authors wrote it, not the lead's owner.
const MaxCoversBytes = 300

// coversAuthor names who wrote the roles' descriptions inside their fence.
const coversAuthor = "the team's and its role experts' authors"

// LeadSeat names the lead in an estimate's Unknown list.
const LeadSeat = "lead"

// Name sets each agent's Handle as the room tools name agents:
// "owner/provider", or "owner/core/provider" where one owner exposes the
// same provider on two cores.
func Name(agents []wire.PlanAgent) {
	pairs := map[string]int{}
	for _, a := range agents {
		pairs[a.OwnerID+"/"+a.Provider]++
	}
	for i, a := range agents {
		h := a.OwnerID + "/" + a.Provider
		if pairs[h] > 1 && a.Core != "" {
			h = a.OwnerID + "/" + a.Core + "/" + a.Provider
		}
		agents[i].Handle = h
	}
}

// Same reports whether two names address one agent. An empty core means
// "the owner's default core" and matches any core of the same owner and
// provider (the dispatch guard's rule, so a role never lands on the lead).
func Same(a, b wire.PlanAgent) bool {
	if a.OwnerID != b.OwnerID || a.Provider != b.Provider {
		return false
	}
	return a.Core == "" || b.Core == "" || a.Core == b.Core
}

// Resolve staffs each role from the room's addressable agents (all of them
// online now): the role's default agent, never the lead's own agent (a
// dispatch to itself is refused). Several candidates pick the first by
// core and provider, so the choice is stable. A role nobody can take has no
// agent and a Reason.
func Resolve(requester string, lead wire.PlanAgent, agents []wire.PlanAgent, roles []wire.TeamPlanRole) []wire.TeamPlanEntry {
	pool := append([]wire.PlanAgent(nil), agents...)
	Name(pool)
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if a.OwnerID != b.OwnerID {
			return a.OwnerID < b.OwnerID
		}
		if a.Core != b.Core {
			return a.Core < b.Core
		}
		return a.Provider < b.Provider
	})
	out := make([]wire.TeamPlanEntry, 0, len(roles))
	for _, r := range roles {
		e := wire.TeamPlanEntry{Role: r.Role, DefaultAgent: r.DefaultAgent}
		da, err := contentpkg.ParseDefaultAgent(r.DefaultAgent)
		if err != nil {
			e.Reason = err.Error()
			out = append(out, e)
			continue
		}
		match := func(a wire.PlanAgent) bool {
			switch da.Kind {
			case contentpkg.AgentRequester:
				return !a.Platform && a.OwnerID == requester
			case contentpkg.AgentPlatform:
				return a.Platform
			default:
				return !a.Platform && a.OwnerID == da.Owner && (da.Provider == "" || a.Provider == da.Provider)
			}
		}
		leadOnly := false
		for _, a := range pool {
			if !match(a) {
				continue
			}
			if Same(a, lead) {
				leadOnly = true
				continue
			}
			agent := a
			e.Agent, e.Online = &agent, true
			break
		}
		if e.Agent == nil {
			e.Reason = unstaffed(da, requester, leadOnly)
		}
		out = append(out, e)
	}
	return out
}

func unstaffed(da contentpkg.DefaultAgent, requester string, leadOnly bool) string {
	who := ""
	switch da.Kind {
	case contentpkg.AgentRequester:
		who = requester + "'s own agent"
	case contentpkg.AgentPlatform:
		who = "a platform agent"
	default:
		who = da.Owner + "'s agent"
		if da.Provider != "" {
			who = da.Owner + "/" + da.Provider
		}
	}
	if leadOnly {
		return who + " is the lead itself (a role needs another agent)"
	}
	if da.Kind == contentpkg.AgentPlatform {
		return "no platform agent is added to this room"
	}
	return who + " is not online in this room"
}

// Estimate sums one team request: the lead plus every staffed role once,
// each at its agent's average reported cost. Seats whose agent reported no
// cost are named in Unknown and left out of the sum; CostUsd is absent when
// no seat has data. Runs counts the runs the sum rests on, each agent's
// once (two roles on one agent share its sample).
func Estimate(lead wire.TeamPlanEntry, roles []wire.TeamPlanEntry) wire.TeamCostEstimate {
	var est wire.TeamCostEstimate
	total, known := 0.0, false
	sampled := map[string]bool{}
	add := func(seat string, e wire.TeamPlanEntry) {
		if e.Agent == nil {
			return
		}
		if e.Cost == nil || e.Cost.AvgCostUsd == nil {
			est.Unknown = append(est.Unknown, seat)
			return
		}
		total += *e.Cost.AvgCostUsd
		if key := e.Agent.OwnerID + "/" + e.Agent.Core + "/" + e.Agent.Provider; !sampled[key] {
			sampled[key] = true
			est.Runs += e.Cost.Runs
		}
		known = true
	}
	add(LeadSeat, lead)
	for _, r := range roles {
		add(r.Role, r)
	}
	if known {
		est.CostUsd = &total
	}
	return est
}

// Section is the generated part of the lead's instructions: who leads, the
// roster (role → expert → agent, online or why not), and the rules the
// room tools enforce. It follows the team expert's own instructions (the
// SOP).
func Section(team wire.ContentTeam, plan wire.TeamPlanReply) string {
	byRole := map[string]wire.TeamPlanEntry{}
	for _, e := range plan.Roles {
		byRole[e.Role] = e
	}
	var b strings.Builder
	lead := team.Lead
	if lead == "" {
		lead = contentpkg.DefaultLeadRole
	}
	fmt.Fprintf(&b, "## Your team\n\nYou are the %s of this team. You do not do the members' work yourself: you plan, delegate each part to the role that covers it with the bnbloc-room tool ask_agent (pass the role name as the handle), and merge their results.\n\n", lead)
	b.WriteString("Roster (role → expert → agent):\n")
	var covered strings.Builder
	for _, m := range team.Members {
		expert := m.Expert
		covers := strings.TrimSpace(m.Description)
		if m.Resolved != nil {
			expert = firstNonEmpty(m.Resolved.DisplayName, m.Resolved.Name) + " v" + m.Resolved.Version
			covers = firstNonEmpty(covers, strings.TrimSpace(m.Resolved.Description))
		}
		agent := "no agent"
		e, ok := byRole[m.Role]
		switch {
		case m.Resolved == nil:
			agent = "unavailable: " + firstNonEmpty(m.Missing, "its expert does not resolve")
		case ok && e.Agent != nil:
			agent = e.Agent.Handle + " (online)"
		case ok:
			agent = "unavailable: " + e.Reason
		}
		fmt.Fprintf(&b, "- %s → %s → %s\n", m.Role, expert, agent)
		if covers != "" {
			covered.WriteString("- " + m.Role + ": " + roomlog.Truncate(oneLine(covers), MaxCoversBytes) + "\n")
		}
	}
	if covered.Len() > 0 {
		// Written by other authors: inside the lead's instructions, but
		// data to weigh, never instructions to follow.
		b.WriteString("\nWhat each role covers, as its authors describe it:\n")
		b.WriteString(roomlog.Fence(coversAuthor, covered.String()))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `
Rules:
- Capability check first: before you start, check that the roster covers every part of the request. If a part is covered by no role, say so and refuse that part; never invent a role or do a missing role's work yourself.
- Dispatch with ask_agent(handle: "<role>", task): the role's agent wears the role's expert. An unavailable role cannot be asked.
- Post one progress line per phase with post_progress (for example "Phase 1/3: review dispatched to reviewer"), so the room can follow.
- A member that does not finish in time keeps running: check it later with room_status. After %d failed member tasks the room tools stop further dispatches: then stop and tell the requester what failed.
`, wire.MaxTeamFailures)
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
