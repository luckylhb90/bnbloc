package contentpkg

import (
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Expert teams (docs/prd/2026-10-12-experts-and-teams.md §8): an expert
// with a team block is a team. The agent the requester addresses with the
// team picked is the lead: it wears the team expert, whose INSTRUCTIONS.md
// is the SOP, and dispatches parts of the task to the roster's roles
// through the room tools. Each role names the expert its agent wears
// (name@range, resolved as the requester when the team runs) and the agent
// that takes it by default.

// TeamSpec is an expert's team block.
type TeamSpec struct {
	// Lead is the lead's role title, for display ("coordinator"); empty
	// reads as DefaultLeadRole.
	Lead    string       `json:"lead,omitempty"`
	Members []TeamMember `json:"members"`
}

// TeamMember is one role of a team.
type TeamMember struct {
	// Role names the role ([a-z0-9-]); the lead dispatches to it by name.
	Role string `json:"role"`
	// Expert is the expert the role's agent wears, "name@range".
	Expert string `json:"expert"`
	// DefaultAgent is the agent that takes the role (ParseDefaultAgent).
	DefaultAgent string `json:"defaultAgent"`
	// Description says what the role covers (the lead's capability check
	// reads it; the member expert's description is used when empty).
	Description string `json:"description,omitempty"`
}

// DefaultLeadRole is the lead's title when a team names none.
const DefaultLeadRole = "lead"

// Default agents of a team role.
const (
	// AgentRequester: the requester's own agent (never the lead's).
	AgentRequester = "requester"
	// AgentPlatform: a platform agent added to the room.
	AgentPlatform = "platform"
	// AgentMemberPrefix + "<member id>" or "<member id>/<provider>": that
	// member's agent.
	AgentMemberPrefix = "member:"
)

// Team bounds. A lead may dispatch at most wire.MaxDispatchFanOut tasks per
// request, so a larger roster could never be fully used.
const (
	maxTeamMembers   = wire.MaxDispatchFanOut
	maxRoleDesc      = 300
	maxTeamMemberRef = 128
)

// DefaultAgent is a parsed defaultAgent.
type DefaultAgent struct {
	// Kind is AgentRequester, AgentPlatform or "member".
	Kind string
	// Owner / Provider name a member's agent (Provider "" = any of theirs).
	Owner    string
	Provider string
}

// AgentMember is DefaultAgent.Kind for a named member's agent.
const AgentMember = "member"

// ParseDefaultAgent parses a team role's defaultAgent.
func ParseDefaultAgent(s string) (DefaultAgent, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == AgentRequester || s == AgentPlatform:
		return DefaultAgent{Kind: s}, nil
	case strings.HasPrefix(s, AgentMemberPrefix):
		rest := strings.TrimPrefix(s, AgentMemberPrefix)
		owner, provider, _ := strings.Cut(rest, "/")
		owner, provider = strings.TrimSpace(owner), strings.TrimSpace(provider)
		if owner == "" || len(rest) > maxTeamMemberRef || strings.ContainsAny(owner, " \t\n/") || strings.ContainsAny(provider, " \t\n/") || (strings.Contains(rest, "/") && provider == "") {
			return DefaultAgent{}, fmt.Errorf("defaultAgent %q: use member:<member id> or member:<member id>/<provider>", s)
		}
		return DefaultAgent{Kind: AgentMember, Owner: owner, Provider: provider}, nil
	}
	return DefaultAgent{}, fmt.Errorf("defaultAgent %q: use %q, %q or %q<member id>[/<provider>]", s, AgentRequester, AgentPlatform, AgentMemberPrefix)
}

// LeadRole is the lead's title.
func (t TeamSpec) LeadRole() string {
	if l := strings.TrimSpace(t.Lead); l != "" {
		return l
	}
	return DefaultLeadRole
}

// Member returns the role's member.
func (t TeamSpec) Member(role string) (TeamMember, bool) {
	for _, m := range t.Members {
		if m.Role == role {
			return m, true
		}
	}
	return TeamMember{}, false
}

// validateTeam checks a team block: 1..maxTeamMembers roles with unique
// names other than the lead's, each with an expert reference and a
// default agent.
func validateTeam(t TeamSpec) error {
	if err := checkText("team.lead", t.Lead, maxQuickTitle, true); err != nil {
		return err
	}
	if len(t.Members) == 0 {
		return fmt.Errorf("a team lists at least one member")
	}
	if len(t.Members) > maxTeamMembers {
		return fmt.Errorf("a team has at most %d members (a lead dispatches at most %d tasks per request)", maxTeamMembers, wire.MaxDispatchFanOut)
	}
	lead := strings.ToLower(t.LeadRole())
	roles := map[string]bool{}
	for i, m := range t.Members {
		if !ValidName(m.Role) {
			return fmt.Errorf("team member %d: role %q: use lower-case letters, digits and hyphens", i+1, m.Role)
		}
		if roles[m.Role] {
			return fmt.Errorf("team role %q is listed twice", m.Role)
		}
		if m.Role == lead {
			return fmt.Errorf("team role %q is the lead's", m.Role)
		}
		roles[m.Role] = true
		if err := checkReference("team role "+m.Role+" expert", m.Expert); err != nil {
			return err
		}
		if _, err := ParseDefaultAgent(m.DefaultAgent); err != nil {
			return fmt.Errorf("team role %s: %w", m.Role, err)
		}
		if err := checkText("team role "+m.Role+" description", m.Description, maxRoleDesc, true); err != nil {
			return err
		}
	}
	return nil
}
