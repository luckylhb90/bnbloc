package wire

// AgentJob is an agent job as a room member sees it: the payload of the
// agent_job_created and agent_job_updated room events and a row of
// GET /v1/rooms/{id}/agent-jobs (decision O4). The tenant, the claiming
// bridge and its lease, and the bridge's raw diagnostics stay server-side;
// what a member needs from the diagnostics is projected into named fields.
// store.AgentJob.View builds it, the one projection.
type AgentJob struct {
	JobID           string `json:"jobId"`
	RoomID          string `json:"roomId"`
	ChannelID       string `json:"channelId"`
	MessageSeq      int64  `json:"messageSeq"`
	RequesterUserID string `json:"requesterUserId"`
	OwnerUserID     string `json:"ownerUserId"`
	Core            string `json:"core"`
	Provider        string `json:"provider"`
	// MessageText is the request (already in the room's chat).
	MessageText     string `json:"messageText"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Status          string `json:"status" tstype:"AgentJobStatus"`
	FailureReason   string `json:"failureReason,omitempty" tstype:"AgentJobFailureReason"`
	Attempt         int    `json:"attempt"`
	CreatedAtMs     int64  `json:"createdAtMs"`
	UpdatedAtMs     int64  `json:"updatedAtMs"`
	ClaimedAtMs     int64  `json:"claimedAtMs,omitempty"`
	StartedAtMs     int64  `json:"startedAtMs,omitempty"`
	FinishedAtMs    int64  `json:"finishedAtMs,omitempty"`
	// DispatchedByJobID / DispatchDepth: the job whose agent dispatched this
	// one through the room tools, and the hops from the person's message.
	DispatchedByJobID string `json:"dispatchedByJobId,omitempty"`
	DispatchDepth     int    `json:"dispatchDepth,omitempty"`
	// Expert* is the expert the chat asked the agent to wear (pinned at
	// post); RoomExpert* the room's expert the turn wore.
	ExpertID          string `json:"expertId,omitempty"`
	ExpertVersion     string `json:"expertVersion,omitempty"`
	ExpertName        string `json:"expertName,omitempty"`
	RoomExpertID      string `json:"roomExpertId,omitempty"`
	RoomExpertVersion string `json:"roomExpertVersion,omitempty"`
	RoomExpertName    string `json:"roomExpertName,omitempty"`
	// Error / Warning say why the job failed, or what went wrong after a
	// reply was posted (the same text the run record shows a member).
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
	// ContentMissing names requirements the job's content could not meet
	// (a skill pack an expert requires, a room expert that is gone); the
	// turn runs without them.
	ContentMissing []string `json:"contentMissing,omitempty"`
	// ReplySessionID is the activity session a replied job's reply was
	// posted under (notifications pair the reply with its job by it).
	ReplySessionID string `json:"replySessionId,omitempty"`
}

// AgentJobsReply is GET /v1/rooms/{id}/agent-jobs.
type AgentJobsReply struct {
	Jobs []AgentJob `json:"jobs"`
}
