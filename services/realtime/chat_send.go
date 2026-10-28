package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
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
	// errRecipientWithoutOwner refuses an agent recipient that names no
	// member (400). An agent is always one member's (a platform agent's
	// too: the client routes it to a host, the recipient's owner), and
	// the bridge claims by owner, so an ownerless job is one no bridge
	// will ever take: it would fail minutes later as no_bridge_online.
	errRecipientWithoutOwner = errors.New("recipient.ownerId is required: name the member whose agent should answer")
)

// errBadAttachment wraps an attachment reference that does not resolve in
// the room (400 to a client).
type errBadAttachment struct{ err error }

func (e errBadAttachment) Error() string { return e.err.Error() }
func (e errBadAttachment) Unwrap() error { return e.err }

// errBadFileRef refuses a message naming a file it may not (not the room's,
// gone, a version it does not have).
type errBadFileRef struct{ msg string }

func (e errBadFileRef) Error() string { return e.msg }

// ChatAgentOptions is the sender's model + reasoning selection, forwarded
// end-to-end to the agent process. Empty fields keep the core/CLI default.
type ChatAgentOptions struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ChatRecipient routes a message to one member's agent (e.g.
// {ownerId:"jack", provider:"codex"}); nil/empty provider is people-only (no
// agent is driven), and an agent recipient always names its owner
// (errRecipientWithoutOwner). Core selects which agent core drives it when the owner
// exposes more than one; empty means the driver's default core.
type ChatRecipient struct {
	OwnerID      string            `json:"ownerId"`
	Provider     string            `json:"provider"`
	Core         string            `json:"core,omitempty"`
	AgentOptions *ChatAgentOptions `json:"agentOptions,omitempty"`
}

// ChatPost is one chat message posted as Author (PostChat checks Author may
// chat in RoomID).
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
	// Dispatched marks a message an agent dispatched through the room tools
	// (payload.dispatchedBy); the agent job it creates records the parent
	// job and depth. Never set for a typed message.
	Dispatched *wire.DispatchMarker
	// Continued marks a message the platform posted as the member because a
	// card decision of theirs lets an agent's work go on (payload.continuedFrom).
	Continued *wire.ContinueMarker
	// Expert asks the agent recipient to wear an expert (a content
	// component the author may use here); the job pins its version.
	Expert *wire.ChatExpert
	// Draft asks the agent recipient to wear a staged content draft (a
	// quick try, chat_expert.go): only a dispatch sets it, for a draft its
	// parent job staged. Never set with Expert.
	Draft string
	// FileRef names the room file the message is about (version 0 = the
	// current one); validated and completed (version, name) on post.
	FileRef *wire.FileRef
}

// ChatPosted is what PostChat did: the message's sequence and, for an agent
// recipient, the job it queued (nil for a people-only message, an
// idempotent retry of an earlier post, or a job write that failed).
type ChatPosted struct {
	Sequence int64
	Job      *store.AgentJob
}

