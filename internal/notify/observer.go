package notify

import (
	"context"
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
	// MayRead says whether a member may read a channel of a room
	// (store.MayReadChannel, which realtime owns): a mention notifies only
	// a reader of its channel (C11: a members-only section's message never
	// reaches anyone else through a notification). Nil reads every channel
	// (a deployment without channels). It is called on the Run goroutine,
	// in the event's tenant.
	MayRead func(ctx context.Context, roomID, channelID, userID string) bool
	// Listeners lists who listens to a channel (realtime owns it): the
	// people who may read it, each with their effective level
	// (wire.EffectiveNotifyLevel) and whether they are here. It lists the
	// channel's followers for ReachFollowers, adds the members who read it in
	// the last hour or are in the room for ReachHere, and every member who
	// may read it for ReachChannel. Nil: only direct mentions notify.
	Listeners func(ctx context.Context, roomID, channelID, reach string) ([]Listener, error)

	queue   chan observation
	dropped atomic.Int64

	// replies and pending pair an agent's reply with the job it completed
	// (G3). Both are touched only on the Run goroutine (process/flush).
	replies map[string]agentReply
	pending map[string]pendingJob
	overdue []observation
}

// Listener is one person listening to a channel (Observer.Listeners).
type Listener struct {
	UserID string
	// Level is the effective level: all, mentions or muted.
	Level string
	// Here: follows the channel, read it in the last hour, or is in the room.
	Here bool
}

// The reach of a Listeners call.
const (
	ReachFollowers = "followers"
	ReachHere      = "here"
	ReachChannel   = "channel"
)

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
	job wire.AgentJob
	// agent_messages: the reply's activity session and the text of its last
	// non-empty message.
	session   string
	replyText string
}

// NewObserver builds an observer with a bounded queue.
func NewObserver(e *Emitter, m membership.Reader, size int) *Observer {
	if size <= 0 {
		size = DefaultQueueSize
	}
	return &Observer{Emitter: e, Membership: m, queue: make(chan observation, size)}
}

// Drain waits until the queued observations are processed (by Run) or ctx
// ends: a stopping replica turns what it observed into notifications before
// it exits.
func (o *Observer) Drain(ctx context.Context) {
	if o == nil {
		return
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for len(o.queue) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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
	case wire.KindChatMessage:
		p, ok := wire.DecodePayload[wire.ChatMessagePayload](ev.Payload)
		if !ok {
			return
		}
		obs.author, obs.text = p.UserID, p.Text
		// Without listeners only a mention notifies; with them a channel at
		// level "all" hears every message.
		if o.Listeners == nil && !strings.Contains(obs.text, "@") {
			return
		}
	case wire.EventAgentJobUpdated:
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
	case wire.KindAgentMessages:
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

// decodeJob reads a job event's payload (wire.AgentJob, the one job
// projection). It arrives typed in-process and as decoded JSON through the
// internal ingest; both are normalized through its JSON.
func decodeJob(payload any) (wire.AgentJob, bool) {
	j, ok := wire.DecodePayload[wire.AgentJob](payload)
	return j, ok && j.JobID != ""
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
	case wire.KindChatMessage:
		return o.messageEvents(obs)
	case wire.KindAgentMessages:
		return o.replyObserved(obs, time.Now())
	case wire.EventAgentJobUpdated:
		if store.AgentJobStatus(obs.job.Status) == store.AgentJobReplied {
			return o.jobReplied(obs, time.Now())
		}
		return jobEvents(obs, agentReply{})
	}
	return nil
}

// mayRead asks MayRead (nil: every channel) in the observation's tenant.
func (o *Observer) mayRead(obs observation, userID string) bool {
	if o.MayRead == nil || obs.channelID == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), obs.tenantID), 5*time.Second)
	defer cancel()
	return o.MayRead(ctx, obs.roomID, obs.channelID, userID)
}

// messageEvents notifies about a person's message (C4, C5): the room
// members it @-mentions (in a channel they may read, whatever its level; a
// muted room is the emitter's), the listeners an @channel or @here reaches
// (never at level muted), and the listeners at level "all". Each person is
// told once, by the first that applies; the author never.
func (o *Observer) messageEvents(obs observation) []Event {
	if o.Membership == nil || obs.author == "" {
		return nil
	}
	told := map[string]bool{obs.author: true}
	var direct []string
	broadcast := ""
	for _, c := range MentionCandidates(obs.text) {
		if b := broadcastReach(c); b != "" {
			if broadcast != ReachChannel {
				broadcast = b
			}
			continue
		}
		if told[c] {
			continue
		}
		if membership.HumanMember(o.Membership, obs.roomID, c) && o.mayRead(obs, c) {
			told[c] = true
			direct = append(direct, c)
		}
	}
	key := "msg:" + obs.roomID + ":" + strconv.FormatInt(obs.seq, 10)
	event := func(typ string, recipients []string, title, dedup string) Event {
		return Event{Type: typ, RoomID: obs.roomID, Recipients: recipients, Actor: obs.author, Title: title,
			Text: obs.text, DedupKey: dedup, Seq: obs.seq, ChannelID: obs.channelID, Reply: roomReplyTo(obs.channelID)}
	}
	var out []Event
	if len(direct) > 0 {
		out = append(out, event(wire.NotificationMention, direct, obs.author+" mentioned you", key))
	}
	if o.Listeners == nil || obs.channelID == "" {
		return out
	}
	reach := ReachFollowers
	if broadcast != "" {
		reach = broadcast
	}
	listeners := o.listeners(obs, reach)
	var reached, all []string
	for _, l := range listeners {
		if told[l.UserID] || l.Level == wire.NotifyLevelMuted {
			continue
		}
		switch {
		case broadcast == ReachChannel, broadcast == ReachHere && l.Here:
			reached = append(reached, l.UserID)
		case l.Level == wire.NotifyLevelAll:
			all = append(all, l.UserID)
		default:
			continue
		}
		told[l.UserID] = true
	}
	if len(reached) > 0 {
		ev := event(wire.NotificationMention, reached, obs.author+" mentioned @"+broadcast, key+":"+broadcast)
		ev.Broadcast = true
		out = append(out, ev)
	}
	if len(all) > 0 {
		out = append(out, event(wire.NotificationChannelMessage, all, obs.author+" posted", key+":all"))
	}
	return out
}

// broadcastReach maps an @-token to the reach it asks for ("" for a
// member's id: "channel" and "here" are no member ids).
func broadcastReach(tok string) string {
	switch tok {
	case "channel":
		return ReachChannel
	case "here":
		return ReachHere
	}
	return ""
}

// listeners asks Listeners in the observation's tenant; a failure notifies
// nobody beyond the direct mentions (fail closed).
func (o *Observer) listeners(obs observation, reach string) []Listener {
	ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), obs.tenantID), 5*time.Second)
	defer cancel()
	ls, err := o.Listeners(ctx, obs.roomID, obs.channelID, reach)
	if err != nil {
		slog.Warn("bnbloc notifications: listeners lookup failed",
			"event", "bnbloc.notify.listeners_failed", "room", obs.roomID, "channel", obs.channelID, "error", err)
		return nil
	}
	return ls
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
			Actor: actor, Title: actor + " could not complete your request", Text: store.AgentJobFailureReason(j.FailureReason).Text(j.Provider) + " " + request,
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
