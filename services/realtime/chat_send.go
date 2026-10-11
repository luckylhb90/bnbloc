package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The one chat publish path. A message typed in a client (POST .../chat) and
// a message posted on a member's behalf by the server (a scheduled prompt)
// both go through PostChat, so persistence, clientMsgId idempotency, channel
// routing, agent-job creation (the bridge claims by recipient), plugin hooks
// and notifications behave identically for both.

var (
	errEmptyMessage   = errors.New("empty message")
	errMessageTooLong = errors.New("message too long")
)

// errBadAttachment wraps an attachment reference that does not resolve in
// the room (400 to a client).
type errBadAttachment struct{ err error }

func (e errBadAttachment) Error() string { return e.err.Error() }
func (e errBadAttachment) Unwrap() error { return e.err }

// ChatAgentOptions is the sender's model + reasoning selection, forwarded
// end-to-end to the agent process. Empty fields keep the core/CLI default.
type ChatAgentOptions struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ChatRecipient routes a message to one member's agent (e.g.
// {ownerId:"jack", provider:"codex"}); nil/empty provider is people-only (no
// agent is driven). Core selects which agent core drives it when the owner
// exposes more than one; empty means the driver's default core.
type ChatRecipient struct {
	OwnerID      string            `json:"ownerId"`
	Provider     string            `json:"provider"`
	Core         string            `json:"core,omitempty"`
	AgentOptions *ChatAgentOptions `json:"agentOptions,omitempty"`
}

// ChatPost is one chat message posted as Author. The caller has already
// authorized Author as a member of RoomID.
type ChatPost struct {
	RoomID string
	Author string
	Text   string
	// ClientMsgID, when set, makes the post idempotent: a retry with the same
	// id returns the original sequence instead of posting a duplicate.
	ClientMsgID string
	// ChannelID routes the message to one channel; empty means the room's
	// default channel.
	ChannelID     string
	AtUnixMs      int64
	Recipient     *ChatRecipient
	AttachmentIDs []string
	// Scheduled marks a message the scheduler posted (payload.scheduled), so
	// clients can badge it. Never set for a typed message.
	Scheduled *wire.ScheduledMarker
}