// PostChat validates, persists, and publishes a chat message, then creates
// the agent job for an agent recipient. It returns the message's sequence
// (the original one for a clientMsgId already seen) and the queued job.
// Errors: a membership refusal (membership.IsRefusal), errEmptyMessage,
// errMessageTooLong, errBadAttachment, errUnknownChannel,
// errRecipientWithoutOwner, errAgentsNotAdmitted, errPlatformAgentOffline,
// a postRefusal of the channel gate (channel_gate.go), or a persistence
// failure.
//
// The author must be a member who may chat (membership.Member.MayChat), in
// the tenant ctx is signed for, and the channel's setup must admit the post
// (admitPost): both checked here, once, so no caller (a typed message, a
// scheduled or dispatched post) can skip them.
func (s *Service) PostChat(ctx context.Context, p ChatPost) (ChatPosted, error) {
	author, err := membership.RoomScope(ctx, s.Membership, p.RoomID, p.Author, membership.Chat)
	if err != nil {
		return ChatPosted{}, err
	}
	if len(p.Text) > maxChatTextBytes {
		return ChatPosted{}, errMessageTooLong
	}
	text := strings.TrimSpace(p.Text)
	attachments, err := s.validateAttachmentIDs(ctx, p.RoomID, p.AttachmentIDs)
	if err != nil {
		return ChatPosted{}, errBadAttachment{err}
	}
	if text == "" && len(attachments) == 0 {
		return ChatPosted{}, errEmptyMessage
	}
	fileRef, err := s.resolveFileRef(ctx, p.RoomID, p.FileRef, func(channelID string) bool {
		// A deliverable of a channel the author may not read is none of
		// theirs to name (C13).
		return s.memberMayRead(ctx, p.RoomID, p.Author, author, channelID)
	})
	if err != nil {
		return ChatPosted{}, err
	}
	// Idempotency: a retry carrying a client id we already stored returns the
	// original sequence without re-publishing (no duplicate message).
	if p.ClientMsgID != "" && s.Chat != nil {
		seq, found, err := s.Chat.LookupClientMsg(ctx, p.RoomID, p.ClientMsgID)
		if err != nil {
			return ChatPosted{}, err
		}
		if found {
			return ChatPosted{Sequence: seq}, nil
		}
	}
	channel, err := s.channelOf(ctx, p.RoomID, p.ChannelID)
	if err != nil {
		return ChatPosted{}, err
	}
	channelID := channel.ChannelID
	// Only a reader of the channel posts in it (store.MayReadChannel): a
	// members-only section's channel does not exist for anyone else (C11),
	// whatever posts (typed, scheduled as its creator, dispatched for its
	// requester).
	if !s.memberMayRead(ctx, p.RoomID, p.Author, author, channelID) {
		return ChatPosted{}, errUnknownChannel
	}
	if p.Recipient != nil {
		// The recipient owner is stored and compared exactly downstream
		// (job owner, bridge claim, borrow checks): subjects are
		// case-sensitive, so the only canonical form is the trimmed id.
		p.Recipient.OwnerID = strings.TrimSpace(p.Recipient.OwnerID)
	}
	agent := p.Recipient != nil && p.Recipient.Provider != ""
	if agent && p.Recipient.OwnerID == "" {
		return ChatPosted{}, errRecipientWithoutOwner
	}
	if agent {
		// No agent is ever driven from a channel agents are not admitted in
		// (the people chat, a channel whose settings bar them).
		barred, err := s.agentsBarred(ctx, p.RoomID, channelID)
		if err != nil {
			return ChatPosted{}, fmt.Errorf("channel lookup failed: %w", err)
		}
		if barred {
			return ChatPosted{}, errAgentsNotAdmitted
		}
		// A platform agent whose host is offline is refused before anything
		// is published or queued.
		if err := s.checkPlatformAgentOnline(ctx, p.RoomID, p.Author, p.Recipient); err != nil {
			return ChatPosted{}, err
		}
	}
	expert, err := s.resolveChatExpert(ctx, p, agent)
	if err != nil {
		return ChatPosted{}, err
	}
	// The channel's setup decides last, right before the post is stored: its
	// slow-mode claim records the post.
	if err := s.admitPost(ctx, p.RoomID, p.Author, author, channel); err != nil {
		return ChatPosted{}, err
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
	if fileRef != nil {
		payload["fileRef"] = *fileRef
	}
	if p.Scheduled != nil {
		payload["scheduled"] = map[string]any{"scheduleId": p.Scheduled.ScheduleID}
	}
	if p.Dispatched != nil {
		payload["dispatchedBy"] = *p.Dispatched
	}
	if p.Continued != nil {
		payload["continuedFrom"] = *p.Continued
	}
	if expert != nil {
		payload["expert"] = expert.payload()
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
		Kind:      wire.KindChatMessage,
		Payload:   payload,
		AtUnixMs:  serverTimeIfUnset(p.AtUnixMs),
	}
	d, err := s.durable(ctx, ev, p.Author, p.ClientMsgID)
	if err != nil {
		return ChatPosted{}, fmt.Errorf("message persistence failed: %w", err)
	}
	ev, err = s.publish(ctx, ev, d)
	if errors.Is(err, store.ErrDuplicateClientMsg) {
		// A concurrent send with the same client id won the race (in the same
		// commit batch, or already in history): the hub answers its sequence,
		// exactly like the idempotent retry above.
		return ChatPosted{Sequence: ev.Sequence}, nil
	}
	if err != nil {
		log.Printf("realtime: persist room=%s kind=%s: %v", p.RoomID, ev.Kind, err)
		return ChatPosted{}, fmt.Errorf("message persistence failed: %w", err)
	}
	posted := ChatPosted{Sequence: ev.Sequence}
	s.pulseMessageSent(ctx, p.RoomID, ev.Sequence, agent, p.Author)
	// decision: channels PRD C18 (being mentioned joins the channel).
	s.joinMentioned(ctx, p.RoomID, channelID, p.Author, text)
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
		var parentJobID string
		var depth int
		if p.Dispatched != nil {
			parentJobID, depth = p.Dispatched.JobID, p.Dispatched.Depth
		}
		aj := store.AgentJob{
			JobID:             ids.New("job_"),
			RoomID:            p.RoomID,
			ChannelID:         channelID,
			MessageSeq:        ev.Sequence,
			ClientMsgID:       p.ClientMsgID,
			RequesterUserID:   p.Author,
			OwnerUserID:       p.Recipient.OwnerID,
			Core:              coreName,
			Provider:          p.Recipient.Provider,
			TargetID:          agentTargetID(coreName, p.Recipient.Provider),
			MessageText:       text,
			AttachmentIDs:     p.AttachmentIDs,
			FileRef:           fileRef,
			Model:             model,
			ReasoningEffort:   reasoningEffort,
			Status:            store.AgentJobQueued,
			CreatedAtMs:       nowMs,
			UpdatedAtMs:       nowMs,
			DispatchedByJobID: parentJobID,
			DispatchDepth:     depth,
		}
		if expert != nil {
			aj.ExpertID, aj.ExpertVersion, aj.ExpertName = expert.ID, expert.Version, expert.label()
		}
		job, err := s.BridgeJobs.PutAgentJob(ctx, aj)
		if err != nil {
			log.Printf("realtime: agent job room=%s seq=%d: %v", p.RoomID, ev.Sequence, err)
		} else {
			posted.Job = &job
			s.pulseJobQueued(job)
			s.auditExpertUse(ctx, expert, job)
			_, _ = s.publish(ctx, wire.Event{RoomID: p.RoomID, ChannelID: channelID, Kind: wire.EventAgentJobCreated, Payload: job.View(), AtUnixMs: nowMs}, realtime.Durable{})
		}
	}
	return posted, nil
}

