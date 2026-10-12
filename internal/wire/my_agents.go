package wire

// MyAgent is one agent the caller's own bridge advertises (any room).
// Online is false while the owner disabled it.
type MyAgent struct {
	Core         string             `json:"core"`
	Provider     string             `json:"provider"`
	TargetID     string             `json:"targetId"`
	Online       bool               `json:"online"`
	ModelOptions *AgentModelOptions `json:"modelOptions,omitempty"`
}

// MyDesktop is the caller's own desktop app, apart from its agents: Online
// is its bridge connected now (even with no agent core); LastSeenAtMs the
// latest connection, 0 when the app never connected (not installed).
type MyDesktop struct {
	Online       bool  `json:"online"`
	LastSeenAtMs int64 `json:"lastSeenAtMs"`
}

// MyAgentsReply is GET /v1/my/agents: the same agent list the member's
// desktop app shows, because both render what their bridge advertises.
type MyAgentsReply struct {
	Agents  []MyAgent `json:"agents"`
	Desktop MyDesktop `json:"desktop"`
}
