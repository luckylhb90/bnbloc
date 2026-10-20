package wire

// Agent dispatch (room tools, docs/prd/2026-10-12-experts-and-teams.md §7):
// an agent running a job asks another agent of the room for part of the
// work. The bridge's room-tools server posts DispatchRequest to the gateway
// (POST /v1/rooms/{roomId}/dispatch), which signs it as the bridge's owner
// and hands it to realtime's internal dispatch route on the room's owner
// replica. The dispatched message is posted as the parent job's human
// requester, so the new job runs under that person's authority (borrow
// authorization, quota, budgets, sandbox policy) exactly like a typed one.

// Dispatch guard limits. They are protocol safety constants, not deployment
// data: they bound how far one person's message can fan out through agents
// (at most MaxDispatchFanOut^MaxDispatchDepth jobs), the same for every
// deployment, and the room-tools contract documents them to agents.
const (
	// MaxDispatchDepth is the deepest hop a dispatched job may sit at: a
	// person's message starts depth 0, each dispatch adds one.
	MaxDispatchDepth = 3
	// MaxDispatchFanOut is how many jobs one parent job may dispatch
	// (persisted dispatches, whatever their outcome).
	MaxDispatchFanOut = 8
)

// DispatchMarker is payload.dispatchedBy on a chat_message an agent
// dispatched: the parent job and its request, the new job's depth and the
// dispatching agent's handle ("jack/codex"). Clients render "maya → via
// jack/codex → bob/claude". An expert the dispatch asked the target to wear is the
// message's ordinary payload.expert.
type DispatchMarker struct {
	JobID string `json:"jobId"`
	// ParentSeq is the room sequence of the parent job's request: clients
	// nest the dispatch (and, through its replies' sourceSeq, everything it
	// produced) under that message without looking jobs up.
	ParentSeq int64  `json:"parentSeq,omitempty"`
	Depth     int    `json:"depth"`
	Via       string `json:"via,omitempty"`
	// Role is the team role the dispatch staffs (a team lead's dispatch).
	Role string `json:"role,omitempty"`
}

// DispatchRecipient names the agent a dispatch addresses, in the chat
// recipient's shape.
type DispatchRecipient struct {
	OwnerID  string `json:"ownerId"`
	Provider string `json:"provider"`
	Core     string `json:"core,omitempty"`
}

// DispatchRequest is the body of POST /v1/rooms/{roomId}/dispatch (and of
// realtime's POST /internal/v1/rooms/{roomId}/dispatch). Expert asks the
// target to wear an expert exactly as a typed chat does (it must be usable
// by the requester, who authors the post). ClientMsgID makes a retried
// dispatch idempotent.
type DispatchRequest struct {
	ParentJobID string            `json:"parentJobId"`
	Recipient   DispatchRecipient `json:"recipient"`
	Text        string            `json:"text"`
	Expert      *ChatExpert       `json:"expert,omitempty"`
	ClientMsgID string            `json:"clientMsgId,omitempty"`
	// Role names the team role the dispatch staffs ([a-z0-9-]); the feed
	// labels the dispatch with it.
	Role string `json:"role,omitempty"`
	// Draft asks the target to wear a staged draft (ContentDraft.ID, the
	// room tools' components try) instead of an Expert: admitted only for a
	// draft the parent job staged in this room, before it expires.
	Draft string `json:"draft,omitempty"`
}

// DispatchReply answers an accepted dispatch: the posted message's room
// sequence (deliverables link back to it as sourceSeq) and the new job.
type DispatchReply struct {
	Sequence int64  `json:"sequence"`
	JobID    string `json:"jobId"`
	Depth    int    `json:"depth"`
}

// Dispatch refusal codes: the JSON body of a refused dispatch is
// {"code": <code>, "error": <message>}.
const (
	DispatchParentNotFound   = "parent_not_found"
	DispatchParentNotRunning = "parent_not_running"
	DispatchNotParentBridge  = "not_parent_bridge"
	DispatchRequesterGone    = "requester_not_member"
	DispatchDepthExceeded    = "depth_exceeded"
	DispatchFanOutExceeded   = "fan_out_exceeded"
	DispatchSelf             = "self_dispatch"
	DispatchCycle            = "dispatch_cycle"
	DispatchPeopleChat       = "people_chat"
	DispatchExpertUnusable   = "expert_not_available"
	DispatchDraftUnavailable = "draft_not_available"
	DispatchInvalid          = "invalid_dispatch"
)

// AgentRunDispatch is a run's dispatch origin: the parent job and the hop
// depth (run records of jobs an agent dispatched).
type AgentRunDispatch struct {
	JobID string `json:"jobId"`
	Depth int    `json:"depth"`
}