// agentTargetID is a job's target before the bridge resolves the core-local
// one: the provider, qualified by its core when the chat named one
// (core.QualifyTargetID; the bridge splits it with core.SplitTargetID).
func agentTargetID(coreName, provider string) string {
	if coreName == "" {
		return provider
	}
	return core.QualifyTargetID(coreName, provider)
}

// ArtifactReader is what realtime reads of a room's deliverables: search
// (team.go) and the file a message names (resolveFileRef).
type ArtifactReader interface {
	store.ArtifactIndex
	GetArtifact(ctx context.Context, artifactID string) (store.Artifact, error)
	GetArtifactVersion(ctx context.Context, artifactID string, version int) (store.ArtifactVersion, error)
}

// selectionMask validates a selection's painted mask: a PNG attachment of
// the room, on an image region. "" for none.
func (s *Service) selectionMask(ctx context.Context, roomID string, sel *wire.FileSelection) (string, error) {
	id := strings.TrimSpace(sel.Mask)
	if id == "" {
		return "", nil
	}
	if sel.Kind != "region" {
		return "", errBadFileRef{"only an image region carries a painted mask"}
	}
	if !s.attachmentsReady() {
		return "", errors.New("attachments not configured")
	}
	att, ok, err := s.Attachments.GetAttachment(ctx, roomID, id)
	if err != nil {
		return "", err
	}
	if !ok || att.ContentType != "image/png" {
		return "", errBadFileRef{"the painted mask must be a PNG attachment of this room"}
	}
	return att.ID, nil
}

