package wire

// A live job's progress (docs/prd/2026-10-13-live-turns.md §3): the bridge
// relays its core's turn activity (Core Protocol PollTurnResponse.Activity)
// to activity (POST /v1/bridge/jobs/{jobId}/progress), which fans it to the
// job's room as EventAgentJobProgress. Transient: never stored in history,
// never replayed, never fed back to an agent. A job that leaves the live
// statuses (agent_job_updated) has none: the web drops it then.

// EventAgentJobProgress carries AgentJobProgress (activity), scoped to the
// job's channel.
const EventAgentJobProgress = "agent_job_progress"

// Job activity kinds (JobActivity.Kind): Core Protocol's turn activity
// kinds (core/protocol TurnActivity*; core/protocol TestTurnActivityIsTheWires).
//
//wire:union JobActivityKind
const (
	// JobActivityTool: a step the agent ran (a command, a file read or
	// written, a search).
	JobActivityTool = "tool"
	// JobActivityThinking: the agent is reasoning.
	JobActivityThinking = "thinking"
	// JobActivityText: the agent is writing.
	JobActivityText = "text"
)

// Bounds of a job's progress (Core Protocol's MaxTurnActivity and
// MaxTurnActivityTitle; the bridge cuts to them).
const (
	MaxJobActivity      = 8
	MaxJobActivityTitle = 120
)

// JobActivity is one step of a live job's turn: a short title such as
// "Bash: python extract.py" (never file contents or secrets).
type JobActivity struct {
	Kind  string `json:"kind" tstype:"JobActivityKind"`
	Title string `json:"title,omitempty"`
	AtMs  int64  `json:"atMs"`
}

// AgentJobProgress is EventAgentJobProgress: a live job's turn start and
// latest steps, oldest first (at most MaxJobActivity). AtMs is when the
// bridge read it.
type AgentJobProgress struct {
	JobID       string        `json:"jobId"`
	StartedAtMs int64         `json:"startedAtMs,omitempty"`
	Activity    []JobActivity `json:"activity"`
	AtMs        int64         `json:"atMs"`
}

// JobProgressInput is POST /v1/bridge/jobs/{jobId}/progress: the claiming
// bridge's report of its job's turn.
type JobProgressInput struct {
	BridgeID    string        `json:"bridgeId"`
	StartedAtMs int64         `json:"startedAtMs,omitempty"`
	Activity    []JobActivity `json:"activity"`
}
