package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/luckylhb90/bnbloc/internal/expertteam"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Expert teams (docs/prd/2026-10-12-experts-and-teams.md §8): a job whose
// expert is a team is led by the agent it addresses. The gateway resolves
// the roles' experts as the requester (ContentEffective.Team); activity
// staffs the roles with agents (POST /v1/rooms/{room}/team-plan?job=). The
// lead wears the team expert's instructions (the SOP) plus the generated
// roster section, and the roster is bound to the turn's room tools session
// so ask_agent dispatches to roles.

// teamPlan asks activity which agent takes each role for the job.
func (b *Bridge) teamPlan(ctx context.Context, job bridgeJob, team *wire.ContentTeam) (wire.TeamPlanReply, error) {
	req := wire.TeamPlanRequest{Members: make([]wire.TeamPlanRole, 0, len(team.Members))}
	for _, m := range team.Members {
		req.Members = append(req.Members, wire.TeamPlanRole{Role: m.Role, DefaultAgent: m.DefaultAgent})
	}
	raw, err := b.gatewayPOST(ctx, "/v1/rooms/"+url.PathEscape(job.RoomID)+"/team-plan?job="+url.QueryEscape(job.JobID), req)
	if err != nil {
		return wire.TeamPlanReply{}, fmt.Errorf("staff the team: %w", err)
	}
	var plan wire.TeamPlanReply
	if err := json.Unmarshal(raw, &plan); err != nil {
		return wire.TeamPlanReply{}, fmt.Errorf("staff the team: %w", err)
	}
	return plan, nil
}

// teamContent adds a team to the job's content: the roster section after
// the SOP (content only: no staffing, which would churn the session's
// binding sum as agents come and go), and the roster the room tools
// dispatch by and report (list_room_agents), staffed for this turn.
func (b *Bridge) teamContent(ctx context.Context, job bridgeJob, c *jobContent, team *wire.ContentTeam) error {
	plan, err := b.teamPlan(ctx, job, team)
	if err != nil {
		return err
	}
	section := expertteam.Section(*team)
	if c.instructions != "" {
		c.instructions += "\n\n"
	}
	c.instructions += section
	staffed := map[string]wire.TeamPlanEntry{}
	for _, e := range plan.Roles {
		staffed[e.Role] = e
	}
	t := &roomtools.Team{Lead: team.Lead}
	if c.expert != nil {
		t.Name = firstNonEmpty(c.expert.DisplayName, c.expert.Name)
	}
	for _, m := range team.Members {
		e := staffed[m.Role]
		t.Roles = append(t.Roles, roomtools.TeamRole{Role: m.Role, Agent: e.Agent, Reason: e.Reason, Expert: m.Resolved, Missing: m.Missing})
	}
	c.team = t
	return nil
}

// teamRoster is the roster the turn's room tools dispatch by (nil: no team).
func (c *jobContent) teamRoster() *roomtools.Team {
	if c == nil {
		return nil
	}
	return c.team
}