// resolveFileRef validates the file a message names and completes it: one
// of the room's deliverables (never a web app: its snapshot is not a file
// an agent edits), at a version it has (0 = its current one), named as its
// file. nil for none.
func (s *Service) resolveFileRef(ctx context.Context, roomID string, ref *wire.FileRef, readable func(channelID string) bool) (*wire.FileRef, error) {
	if ref == nil {
		return nil, nil
	}
	id := strings.TrimSpace(ref.ArtifactID)
	if id == "" || s.Artifacts == nil {
		return nil, errBadFileRef{"fileRef names no deliverable"}
	}
	art, err := s.Artifacts.GetArtifact(ctx, id)
	if errors.Is(err, store.ErrArtifactNotFound) || (err == nil && (art.RoomID != roomID || !readable(art.ChannelID))) {
		return nil, errBadFileRef{"the file is not one of this room's deliverables (it may have been deleted)"}
	}
	if err != nil {
		return nil, fmt.Errorf("file lookup failed: %w", err)
	}
	if art.Kind == store.ArtifactKindSite {
		return nil, errBadFileRef{"a web app cannot be edited from its preview yet; ask in the chat"}
	}
	version := ref.Version
	if version == 0 {
		version = art.CurrentVersion
	}
	if version < 1 || version > art.CurrentVersion {
		return nil, errBadFileRef{fmt.Sprintf("%s has no version %d", art.Title, ref.Version)}
	}
	name := art.FileName
	if name == "" {
		name = path.Base(art.Title)
	}
	out := &wire.FileRef{ArtifactID: art.ArtifactID, Version: version, Name: name}
	if sel := ref.Selection; sel != nil {
		label := strings.TrimSpace(sel.Label)
		if label == "" || utf8.RuneCountInString(label) > wire.MaxFileSelectionLabel || utf8.RuneCountInString(sel.Detail) > wire.MaxFileSelectionDetail {
			return nil, errBadFileRef{fmt.Sprintf("the selection must be a label of 1-%d characters and at most %d characters of content", wire.MaxFileSelectionLabel, wire.MaxFileSelectionDetail)}
		}
		if sel.Kind != "" && !wire.IsFileSelectionKind(sel.Kind) {
			return nil, errBadFileRef{fmt.Sprintf("the selection kind %q is not one of %v", sel.Kind, wire.FileSelectionKinds)}
		}
		if len(sel.Keys) > wire.MaxFileSelectionKeys {
			return nil, errBadFileRef{fmt.Sprintf("a selection covers at most %d keys", wire.MaxFileSelectionKeys)}
		}
		for _, k := range sel.Keys {
			if k == "" || utf8.RuneCountInString(k) > wire.MaxFileSelectionKey {
				return nil, errBadFileRef{fmt.Sprintf("a selection key is 1-%d characters", wire.MaxFileSelectionKey)}
			}
		}
		mask, err := s.selectionMask(ctx, roomID, sel)
		if err != nil {
			return nil, err
		}
		out.Selection = &wire.FileSelection{Label: label, Detail: sel.Detail, Kind: sel.Kind, Keys: sel.Keys, Mask: mask}
	}
	return out, nil
}
