package notify

import (
	"context"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Pairing an agent's reply with the job it completed (G3). The bridge posts
// the reply (agent_messages, under the agent's activity session) and then
// reports the job replied, its diagnostics naming that session; both reach
// the observer in publish order. The observer remembers each session's
// latest reply and consumes it when the job's replied event arrives. When the
// reply has not arrived yet (a reply posted after the status report), the
// job is parked until the reply shows up or replyWait lapses, then
// agent_finished is emitted without a summary. Nothing here ever delays the
// publish path: all of it runs on the observer's own goroutine.
const (
	// replyWait bounds how long a replied job waits for its reply.
	replyWait = 20 * time.Second
	// replyTTL bounds how long an unclaimed reply is remembered.
	replyTTL = 10 * time.Minute
	// replyClockSlack tolerates clock skew between the service that stamps a
	// reply (realtime) and the one that stamps the job's start (activity).
	replyClockSlack = 5 * time.Second
	// maxTrackedReplies caps the remembered replies and parked jobs.
	maxTrackedReplies = 4096
)

// agentReply is the latest reply an agent session posted in a room.
type agentReply struct {
	text      string
	seq       int64
	channelID string
	atMs      int64
	seenAt    time.Time
}

// pendingJob is a replied job waiting for its reply.
type pendingJob struct {
	obs      observation
	deadline time.Time
}

func replyKey(tenantID, roomID, session string) string {
	return tenantID + "\x00" + roomID + "\x00" + session
}

// decodeReply extracts an agent_messages batch's activity session and the
// text of its last non-empty message. Job notices (refused borrow, failure
// notes) are synthetic rows under a job's notice session, never a reply.
func decodeReply(payload any) (session, text string, ok bool) {
	in, ok := wire.DecodePayload[wire.AgentMessagesPayload](payload)
	if !ok {
		return "", "", false
	}
	session = strings.TrimSpace(in.AgentSessionID)
	if session == "" || wire.IsJobNoticeSession(session) {
		return "", "", false
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		m := in.Messages[i].Payload
		if (m.Role == "" || m.Role == "assistant") && strings.TrimSpace(m.Text) != "" {
			return session, m.Text, true
		}
	}
	return "", "", false
}

// replyObserved records a session's reply, or completes the job parked on it.
func (o *Observer) replyObserved(obs observation, now time.Time) []Event {
	key := replyKey(obs.tenantID, obs.roomID, obs.session)
	r := agentReply{text: obs.replyText, seq: obs.seq, channelID: obs.channelID, atMs: obs.atMs, seenAt: now}
	if p, ok := o.pending[key]; ok {
		delete(o.pending, key)
		return jobEvents(p.obs, r)
	}
	if o.replies == nil {
		o.replies = map[string]agentReply{}
	}
	if len(o.replies) >= maxTrackedReplies {
		o.pruneReplies(now)
	}
	o.replies[key] = r
	return nil
}

// jobReplied emits agent_finished with the job's reply, or parks the job
// until the reply arrives.
func (o *Observer) jobReplied(obs observation, now time.Time) []Event {
	session := strings.TrimSpace(obs.job.ReplySessionID)
	if session == "" {
		return jobEvents(obs, agentReply{})
	}
	key := replyKey(obs.tenantID, obs.roomID, session)
	if r, ok := o.replies[key]; ok {
		delete(o.replies, key)
		if r.atMs == 0 || r.atMs >= jobSinceMs(obs.job)-replyClockSlack.Milliseconds() {
			return jobEvents(obs, r)
		}
		// An older turn's unclaimed reply: not this job's answer.
	}
	if o.pending == nil {
		o.pending = map[string]pendingJob{}
	}
	if prev, ok := o.pending[key]; ok {
		// A second job on the same session finished before the first one's
		// reply arrived: the first goes out without a summary.
		o.emitLater(prev.obs)
	}
	if len(o.pending) >= maxTrackedReplies {
		return jobEvents(obs, agentReply{})
	}
	o.pending[key] = pendingJob{obs: obs, deadline: now.Add(replyWait)}
	return nil
}

// emitLater queues an already-decided job event for the next flush.
func (o *Observer) emitLater(obs observation) {
	o.overdue = append(o.overdue, obs)
}

// flushPending emits parked jobs whose wait lapsed (without a summary).
func (o *Observer) flushPending(ctx context.Context, now time.Time) {
	due := o.overdue
	o.overdue = nil
	for key, p := range o.pending {
		if !now.Before(p.deadline) {
			delete(o.pending, key)
			due = append(due, p.obs)
		}
	}
	for _, obs := range due {
		o.emit(ctx, obs.tenantID, jobEvents(obs, agentReply{}))
	}
	if len(o.replies) > 0 {
		o.pruneReplies(now)
	}
}

// pruneReplies drops remembered replies older than replyTTL, and the oldest
// half when the table is still full.
func (o *Observer) pruneReplies(now time.Time) {
	for k, r := range o.replies {
		if now.Sub(r.seenAt) > replyTTL {
			delete(o.replies, k)
		}
	}
	if len(o.replies) < maxTrackedReplies {
		return
	}
	cut := now.Add(-replyTTL / 2)
	for k, r := range o.replies {
		if r.seenAt.Before(cut) || len(o.replies) >= maxTrackedReplies {
			delete(o.replies, k)
		}
	}
}

// jobSinceMs is when the job's turn began (claim time when the bridge never
// reported running).
func jobSinceMs(j wire.AgentJob) int64 {
	if j.StartedAtMs > 0 {
		return j.StartedAtMs
	}
	return j.CreatedAtMs
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
