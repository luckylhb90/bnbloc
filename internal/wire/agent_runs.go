package wire

import (
	"strings"
	"unicode/utf8"
)

// Agent run records (R10). A run is one agent job seen as a record: the
// request, the addressed agent, per-phase timestamps, outcome, a reply
// preview, and reported usage. JSON tags are the public contract the web
// client reads; a field with no source is omitted (absent cost ≠ zero cost).

// Run listing scopes (AgentRunsReply.Scope).
//
//wire:union RunScope
const (
	RunScopeRoom   = "room"
	RunScopeSelf   = "self"
	RunScopeTenant = "tenant"
)

// Keys of the run summary a bridge adds to its job-status report diagnostics
// (POST /v1/bridge/jobs/{jobId}/status). Additive: older bridges send neither
// and their runs simply have no reply preview or usage.
const (
	RunDiagReply = "reply" // AgentRunReply (without RoomSeq)
	RunDiagUsage = "usage" // AgentRunUsage
	RunDiagTools = "tools" // AgentRunTools
)

// AgentRunTools is the run summary's tools (RunDiagTools): the MCP tool
// components attached to the turn's session, by name. The record keeps what
// the session had, never which tool calls the agent made.
type AgentRunTools struct {
	MCP []string `json:"mcp"`
}

// RunPreviewRunes bounds every text preview a run record carries.
const RunPreviewRunes = 280

// RunPreview trims s and cuts it to RunPreviewRunes runes (with an ellipsis).
func RunPreview(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= RunPreviewRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:RunPreviewRunes])) + "…"
}

// AgentRunRequest is the room message that requested the run.
type AgentRunRequest struct {
	// MessageSeq is the request's room sequence (jump-to-message target).
	MessageSeq  int64  `json:"messageSeq"`
	ClientMsgID string `json:"clientMsgId,omitempty"`
	// Text is a preview of the request (absent when redacted).
	Text        string `json:"text,omitempty"`
	Attachments int    `json:"attachments,omitempty"`
}

// AgentRunAgent is the addressed agent: owner/core/provider plus the target
// and the model options the request chose.
type AgentRunAgent struct {
	Owner           string `json:"owner"`
	Core            string `json:"core"`
	Provider        string `json:"provider"`
	TargetID        string `json:"targetId,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// AgentRunPhases are the run's phase timestamps (unix ms; absent = the phase
// never happened). Queued is the job's creation; Claimed the latest bridge
// claim; Started the first time a bridge began driving; FirstReply when the
// first visible reply was produced (bridge-reported); Finished when the job
// reached its terminal status (replied/failed/timed out/cancelled).
type AgentRunPhases struct {
	QueuedAtMs     int64 `json:"queuedAtMs"`
	ClaimedAtMs    int64 `json:"claimedAtMs,omitempty"`
	StartedAtMs    int64 `json:"startedAtMs,omitempty"`
	FirstReplyAtMs int64 `json:"firstReplyAtMs,omitempty"`
	FinishedAtMs   int64 `json:"finishedAtMs,omitempty"`
	UpdatedAtMs    int64 `json:"updatedAtMs,omitempty"`
}

// AgentRunReply summarizes the run's visible replies. MessageID/Text/AtMs
// describe the first reply; Count is how many the run posted. RoomSeq is the
// first reply's room sequence, resolved by the run detail route only.
type AgentRunReply struct {
	MessageID string `json:"messageId,omitempty"`
	RoomSeq   int64  `json:"roomSeq,omitempty"`
	Text      string `json:"text,omitempty"`
	AtMs      int64  `json:"atMs,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// AgentRunExpert is the expert of a run at the version the job pinned.
type AgentRunExpert struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// AgentRunUsage is the usage the run's replies reported, summed. Tokens and
// cost are present only when reported.
type AgentRunUsage struct {
	Model        string   `json:"model,omitempty"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
	CostUsd      *float64 `json:"costUsd,omitempty"`
}

// AgentRun is one run record.
type AgentRun struct {
	JobID     string `json:"jobId"`
	RoomID    string `json:"roomId"`
	RoomName  string `json:"roomName,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	// Requester asked for the run; Borrowed when that is not the agent owner.
	Requester     string `json:"requester"`
	Borrowed      bool   `json:"borrowed,omitempty"`
	Status        string `json:"status" tstype:"AgentJobStatus"`
	FailureReason string `json:"failureReason,omitempty" tstype:"AgentJobFailureReason"`
	Error         string `json:"error,omitempty"`
	Warning       string `json:"warning,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	// Attempts are the run's earlier attempts and why each was retried.
	Attempts []AgentJobAttempt `json:"attempts,omitempty"`
	Request  AgentRunRequest   `json:"request"`
	Agent    AgentRunAgent     `json:"agent"`
	Phases   AgentRunPhases    `json:"phases"`
	Reply    *AgentRunReply    `json:"reply,omitempty"`
	Usage    *AgentRunUsage    `json:"usage,omitempty"`
	// DispatchedBy is set when an agent dispatched this run through the room
	// tools (the parent job and the hop depth).
	DispatchedBy *AgentRunDispatch `json:"dispatchedBy,omitempty"`
	// Expert is the expert the agent wore for this run ("expert X v1.2 on
	// agent Y"); absent when the chat picked none.
	Expert *AgentRunExpert `json:"expert,omitempty"`
	// RoomExpert is the room's expert the turn wore under the chat's own,
	// recorded when the turn resolved it; absent when the room had none.
	RoomExpert *AgentRunExpert `json:"roomExpert,omitempty"`
	// Tools are the MCP tool components the turn's session had
	// (AgentRunTools), by name; absent when none or reported by an older
	// bridge.
	Tools []string `json:"tools,omitempty"`
	// Redacted marks a run whose request/reply text and diagnostics are
	// withheld because the viewer is not a member of the run's room.
	Redacted bool `json:"redacted,omitempty"`
	// Diagnostics is the bridge's last raw status report: detail route only,
	// and only to the agent's owner and org admins (O4).
	Diagnostics map[string]any `json:"diagnostics,omitempty"`
}

// AgentRunsReply is GET /v1/rooms/{id}/jobs (scope room), GET /v1/my/jobs
// (scope self) and GET /v1/tenant/jobs (scope tenant). NextBefore is the
// opaque cursor of the next (older) page; absent on the last page.
type AgentRunsReply struct {
	Scope      string     `json:"scope" tstype:"RunScope"`
	Runs       []AgentRun `json:"runs"`
	NextBefore string     `json:"nextBefore,omitempty"`
}
