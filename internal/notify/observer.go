package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// DefaultQueueSize bounds the observation queue between the publish tap and
// the notification producer. A full queue drops (and counts) observations:
// notifications are best-effort, the chat path never waits on them.
const DefaultQueueSize = 512

// Observer is the realtime publish tap. Observe extracts what it needs from a
// published event and hands it to a bounded queue without blocking; Run turns
// queued observations into Events (mention detection needs membership
// lookups, so it never runs on the publisher's goroutine).
type Observer struct {
	Emitter    *Emitter
	Membership membership.Reader

	queue   chan observation
	dropped atomic.Int64

	// replies and pending pair an agent's reply with the job it completed
	// (G3). Both are touched only on the Run goroutine (process/flush).
	replies map[string]agentReply
	pending map[string]pendingJob
	overdue []observation
}

// observation is the immutable extract of one published event.
type observation struct {
	tenantID  string
	kind      string
	roomID    string
	channelID string
	seq       int64
	atMs      int64
	// chat_message
	author string
	text   string
	// agent_job_updated
	job jobView
	// agent_messages: the reply's activity session and the text of its last
	// non-empty message.
	session   string
	replyText string
}

// jobView is the subset of a store.AgentJob the producer reads. The payload
// arrives typed in-process and as decoded JSON through the internal ingest;
// both are normalized through the job's JSON shape.
type jobView struct {
	JobID           string `json:"jobId"`
	RequesterUserID string `json:"requesterUserId"`
	OwnerUserID     string `json:"ownerUserId"`
	Provider        string `json:"provider"`
	MessageText     string `json:"messageText"`
	Status          string `json:"status"`
	FailureReason   string `json:"failureReason"`
	ChannelID       string `json:"channelId"`
	Core            string `json:"core"`
	MessageSeq      int64  `json:"messageSeq"`
	CreatedAtMs     int64  `json:"createdAtMs"`
	StartedAtMs     int64  `json:"startedAtMs"`
	// Diagnostics carries the bridge's report; on replied it names the
	// activity session the reply was posted under.
	Diagnostics struct {
		SessionID string `json:"sessionId"`
	} `json:"diagnostics"`
}

// NewObserver builds an observer with a bounded queue.
func NewObserver(e *Emitter, m membership.Reader, size int) *Observer {
	if size <= 0 {
		size = DefaultQueueSize
	}
	return &Observer{Emitter: e, Membership: m, queue: make(chan observation, size)}
}

// Dropped returns how many observations a full queue discarded.
func (o *Observer) Dropped() int64 { return o.dropped.Load() }

// Observe inspects a just-published room event. It never blocks: irrelevant
// kinds return at once, relevant ones are queued or dropped.
func (o *Observer) Observe(ctx context.Context, ev wire.Event) {
	if o == nil {
		return
	}
	obs := observation{tenantID: tenant.FromContext(ctx), kind: ev.Kind, roomID: ev.RoomID, channelID: ev.ChannelID, seq: ev.Sequence, atMs: ev.AtUnixMs}
	switch ev.Kind {
	case "chat_message":
		p, ok := ev.Payload.(map[string]any)
		if !ok {
			return
		}
		obs.author, _ = p["userId"].(string)
		obs.text, _ = p["text"].(string)
		if !strings.Contains(obs.text, "@") {
			return
		}
	case "agent_job_updated":
		job, ok := decodeJob(ev.Payload)
		if !ok {
			return
		}
		switch store.AgentJobStatus(job.Status) {
		case store.AgentJobReplied, store.AgentJobFailed, store.AgentJobTimedOut:
		default:
			return
		}
		obs.job = job
	case "agent_messages":
		session, text, ok := decodeReply(ev.Payload)
		if !ok {
			return
		}
		obs.session, obs.replyText = session, text
	default:
		return
	}
	if obs.tenantID == "" || obs.roomID == "" {
		return
	}
	select {
	case o.queue <- obs:
	default:
		if n := o.dropped.Add(1); n == 1 || n%100 == 0 {
			slog.Warn("bnbloc notifications: observation queue full; dropping",
				"event", "bnbloc.notify.queue_full", "dropped", n)
		}
	}
}

func decodeJob(payload any) (jobView, bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return jobView{}, false
	}
	var j jobView
	if err := json.Unmarshal(raw, &j); err != nil || j.JobID == "" {
		return jobView{}, false
	}
	return j, true
}

// Run processes queued observations until ctx ends, and emits parked
// agent_finished events whose reply never arrived once their wait lapses.
func (o *Observer) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case obs := <-o.queue:
			o.process(ctx, obs)
		case <-tick.C:
			o.flushPending(ctx, time.Now())
		}
	}
}

// process turns one observation into zero or more Events. Each runs under a
// bounded timeout in the event's tenant.
func (o *Observer) process(parent context.Context, obs observation) {
	o.emit(parent, obs.tenantID, o.events(obs))
}

