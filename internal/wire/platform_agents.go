package wire

// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2,
// docs/prd/2026-10-10-platform-agents-v2-onboarding.md §2–4): super admins
// publish some of their own agents platform-wide; any member who may chat can
// add one to a room and use it with no install and no per-room approval. A
// platform agent is a (core, provider) published by one or more hosts. The
// JSON tags are the public contract the web client reads.

// PlatformAgentRef names an agent by its core and provider.
type PlatformAgentRef struct {
	Core     string `json:"core"`
	Provider string `json:"provider"`
}

// PlatformAgentSpec is one agent a host publishes. DefaultModel (optional)
// is the model a platform turn runs with when the chat carried no explicit
// model; members may still pick another.
type PlatformAgentSpec struct {
	Core         string `json:"core"`
	Provider     string `json:"provider"`
	DefaultModel string `json:"defaultModel,omitempty"`
}

// Ref is the spec's (core, provider).
func (a PlatformAgentSpec) Ref() PlatformAgentRef {
	return PlatformAgentRef{Core: a.Core, Provider: a.Provider}
}

// PlatformAgentHost is one super admin's published agents: their entry in
// the policy. Only that super admin writes it.
type PlatformAgentHost struct {
	HostUserID string              `json:"hostUserId"`
	Enabled    bool                `json:"enabled"`
	Agents     []PlatformAgentSpec `json:"agents"`
}

// PlatformAgentsPolicy is the platform setting `platform_agents` and the
// answer of GET/PUT /v1/platform/platform-agents: one entry per host. PUT
// {enabled, agents} replaces the caller's entry only.
type PlatformAgentsPolicy struct {
	Hosts []PlatformAgentHost `json:"hosts"`
}

// Agent returns host's published spec of (core, provider) — found only while
// host's entry is enabled and lists it.
func (p PlatformAgentsPolicy) Agent(host, core, provider string) (PlatformAgentSpec, bool) {
	if host == "" {
		return PlatformAgentSpec{}, false
	}
	for _, h := range p.Hosts {
		if h.HostUserID != host || !h.Enabled {
			continue
		}
		for _, a := range h.Agents {
			if a.Core == core && a.Provider == provider {
				return a, true
			}
		}
	}
	return PlatformAgentSpec{}, false
}

// Publishes reports whether the policy offers host's (core, provider) now.
func (p PlatformAgentsPolicy) Publishes(host, core, provider string) bool {
	_, ok := p.Agent(host, core, provider)
	return ok
}

// PublishedHost is one host publishing an agent, with its spec.
type PublishedHost struct {
	HostUserID string
	Spec       PlatformAgentSpec
}

// PublishedAgent is a platform agent and every host publishing it now.
type PublishedAgent struct {
	Ref   PlatformAgentRef
	Hosts []PublishedHost
}

// Published groups the enabled entries' agents by (core, provider), in policy
// order (first appearance across hosts; hosts in policy order).
func (p PlatformAgentsPolicy) Published() []PublishedAgent {
	var out []PublishedAgent
	index := map[PlatformAgentRef]int{}
	for _, h := range p.Hosts {
		if !h.Enabled || h.HostUserID == "" {
			continue
		}
		for _, a := range h.Agents {
			ref := a.Ref()
			i, ok := index[ref]
			if !ok {
				i = len(out)
				index[ref] = i
				out = append(out, PublishedAgent{Ref: ref})
			}
			out[i].Hosts = append(out[i].Hosts, PublishedHost{HostUserID: h.HostUserID, Spec: a})
		}
	}
	return out
}

// HostsOf returns the hosts publishing (core, provider) now.
func (p PlatformAgentsPolicy) HostsOf(ref PlatformAgentRef) []PublishedHost {
	for _, a := range p.Published() {
		if a.Ref == ref {
			return a.Hosts
		}
	}
	return nil
}

// WithHost returns the policy with host's entry replaced by e (appended when
// host had none); every other host's entry is kept as is.
func (p PlatformAgentsPolicy) WithHost(e PlatformAgentHost) PlatformAgentsPolicy {
	out := PlatformAgentsPolicy{Hosts: make([]PlatformAgentHost, 0, len(p.Hosts)+1)}
	replaced := false
	for _, h := range p.Hosts {
		if h.HostUserID == e.HostUserID {
			h, replaced = e, true
		}
		out.Hosts = append(out.Hosts, h)
	}
	if !replaced {
		out.Hosts = append(out.Hosts, e)
	}
	return out
}