// PostChat validates, persists, and publishes a chat message, then creates
// the agent job for an agent recipient. It returns the message's sequence
// (the original one for a clientMsgId already seen). Errors: errEmptyMessage,
// errMessageTooLong, errBadAttachment, errUnknownChannel,
// errAgentInPeopleChat, errPlatformAgentOffline, or a persistence failure.
func (s *Service) PostChat(ctx context.Context, p ChatPost) (int64, error) {
	if len(p.Text) > maxChatTextBytes {
		return 0, errMessageTooLong
	}
	text := strings.TrimSpace(p.Text)
	attachments, err := s.validateAttachmentIDs(ctx, p.RoomID, p.AttachmentIDs)
	if err != nil {
		return 0, errBadAttachment{err}
	}
	if text == "" && len(attachments) == 0 {
		return 0, errEmptyMessage
	}
	// Idempotency: a retry carrying a client id we already stored returns the
	// original sequence without re-publishing (no duplicate message).
	if p.ClientMsgID != "" && s.Chat != nil {
		if seq, found := s.Chat.LookupClientMsg(ctx, p.RoomID, p.ClientMsgID); found {
			return seq, nil
		}
	}
	channelID, err := s.resolveChannel(ctx, p.RoomID, p.ChannelID)
	if err != nil {
		return 0, err
	}
	agent := p.Recipient != nil && p.Recipient.Provider != ""
	if agent {
		// The people chat is human-only: no agent is ever driven from it.
		people, err := s.isPeopleChannel(ctx, p.RoomID, channelID)
		if err != nil {
			return 0, fmt.Errorf("channel lookup failed: %w", err)
		}
		if people {
			return 0, errAgentInPeopleChat
		}
		// A platform agent whose host is offline is refused before anything
		// is published or queued.
		if err := s.checkPlatformAgentOnline(ctx, p.RoomID, p.Author, p.Recipient); err != nil {
			return 0, err
		}
	}
	payload := map[string]any{"userId": p.Author, "text": text}
	if p.ClientMsgID != "" {
		// Echo the sender's idempotency id so a client whose send response was
		// lost reconciles its optimistic row by id instead of rendering the
		// live echo as a duplicate until reload.
		payload["clientMsgId"] = p.ClientMsgID
	}
	if len(attachments) > 0 {
		payload["attachments"] = attachments
	}
	if p.Scheduled != nil {
		payload["scheduled"] = map[string]any{"scheduleId": p.Scheduled.ScheduleID}
	}
	if agent {
		rp := map[string]any{
			"ownerId":  p.Recipient.OwnerID,
			"provider": p.Recipient.Provider,
		}
		if p.Recipient.Core != "" {
			rp["core"] = p.Recipient.Core
		}
		if ao := p.Recipient.AgentOptions; ao != nil && (ao.Model != "" || ao.ReasoningEffort != "") {
			opts := map[string]any{}
			if ao.Model != "" {
				opts["model"] = ao.Model
			}
			if ao.ReasoningEffort != "" {
				opts["reasoningEffort"] = ao.ReasoningEffort
			}
			rp["agentOptions"] = opts
		}
		payload["recipient"] = rp
	}
	// Persist before fan-out: a message is delivered live only once it is
	// durable, so a client retry after a failure can never surface a second
	// live copy, and history never lacks a message members already saw.
	ev := wire.Event{
		RoomID:    p.RoomID,
		ChannelID: channelID,
		Kind:      "chat_message",
		Payload:   payload,
		AtUnixMs:  serverTimeIfUnset(p.AtUnixMs),
	}
	d, err := s.durable(ctx, ev, p.Author, p.ClientMsgID)
	if err != nil {
		return 0, fmt.Errorf("message persistence failed: %w", err)
	}
	ev, err = s.Hub.PublishCommit(ev, d)
	if errors.Is(err, store.ErrDuplicateClientMsg) {
		// A concurrent send with the same client id won the race (in the same
		// commit batch, or already in history): the hub answers its sequence,
		// exactly like the idempotent retry above.
		return ev.Sequence, nil
	}
	if err != nil {
		log.Printf("realtime: persist room=%s kind=%s: %v", p.RoomID, ev.Kind, err)
		return 0, fmt.Errorf("message persistence failed: %w", err)
	}
	s.deliverPluginEvent(ctx, ev)
	if agent && s.BridgeJobs != nil {
		coreName := strings.TrimSpace(p.Recipient.Core)
		var model, reasoningEffort string
		if ao := p.Recipient.AgentOptions; ao != nil {
			model = ao.Model
			reasoningEffort = ao.ReasoningEffort
		}
		// Queue timestamps must be server time. ev.AtUnixMs is client-supplied and
		// may be zero or skewed; letting it into the queue would make lease and
		// timeout decisions fire against a bogus clock.
		nowMs := time.Now().UnixMilli()
		job, err := s.BridgeJobs.PutAgentJob(ctx, store.AgentJob{
			JobID:           ids.New("job_"),
			RoomID:          p.RoomID,
			ChannelID:       channelID,
			MessageSeq:      ev.Sequence,
			ClientMsgID:     p.ClientMsgID,
			RequesterUserID: p.Author,
			OwnerUserID:     p.Recipient.OwnerID,
			Core:            coreName,
			Provider:        p.Recipient.Provider,
			TargetID:        strings.Trim(strings.Join([]string{coreName, p.Recipient.Provider}, "/"), "/"),
			MessageText:     text,
			AttachmentIDs:   p.AttachmentIDs,
			Model:           model,
			ReasoningEffort: reasoningEffort,
			Status:          store.AgentJobQueued,
			CreatedAtMs:     nowMs,
			UpdatedAtMs:     nowMs,
		})
		if err == nil {
			s.Hub.Publish(wire.Event{RoomID: p.RoomID, ChannelID: channelID, Kind: "agent_job_created", Payload: job, AtUnixMs: nowMs})
		}
	}
	return ev.Sequence, nil
}
