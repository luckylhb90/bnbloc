package wire

// Agent model catalogs. Each bridge uploads, with its heartbeat, the model
// options its core reports for every agent it serves (store.BridgeAgent
// ModelOptionsJSON). The catalog is environment data owned by the agent's
// core: bnbloc relays it, never invents one.

// AgentModelOptions is one agent's model catalog as its core reports it.
type AgentModelOptions struct {
	Models           []string            `json:"models,omitempty"`
	ReasoningEfforts []string            `json:"reasoningEfforts,omitempty"`
	ReasoningByModel map[string][]string `json:"reasoningByModel,omitempty"`
	DefaultModel     string              `json:"defaultModel,omitempty"`
	DefaultReasoning string              `json:"defaultReasoning,omitempty"`
}

// RoomAgentCatalog is one agent a room member may address, keyed by its owner
// (owner, core, provider), with the catalog its owner's live bridge
// advertises. Online is always true today (only live advertisements are
// listed); it is kept so a client never infers liveness from presence alone.
type RoomAgentCatalog struct {
	OwnerUserID  string             `json:"ownerUserId"`
	Core         string             `json:"core"`
	Provider     string             `json:"provider"`
	Online       bool               `json:"online"`
	ModelOptions *AgentModelOptions `json:"modelOptions,omitempty"`
}

// RoomAgentCatalogsReply is GET /v1/rooms/{roomId}/agent-catalogs (room
// members): the live agents of the room's members, plus the room's added
// platform agents of its agent-host members (never a host's other agents).
type RoomAgentCatalogsReply struct {
	Agents []RoomAgentCatalog `json:"agents"`
}