// PlatformAgentHostState is one host of a platform agent as members see it.
// Online: the host's live bridge advertises the agent; TargetID is the
// core-qualified target it advertises (as in GET /v1/my/agents; empty while
// offline); Queued counts the jobs addressed to this host's agent that have
// not started yet (queued or claimed, across organizations); DefaultModel is
// the host's default for the agent ("" = the core's own default).
type PlatformAgentHostState struct {
	HostUserID   string `json:"hostUserId"`
	Online       bool   `json:"online"`
	Queued       int    `json:"queued"`
	TargetID     string `json:"targetId"`
	DefaultModel string `json:"defaultModel,omitempty"`
}

// PlatformAgent is one platform agent (core, provider) as members see it,
// with the state of each host serving it. Online is true when any host is.
// In a room's listing Hosts are the hosts that serve the room, and
// AddedBy/AddedAtMs say who added the agent there and when (the earliest
// grant).
type PlatformAgent struct {
	Core      string                   `json:"core"`
	Provider  string                   `json:"provider"`
	Online    bool                     `json:"online"`
	Hosts     []PlatformAgentHostState `json:"hosts"`
	AddedBy   string                   `json:"addedBy,omitempty"`
	AddedAtMs int64                    `json:"addedAtMs,omitempty"`
}

// PlatformAgentsReply is GET /v1/platform-agents and
// GET /v1/rooms/{roomId}/platform-agents.
type PlatformAgentsReply struct {
	Agents []PlatformAgent `json:"agents"`
}

// ErrPlatformAgentOffline is the 409 error code of a chat addressed to a
// platform agent whose host is not online (no job is created).
const ErrPlatformAgentOffline = "platform_agent_offline"

// PlatformAgentOfflineReply is that 409 body.
type PlatformAgentOfflineReply struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	HostUserID string `json:"hostUserId"`
	Core       string `json:"core"`
	Provider   string `json:"provider"`
}

// PlatformAgentUsageTotals are the platform-agent runs of one period:
// distinct members and rooms, reported tokens (input + output) and cost.
type PlatformAgentUsageTotals struct {
	Runs    int     `json:"runs"`
	Members int     `json:"members"`
	Rooms   int     `json:"rooms"`
	Tokens  int64   `json:"tokens"`
	Cost    float64 `json:"cost"`
}

// PlatformAgentUsageMember is one member's platform-agent use this month.
type PlatformAgentUsageMember struct {
	MemberID string  `json:"memberId"`
	Runs     int     `json:"runs"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// PlatformAgentUsageRoom is one room's platform-agent use this month.
type PlatformAgentUsageRoom struct {
	RoomID   string  `json:"roomId"`
	RoomName string  `json:"roomName"`
	TenantID string  `json:"tenantId"`
	Runs     int     `json:"runs"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// PlatformAgentUsageRun is one platform-agent run. Agent is "core/provider";
// Model is what the core reported (else the requested or default model);
// Cost is what the host's bridge reported (0 when unpriced or unreported).
type PlatformAgentUsageRun struct {
	AtMs       int64   `json:"atMs"`
	MemberID   string  `json:"memberId"`
	RoomID     string  `json:"roomId"`
	RoomName   string  `json:"roomName"`
	TenantID   string  `json:"tenantId"`
	Agent      string  `json:"agent"`
	HostUserID string  `json:"hostUserId"`
	Model      string  `json:"model"`
	Cost       float64 `json:"cost"`
}

// PlatformAgentUsageReply is GET /v1/platform/platform-agents/usage (super
// admin): today's and this month's totals (UTC), the top 5 members and rooms
// of the month by runs, and the last 20 runs, newest first, across every
// organization. Truncated: the month's scan hit its row cap.
type PlatformAgentUsageReply struct {
	Today      PlatformAgentUsageTotals   `json:"today"`
	Month      PlatformAgentUsageTotals   `json:"month"`
	TopMembers []PlatformAgentUsageMember `json:"topMembers"`
	TopRooms   []PlatformAgentUsageRoom   `json:"topRooms"`
	Recent     []PlatformAgentUsageRun    `json:"recent"`
	Truncated  bool                       `json:"truncated"`
}
