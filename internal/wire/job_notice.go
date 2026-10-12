package wire

import "strings"

// JobNotice is one notice row about a job in the room feed: a line that says
// why nothing (or nothing more) came back — a refused or failed drive, a
// team member that timed out, a lead's progress. It is an agent_messages row
// whose payload carries `notice` (its kind): clients render it as a notice,
// and every reader that feeds the room back to an agent skips it (an agent
// must never read a notice as something it or another agent said).
type JobNotice struct {
	// Kind is payload.notice ("borrow-denied", "turn-failed", "progress", …).
	Kind string
	// MessageID is the row's stable message id (idempotent on retry).
	MessageID string
	// JobID and SourceSeq (the room sequence of the message that started
	// the job) let the feed nest the notice under its request.
	JobID     string
	SourceSeq int64
	Text      string
	// OwnerUserID / Provider attribute the notice to the agent it is about.
	OwnerUserID string
	Provider    string
	// FailReason is the job failure reason the notice reports ("" = none).
	FailReason string
	// Extra holds kind-specific fields (e.g. denyReason).
	Extra map[string]any
}

// Payload is the notice's agent_messages message payload.
func (n JobNotice) Payload() map[string]any {
	p := make(map[string]any, len(n.Extra)+9)
	for k, v := range n.Extra {
		p[k] = v
	}
	p["messageId"] = n.MessageID
	p["role"] = "assistant"
	p["notice"] = n.Kind
	p["text"] = n.Text
	p["ownerUserId"] = n.OwnerUserID
	p["provider"] = n.Provider
	if n.FailReason != "" {
		p["failReason"] = n.FailReason
	}
	if strings.TrimSpace(n.JobID) != "" {
		p["jobId"] = n.JobID
		if n.SourceSeq > 0 {
			p["sourceSeq"] = n.SourceSeq
		}
	}
	return p
}

// IsJobNoticeSession reports an activity session in a job's notice namespace
// (JobNoticeSessionID, and the sessions derived from it): its rows are
// notices, never an agent's replies.
func IsJobNoticeSession(sessionID string) bool {
	return strings.HasPrefix(strings.TrimSpace(sessionID), JobNoticeSessionID(""))
}
