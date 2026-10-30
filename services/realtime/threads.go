package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Threads (docs/prd/2026-10-28-channels-and-threads.md §2.2, C1-C4): a
// thread is a channel of kind thread opened from one message (its root).
// Realtime opens it, because it holds the messages: the root must be a live
// message of the parent the opener may post in. Everything per channel
// (history, unread, typing, read state, search, schedules, agent jobs) then
// works in it unchanged; the thread's readers are its parent's (store.ThreadOf
// keeps its section and agents switch), and a reply in it counts on its root
// (noteThreadReply) and tells the parent's stream (EventThreadUpdated).
//
//	POST /v1/rooms/{roomId}/threads   wire.ThreadOpenRequest → wire.RoomChannel
//
// Opening again returns the thread the root has (one per root).

// errNotThreadable: the root is no live chat or agent message of the parent.
var errNotThreadable = errors.New("no such message in this channel to open a thread from")

func (s *Service) handleOpenThread(w http.ResponseWriter, r *http.Request) {
	roomID, userID, ok := s.roomCaller(w, r)
	if !ok {
		return
	}
	// C2: any member who may post in the parent opens a thread; never a
	// visitor (membership.Chat), never an agent's own act (an agent opens one
	// only as the member it works for, through the agent door).
	m, ok := s.member(w, r, roomID, userID, membership.Chat)
	if !ok {
		return
	}
	var body wire.ThreadOpenRequest
	if !httpx.DecodeJSON(w, r, maxChatBodyBytes, &body) {
		return
	}
	th, err := s.OpenThread(r.Context(), roomID, userID, m, body)
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(threadView(th))
	case errors.Is(err, errUnknownChannel), errors.Is(err, errNotThreadable):
		httpx.Error(w, err.Error(), http.StatusNotFound)
	default:
		var refusal postRefusal
		if errors.As(err, &refusal) {
			writePostRefusal(w, refusal)
			return
		}
		if errors.Is(err, errBadThread) {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		obs.ServerError(w, "open thread failed", err)
		httpx.Error(w, "open thread failed", http.StatusInternalServerError)
	}
}

var errBadThread = errors.New("a thread opens from a message of an ordinary channel")

// OpenThread opens (or returns) the thread of message body.RootSeq of
// channel body.ParentID for userID. The parent must be one the opener may
// read and post in (the channel gate's archive and who-posts rules; not slow
// mode, which counts posts), and the root a live message of it.
func (s *Service) OpenThread(ctx context.Context, roomID, userID string, m membership.Member, body wire.ThreadOpenRequest) (store.RoomChannel, error) {
	if s.Channels == nil || s.Chat == nil {
		return store.RoomChannel{}, errUnknownChannel
	}
	parent, err := s.channelOf(ctx, roomID, body.ParentID)
	if err != nil {
		return store.RoomChannel{}, err
	}
	if !s.memberMayRead(ctx, roomID, userID, m, parent.ChannelID) {
		return store.RoomChannel{}, errUnknownChannel
	}
	if !parent.Listed() {
		// No thread of a thread, and none in the people chat.
		return store.RoomChannel{}, errBadThread
	}
	if existing, found, err := s.channelStore().ThreadOfRoot(ctx, roomID, parent.ChannelID, body.RootSeq); err != nil {
		return store.RoomChannel{}, err
	} else if found {
		return existing, nil
	}
	if parent.Archived() {
		return store.RoomChannel{}, postRefusal{status: http.StatusForbidden, code: wire.ChannelArchived, msg: "this channel is archived: it is read-only until an owner restores it"}
	}
	if parent.Settings.WhoPosts == wire.ChannelPostersAdmins && !m.Role.CanInvite() {
		return store.RoomChannel{}, postRefusal{status: http.StatusForbidden, code: wire.ChannelAdminsOnly, msg: "only the room's owner and admins post in this channel"}
	}
	root, found, err := s.Chat.GetRoomMessage(ctx, roomID, body.RootSeq)
	if err != nil {
		return store.RoomChannel{}, err
	}
	if !found || root.ChannelID != parent.ChannelID || !wire.PersistedFeedKind(root.Kind) {
		return store.RoomChannel{}, errNotThreadable
	}
	return s.createThread(ctx, userID, m, parent, root, body.Title, nil)
}