// emit enqueues events of one tenant under a bounded timeout.
func (o *Observer) emit(parent context.Context, tenantID string, events []Event) {
	if len(events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(tenant.NewContext(parent, tenantID), 10*time.Second)
	defer cancel()
	for _, ev := range events {
		// A borrow request goes to the agent's owner only while they are one
		// of the room's people: a platform agent's host (an agent-host
		// membership) is not asked to authorize anything.
		if ev.Type == wire.NotificationBorrowRequest &&
			(len(ev.Recipients) != 1 || !membership.HumanMember(o.Membership, ev.RoomID, ev.Recipients[0])) {
			continue
		}
		if _, err := o.Emitter.Emit(ctx, ev); err != nil {
			slog.Warn("bnbloc notifications: enqueue failed",
				"event", "bnbloc.notify.enqueue_failed", "type", ev.Type, "room", ev.RoomID, "error", err)
		}
	}
}

// events maps an observation to notification Events.
func (o *Observer) events(obs observation) []Event {
	switch obs.kind {
	case "chat_message":
		return o.mentionEvents(obs)
	case "agent_messages":
		return o.replyObserved(obs, time.Now())
	case "agent_job_updated":
		if store.AgentJobStatus(obs.job.Status) == store.AgentJobReplied {
			return o.jobReplied(obs, time.Now())
		}
		return jobEvents(obs, agentReply{})
	}
	return nil
}

// mentionEvents notifies the room members a human message @-mentions. A
// candidate counts only when it names a member of the room; the author is
// never notified about their own message.
func (o *Observer) mentionEvents(obs observation) []Event {
	if o.Membership == nil || obs.author == "" {
		return nil
	}
	var recipients []string
	for _, c := range MentionCandidates(obs.text) {
		if c == obs.author {
			continue
		}
		if membership.HumanMember(o.Membership, obs.roomID, c) {
			recipients = append(recipients, c)
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	return []Event{{
		Type:       wire.NotificationMention,
		RoomID:     obs.roomID,
		Recipients: recipients,
		Actor:      obs.author,
		Title:      obs.author + " mentioned you",
		Text:       obs.text,
		DedupKey:   "msg:" + obs.roomID + ":" + strconv.FormatInt(obs.seq, 10),
		Seq:        obs.seq,
		ChannelID:  obs.channelID,
		Reply:      roomReplyTo(obs.channelID),
	}}
}

// jobEvents notifies the member who addressed an agent when the job ends, and
// — when the job failed because the owner has not authorized borrowing — the
// agent's owner, which is how a borrower's attempt reaches them as a request.
// reply is the agent's reply that completed a replied job (zero when it was
// not seen): agent_finished then carries its summary and links to it;
// otherwise the link points at the request message.
func jobEvents(obs observation, reply agentReply) []Event {
	j := obs.job
	if j.RequesterUserID == "" {
		return nil
	}
	agent := j.Provider
	if agent == "" {
		agent = "agent"
	}
	actor := agent
	if j.OwnerUserID != "" {
		actor = j.OwnerUserID + "/" + agent
	}
	request := "Request: " + j.MessageText
	channel := firstNonEmptyString(j.ChannelID, obs.channelID)
	switch store.AgentJobStatus(j.Status) {
	case store.AgentJobReplied:
		text, seq := request, j.MessageSeq
		if summary := ReplySummary(reply.text, ReplySummaryRunes); summary != "" {
			text = summary + "\n\n" + request
		}
		if reply.seq > 0 {
			seq, channel = reply.seq, firstNonEmptyString(reply.channelID, channel)
		}
		return []Event{{
			Type: wire.NotificationAgentFinished, RoomID: obs.roomID, Recipients: []string{j.RequesterUserID},
			Actor: actor, Title: actor + " finished your request", Text: text, DedupKey: "job:" + j.JobID,
			Seq: seq, ChannelID: channel, Reply: agentReplyTo(channel, j),
		}}
	case store.AgentJobFailed, store.AgentJobTimedOut:
		if store.AgentJobFailureReason(j.FailureReason) == store.FailureCancelled {
			return nil // a cancel is a member's own action, not news
		}
		out := []Event{{
			Type: wire.NotificationAgentFailed, RoomID: obs.roomID, Recipients: []string{j.RequesterUserID},
			Actor: actor, Title: actor + " could not complete your request", Text: failureText(j.FailureReason) + " " + request,
			DedupKey: "job:" + j.JobID, Seq: j.MessageSeq, ChannelID: channel, Reply: agentReplyTo(channel, j),
		}}
		if store.AgentJobFailureReason(j.FailureReason) == store.FailureBorrowNotAuthorized &&
			j.OwnerUserID != "" && j.OwnerUserID != j.RequesterUserID {
			out = append(out, Event{
				Type: wire.NotificationBorrowRequest, RoomID: obs.roomID, Recipients: []string{j.OwnerUserID},
				Actor: j.RequesterUserID, Title: j.RequesterUserID + " asked to use your " + agent + " agent",
				Text:     "Authorize borrowing in the room to let them run it. " + request,
				DedupKey: "borrow:" + j.JobID, Seq: j.MessageSeq, ChannelID: channel,
			})
		}
		return out
	}
	return nil
}

func failureText(reason string) string {
	switch store.AgentJobFailureReason(reason) {
	case store.FailureNoBridgeOnline, store.FailureAgentNotAdvertised:
		return "The agent is offline."
	case store.FailureBorrowNotAuthorized:
		return "The owner has not authorized borrowing this agent."
	case store.FailureTurnTimeout, store.FailureReplyTimeout:
		return "The agent stopped responding."
	case "":
		return "The agent failed."
	}
	return "The agent failed (" + reason + ")."
}
