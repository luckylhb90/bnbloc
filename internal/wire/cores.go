package wire

// AgentTarget is one agent a core can drive for the caller
// (GET /v1/local-agent/targets). ID is core-qualified ("<core>/<rawid>",
// core.QualifyTargetID); Core names the core for grouping. The model and
// reasoning pickers are what the core discovered (absent: no picker).
type AgentTarget struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Core     string `json:"core"`
	AgentModelOptions
}

// AgentTargetsReply is GET /v1/local-agent/targets.
type AgentTargetsReply struct {
	Targets []AgentTarget `json:"targets"`
}

// Core is one registered core as GET /v1/cores lists it to a member (the
// rollout gate applied). Available means the gateway resolved an endpoint
// for it; Runtime "sidecar" cores are driven by the member's own bridge.
type Core struct {
	Name            string `json:"name"`
	Available       bool   `json:"available"`
	Backend         string `json:"backend"`
	Runtime         string `json:"runtime,omitempty"`
	RequiresDesktop bool   `json:"requiresDesktop,omitempty"`
	Rollout         string `json:"rollout,omitempty"`
}

// CoresReply is GET /v1/cores.
type CoresReply struct {
	Cores []Core `json:"cores"`
}