// createThread makes the thread of root (a live message of parent) for its
// opener: titled (threadTitle) and tagged (a forum's tags, of the parent's
// vocabulary), followed by the opener and the root's author (C4), told to
// the parent's stream, and, when the parent's agents answer every new thread
// (C19), asked of its default agent. The one construction after the checks,
// for a thread opened from a message and for a forum's post.
func (s *Service) createThread(ctx context.Context, userID string, m membership.Member, parent store.RoomChannel, root store.RoomMessage, title string, tags []string) (store.RoomChannel, error) {
	roomID := parent.RoomID
	th := store.ThreadOf(parent, ids.New("ch_"), root.Seq, threadTitle(title, root), userID, time.Now().UnixMilli())
	if known := forumTags(parent.Settings, tags); len(known) > 0 {
		th.Thread.Tags = known
	}
	if err := s.channelStore().PutChannel(ctx, th); err != nil {
		if errors.Is(err, store.ErrThreadExists) {
			// Opened concurrently: the one that won is the thread.
			existing, found, gerr := s.channelStore().ThreadOfRoot(ctx, roomID, parent.ChannelID, root.Seq)
			if gerr == nil && found {
				return existing, nil
			}
		}
		return store.RoomChannel{}, err
	}
	// The opener and the root's author follow it (C4: who wrote in it).
	for _, u := range []string{userID, root.Author} {
		if u == "" || (u != userID && !membership.HumanMember(s.Membership, roomID, u)) {
			continue
		}
		if err := s.channelStore().FollowChannel(ctx, roomID, th.ChannelID, u, th.CreatedAtMs, store.FollowMarks{}); err != nil {
			log.Printf("realtime: room=%s thread=%s: follow for %s failed: %v", roomID, th.ChannelID, u, err)
		}
	}
	s.publishThreadUpdated(ctx, th)
	s.askChannelAgent(ctx, userID, parent, th, root)
	return th, nil
}

// openPostThread opens the thread a forum's post is the root of (§2.1c): the
// post (seq, just stored in channel) by p.Author, titled and tagged by the
// post's thread fields.
func (s *Service) openPostThread(ctx context.Context, p ChatPost, m membership.Member, channel store.RoomChannel, seq int64) (store.RoomChannel, error) {
	root, found, err := s.Chat.GetRoomMessage(ctx, p.RoomID, seq)
	if err != nil {
		return store.RoomChannel{}, err
	}
	if !found {
		return store.RoomChannel{}, errNotThreadable
	}
	var title string
	var tags []string
	if p.Thread != nil {
		title, tags = p.Thread.ThreadTitle, p.Thread.ThreadTags
	}
	return s.createThread(ctx, p.Author, m, channel, root, title, tags)
}

// forumTags keeps the tags of the parent's own vocabulary (C20), once each,
// at most MaxThreadTags; none outside a forum.
func forumTags(parent wire.ChannelSettings, tags []string) []string {
	if !parent.Forum() || len(tags) == 0 {
		return nil
	}
	var out []string
	for _, id := range tags {
		id = strings.TrimSpace(id)
		if slices.ContainsFunc(parent.Tags, func(t wire.ChannelTag) bool { return t.ID == id }) && !slices.Contains(out, id) && len(out) < wire.MaxThreadTags {
			out = append(out, id)
		}
	}
	return out
}

// askChannelAgent asks a new thread's question of its parent's default
// agent when the parent's agents answer every new thread (C19 "threads"):
// one message in the thread, as its opener, to that agent, marked automatic
// (payload.agentAuto), through PostChat like any addressed message (the
// agents switch, a platform agent's host, borrowing and the job are its).
// Best effort: the thread is open whatever happens to the request.
func (s *Service) askChannelAgent(ctx context.Context, opener string, parent store.RoomChannel, th store.RoomChannel, root store.RoomMessage) {
	a := parent.Settings.DefaultAgent
	if parent.Settings.AgentParticipation != wire.ChannelAgentsThreads || a == nil || a.OwnerID == "" || a.Provider == "" {
		return
	}
	text := strings.TrimSpace(rootText(root))
	if text == "" {
		text = th.Name
	}
	if _, err := s.PostChat(ctx, ChatPost{
		RoomID: parent.RoomID, Author: opener, Text: text, ChannelID: th.ChannelID,
		ClientMsgID: "auto:" + th.ChannelID, AtUnixMs: time.Now().UnixMilli(),
		Recipient: &ChatRecipient{OwnerID: a.OwnerID, Core: a.Core, Provider: a.Provider},
		AgentAuto: true,
	}); err != nil {
		log.Printf("realtime: room=%s thread=%s: the channel's agent was not asked: %v", parent.RoomID, th.ChannelID, err)
	}
}

// threadTitle is a thread's title: the opener's, else the root's first
// words (the AI may name it later, §2.2: an assist the member accepts).
func threadTitle(given string, root store.RoomMessage) string {
	t := strings.Join(strings.Fields(given), " ")
	if t == "" {
		t = strings.Join(strings.Fields(rootText(root)), " ")
	}
	if t == "" {
		t = "Thread"
	}
	if utf8.RuneCountInString(t) > wire.MaxThreadTitle {
		t = string([]rune(t)[:wire.MaxThreadTitle-1]) + "…"
	}
	return t
}

