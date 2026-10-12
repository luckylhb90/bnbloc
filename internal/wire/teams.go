package wire

// Expert teams (docs/prd/2026-10-12-experts-and-teams.md §8): an expert
// with a team block. The lead (the agent the requester addresses with the
// team picked) wears the team expert and dispatches to the roster's roles
// through the room tools. Two resolutions make a roster runnable: member
// experts resolve as the requester (ContentEffective.Team, gateway) and
// member agents against the room's addressable agents (TeamPlanReply,
// activity's POST /v1/rooms/{roomId}/team-plan).

// MaxTeamFailures is how many member tasks of one request may fail before
// the room tools refuse the lead further dispatches. A protocol safety
// constant beside MaxDispatchDepth and MaxDispatchFanOut: it bounds what a
// failing team spends on one person's request, the same everywhere.
const MaxTeamFailures = 3

// ContentTeam is a team expert's roster. In a listing or detail it is the
// manifest's; in ContentEffective each member carries its resolution.
type ContentTeam struct {
	// Lead is the lead's role title.
	Lead    string              `json:"lead"`
	Members []ContentTeamMember `json:"members"`
}

// ContentTeamMember is one role of a team.
type ContentTeamMember struct {
	Role         string `json:"role"`
	Expert       string `json:"expert"`
	DefaultAgent string `json:"defaultAgent"`
	Description  string `json:"description,omitempty"`
	// Resolved is the member expert the requester may use (effective
	// only); Missing says why there is none.
	Resolved *ContentTeamExpert `json:"resolved,omitempty"`
	Missing  string             `json:"missing,omitempty"`
}

// ContentTeamExpert is a team member's expert as it resolved.
type ContentTeamExpert struct {
	ComponentID string `json:"componentId"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

// PlanAgent names one agent of a room: owner, core and provider, plus its
// handle as the room tools name it ("owner/provider", "owner/core/provider"
// where an owner exposes the provider on two cores).
type PlanAgent struct {
	OwnerID  string `json:"ownerId"`
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider"`
	Handle   string `json:"handle,omitempty"`
	// Platform marks a platform agent (served by a host member).
	Platform bool `json:"platform,omitempty"`
}

// TeamPlanRole is one role the plan staffs: its name and default agent.
type TeamPlanRole struct {
	Role         string `json:"role"`
	DefaultAgent string `json:"defaultAgent"`
}

// TeamPlanRequest is POST /v1/rooms/{roomId}/team-plan. Lead is the agent
// the requester picked (the composer); with ?job= (the lead's bridge) the
// lead and the requester are the job's and Lead is ignored.
type TeamPlanRequest struct {
	Lead    *PlanAgent     `json:"lead,omitempty"`
	Members []TeamPlanRole `json:"members"`
}

// AgentCostSample is an agent's recent reported cost: how many of its last
// finished runs reported one, and their average (absent when none did).
type AgentCostSample struct {
	Runs       int      `json:"runs"`
	AvgCostUsd *float64 `json:"avgCostUsd,omitempty"`
}

// TeamPlanEntry is one staffed (or unstaffed) seat of the plan.
type TeamPlanEntry struct {
	// Role is the role's name ("" for the lead).
	Role         string `json:"role,omitempty"`
	DefaultAgent string `json:"defaultAgent,omitempty"`
	// Agent is the agent that takes the seat; absent when none can, and
	// Reason says why.
	Agent  *PlanAgent       `json:"agent,omitempty"`
	Online bool             `json:"online"`
	Reason string           `json:"reason,omitempty"`
	Cost   *AgentCostSample `json:"cost,omitempty"`
}

// TeamCostEstimate is the plan's pre-flight estimate of one team request:
// the lead plus every staffed role once, from each agent's average
// reported cost. CostUsd is absent when no seat has data; Unknown names the
// seats ("lead" or a role) without data, which the sum leaves out.
type TeamCostEstimate struct {
	CostUsd *float64 `json:"costUsd,omitempty"`
	Runs    int      `json:"runs"`
	Unknown []string `json:"unknown,omitempty"`
}

// TeamPlanReply answers POST /v1/rooms/{roomId}/team-plan.
type TeamPlanReply struct {
	Lead     TeamPlanEntry    `json:"lead"`
	Roles    []TeamPlanEntry  `json:"roles"`
	Estimate TeamCostEstimate `json:"estimate"`
}
