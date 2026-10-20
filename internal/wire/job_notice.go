package wire

import "strings"

// JobNotice is one notice row about a job in the room feed: a line that says
// why nothing (or nothing more) came back — a refused or failed drive, a
// swept or budget-refused job, a team member that timed out, a lead's
// progress. It is a synthetic assistant row of an agent_messages batch whose
// payload carries `notice` (its kind): clients render it as a notice, and
// every reader that feeds the room back to an agent skips it (roomlog,
// notify: an agent must never read a notice as something it or another agent
// said). Every writer — the bridge, activity, the room tools — builds its row
// here (Payload, Report), and nothing else sets payload.notice
// (TestNoticesCarryKind).
type JobNotice struct {
	// Kind is payload.notice ("borrow-denied", NoticeProgress, …).
	Kind string
	// MessageID is the row's stable message id (idempotent on retry);
	// Kind-Session when empty.
	MessageID string
	// JobID and SourceSeq (the room sequence of the message that started
	// the job) let the feed nest the notice under its request.
	JobID     string
	SourceSeq int64
	Text      string
	// Detail is the agent's or its core's own error output, verbatim but
	// cut (a failed turn's failMessage): never bnbloc's sentence and never
	// Markdown. Clients render it fenced (monospace, apart from Text).
	Detail string
	// OwnerUserID / Provider attribute the notice to the agent it is about.
	OwnerUserID string
	Provider    string
	// FailReason is the job failure reason the notice reports ("" = none).
	FailReason string
	// Extra holds kind-specific fields (e.g. denyReason); it can never
	// override the fields above.
	Extra map[string]any

	// Where the row is recorded (Report). RoomID and ChannelID place it;
	// Session is the activity session it goes under
	// (JobNoticeSessionID(JobID) when empty): never a real agent session,
	// whose reply cursor a notice would move. Seq is its sequence there
	// (JobNoticeSeq when 0: one notice per job, a retry is a replay), and
	// AtUnixMs its creation time.
	RoomID    string
	ChannelID string
	Session   string
	Seq       int64
	AtUnixMs  int64
}

// Notice kinds written outside the bridge's refusal table (payload.notice).
const (
	// NoticeProgress is a team lead's progress line (post_progress).
	NoticeProgress = "progress"
	// NoticeTimeout is a team member that has not answered in time.
	NoticeTimeout = "member-timeout"
	// NoticeJobFailed is a job the server failed (a swept lease, a budget
	// refusal at claim).
	NoticeJobFailed = "job-failed"
	// NoticeAttachmentUndelivered names a message's attachments that did not
	// reach the agent's workspace, and why (the bridge, once per job, under
	// AttachmentNoticeSessionID so the job's own notice still posts).
	NoticeAttachmentUndelivered = "attachment-undelivered"
	// NoticeContextReset says a turn outgrew its model's context window and
	// continues in a fresh session (the bridge, at most once per job, under
	// ContextResetNoticeSessionID so the job's own notice still posts).
	NoticeContextReset = "context-reset"
	// NoticeFileEdit says the file a request asked to change did not
	// change: it never reached the agent (and why), or the agent left it
	// as it was (the bridge, once per job, under FileEditNoticeSessionID).
	NoticeFileEdit = "file-edit"
	// NoticeRoomSetup is a change an agent made to the room's setup for
	// the person who asked it ("maya's agent set the welcome"; room_setup,
	// a line each, under the job's progress session).
	NoticeRoomSetup = "room-setup"
	// NoticePublishProposal asks the person who asked the agent to share a
	// component (components publish): payload.proposal is a PublishProposal,
	// shown to them only.
	NoticePublishProposal = "publish-proposal"
)

// AttachmentNoticeSessionID is the activity session of a job's
// attachment-undelivered notice: in the job's notice namespace, apart from
// JobNoticeSessionID itself, whose one notice per job is the job's refusal
// or failure.
func AttachmentNoticeSessionID(jobID string) string {
	return JobNoticeSessionID(jobID) + "-attachments"
}

// ContextResetNoticeSessionID is the activity session of a job's
// context-reset notice, apart from the job's own notice.
func ContextResetNoticeSessionID(jobID string) string {
	return JobNoticeSessionID(jobID) + "-context"
}

// FileEditNoticeSessionID is the activity session of a job's file-edit
// notice, apart from the job's own notice.
func FileEditNoticeSessionID(jobID string) string {
	return JobNoticeSessionID(jobID) + "-file"
}

// session is the activity session the notice is recorded under.
func (n JobNotice) session() string {
	if n.Session != "" {
		return n.Session
	}
	return JobNoticeSessionID(n.JobID)
}

// Payload is the notice's agent_messages message payload.
func (n JobNotice) Payload() map[string]any {
	p := make(map[string]any, len(n.Extra)+9)
	for k, v := range n.Extra {
		p[k] = v
	}
	id := n.MessageID
	if id == "" {
		id = n.Kind + "-" + n.session()
	}
	p["messageId"] = id
	p["role"] = "assistant"
	p["notice"] = n.Kind
	p["text"] = n.Text
	p["ownerUserId"] = n.OwnerUserID
	p["provider"] = n.Provider
	if n.FailReason != "" {
		p["failReason"] = n.FailReason
	}
	if strings.TrimSpace(n.Detail) != "" {
		p["detail"] = n.Detail
	}
	if strings.TrimSpace(n.JobID) != "" {
		p["jobId"] = n.JobID
		if n.SourceSeq > 0 {
			p["sourceSeq"] = n.SourceSeq
		}
	}
	return p
}

// Report is the agent_messages batch that records the notice
// (POST …/agent-activity/messages, or activity's own append).
func (n JobNotice) Report() ReportMessagesInput {
	seq := n.Seq
	if seq == 0 {
		seq = JobNoticeSeq
	}
	return ReportMessagesInput{
		WorkspaceID:    n.RoomID,
		AgentSessionID: n.session(),
		ChannelID:      n.ChannelID,
		Messages:       []Message{{Seq: seq, Payload: n.Payload(), CreatedAtUnixMs: n.AtUnixMs}},
	}
}

// IsJobNoticeSession reports an activity session in a job's notice namespace
// (JobNoticeSessionID, and the sessions derived from it): its rows are
// notices, never an agent's replies.
func IsJobNoticeSession(sessionID string) bool {
	return strings.HasPrefix(strings.TrimSpace(sessionID), JobNoticeSessionID(""))
}