// rootText is the text of a chat message or the first text of an agent's
// batch.
func rootText(m store.RoomMessage) string {
	switch m.Kind {
	case wire.KindChatMessage:
		p, _ := wire.DecodePayload[wire.ChatMessagePayload](json.RawMessage(m.Payload))
		return p.Text
	case wire.KindAgentMessages:
		p, _ := wire.DecodePayload[wire.AgentMessagesPayload](json.RawMessage(m.Payload))
		for _, am := range p.Messages {
			if t := strings.TrimSpace(am.Payload.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

// followedThreads is the set of threads among channels userID follows (one
// read, only when the room has a thread). A failed read follows none: the
// unread then leaves threads out rather than count them for everyone.
func (s *Service) followedThreads(ctx context.Context, roomID, userID string, channels []store.RoomChannel) map[string]bool {
	if !slices.ContainsFunc(channels, store.RoomChannel.IsThread) {
		return nil
	}
	fs, err := s.channelStore().ChannelFollows(ctx, roomID, userID)
	if err != nil {
		log.Printf("realtime: room=%s user=%s: follows: %v", roomID, userID, err)
		return nil
	}
	out := make(map[string]bool, len(fs))
	for _, f := range fs {
		out[f.ChannelID] = true
	}
	return out
}

// errBadReply: replyTo names no live message the reply may answer.
var errBadReply = errors.New("replyTo names no message of this channel")

// replyRef completes a reply's reference (§2.3): seq must be a live chat or
// agent message of ch, or, in a thread, its root (a message of the parent).
// The quote is what the message said when answered.
func (s *Service) replyRef(ctx context.Context, roomID string, ch store.RoomChannel, seq int64) (wire.ReplyRef, error) {
	if s.Chat == nil {
		return wire.ReplyRef{Seq: seq}, nil
	}
	m, found, err := s.Chat.GetRoomMessage(ctx, roomID, seq)
	if err != nil {
		return wire.ReplyRef{}, err
	}
	sameChannel := found && m.ChannelID == ch.ChannelID
	threadRoot := found && ch.IsThread() && m.ChannelID == ch.ParentID && seq == ch.RootSeq
	if !found || !wire.PersistedFeedKind(m.Kind) || (!sameChannel && !threadRoot) {
		return wire.ReplyRef{}, errBadReply
	}
	excerpt := strings.Join(strings.Fields(rootText(m)), " ")
	if utf8.RuneCountInString(excerpt) > wire.MaxReplyExcerpt {
		excerpt = string([]rune(excerpt)[:wire.MaxReplyExcerpt-1]) + "…"
	}
	author := m.Author
	if m.Kind == wire.KindAgentMessages {
		p, _ := wire.DecodePayload[wire.AgentMessagesPayload](json.RawMessage(m.Payload))
		if len(p.Messages) > 0 {
			author = p.Messages[0].Payload.OwnerUserID + "/" + p.Messages[0].Payload.Provider
		}
	}
	return wire.ReplyRef{Seq: seq, Author: author, Excerpt: excerpt}, nil
}

// noteThreadPost counts a new message of a thread on its root and tells its
// parent's stream: called after a chat or agent message is stored in
// channelID (a no-op for any other channel). Best effort.
func (s *Service) noteThreadPost(ctx context.Context, roomID, channelID, author string, atMs int64) {
	if s.Channels == nil || channelID == "" {
		return
	}
	ch, found, err := s.channelStore().GetChannel(ctx, roomID, channelID)
	if err != nil || !found || !ch.IsThread() {
		return
	}
	th, err := s.channelStore().NoteThreadReply(ctx, roomID, channelID, author, atMs)
	if err != nil {
		log.Printf("realtime: room=%s thread=%s: reply count failed: %v", roomID, channelID, err)
		return
	}
	s.publishThreadUpdated(ctx, th)
}

// publishThreadUpdated tells the parent channel's readers about a thread
// (its root's summary line): scoped to the parent, whose readers are the
// thread's.
func (s *Service) publishThreadUpdated(ctx context.Context, th store.RoomChannel) {
	_, _ = s.publish(ctx, wire.Event{
		RoomID: th.RoomID, ChannelID: th.ParentID, Kind: wire.EventThreadUpdated,
		Payload:  th.ThreadUpdated(),
		AtUnixMs: time.Now().UnixMilli(),
	}, realtime.Durable{})
}

// threadView answers a just-opened thread (the opener follows it).
func threadView(th store.RoomChannel) wire.RoomChannel {
	info := th.ThreadInfo()
	return wire.RoomChannel{ChannelID: th.ChannelID, Name: th.Name, Kind: th.Kind, CreatedBy: th.CreatedBy, SectionID: th.SectionID,
		OwnerID: th.OwnerID, Settings: th.Settings, Joined: true, Thread: &info}
}
