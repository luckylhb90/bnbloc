// Package realtimehttp exposes the bnbloc-realtime fan-out over HTTP using
// Server-Sent Events (stdlib-only, air-gapped friendly). Presence and business
// events share the one room event stream.
package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Chat size limits: a product cap on message text, and a larger cap on the whole
// request body (text + recipient + framing) as defense-in-depth.
const (
	maxChatTextBytes = wire.MaxChatTextBytes
	maxChatBodyBytes = 64 * 1024
	// maxSmallBodyBytes caps the tiny JSON bodies of read/typing.
	maxSmallBodyBytes = 4 * 1024
	// maxInternalPublishBytes caps one event posted to the internal ingest (an
	// agent_messages batch is the largest legitimate payload).
	maxInternalPublishBytes = 1 << 20
	// maxUnreadRooms bounds one rooms-unread query: each room costs a
	// membership probe plus per-channel counts, so an unbounded list is a
	// request-amplification vector. Larger lists are rejected (400), never
	// silently truncated, so a client cannot mistake a cut list for "no unread".
	maxUnreadRooms = 200
)

// errUnknownChannel rejects an explicit channelId that is not a live channel of
// the addressed room, so a stale or foreign channel id can never route a send,
// history read, read cursor, typing hint, or stream into another room's stream.
var errUnknownChannel = errors.New("channel not found in room")

// errAgentInPeopleChat rejects an agent recipient (or an agent reply) in the
// room's people chat: it is a human-only conversation agents never see.
var errAgentInPeopleChat = errors.New("agents cannot be addressed in the people chat")

// Membership authorizes a caller for a room (fail closed), through
// membership.RoomScope (s.member): any membership, a platform agent host's
// included, reads the room (its bridge streams it and reads history);
// Member.IsHuman gates the human-only surfaces (presence, roster
// advertisement, the people chat, plugin events, mentions) and
// Member.MayChat everything that acts in the conversation (chat, typing,
// schedules, dispatch requesters: PostChat enforces it for every post).
type Membership = membership.Directory

// Service holds the realtime handler dependencies.
type Service struct {
	Hub        *realtime.Hub
	Membership Membership
	Presence   *realtime.Presence
	// Roster tracks which agents each member's bridge advertises as reachable in
	// a room, so the composer picker offers real drivable agents without the
	// gateway reaching any user's machine (remote-safe). Lazily created in
	// Handler when not injected.
	Roster *realtime.AgentRoster
	// Chat optionally persists room-feed messages for history paging. Nil keeps
	// realtime in-memory-only (local dev / tests).
	Chat ChatStore
	// Channels optionally resolves channel routing: the default channel that
	// receives requests omitting channelId, and the channel list for per-channel
	// unread aggregation. Nil routes channel-less traffic to the legacy empty
	// channel "" (pre-channels rooms keep working unchanged).
	Channels   ChannelStore
	BridgeJobs BridgeJobStore
	// Attachments + Blobs back the multimodal upload/download path: metadata in
	// the store, binaries in the blob store. Nil disables attachments (dev with
	// no blob backend still runs text chat).
	Attachments AttachmentStore
	Blobs       BlobStore
	BlobError   string
	// DirectDownloads (BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS) lets
	// POST /internal/v1/blobs/presign hand out presigned URLs when Blobs
	// can presign (S3); off, every download streams through bnbloc.
	DirectDownloads bool
	PluginHooks     *PluginHookDispatcher
	// Team features (team.go): message tasks and global search. A nil
	// store answers its routes 503 (search skips that kind); Rooms is the
	// server-side membership listing search is scoped to.
	Tasks     store.MessageTaskStore
	Artifacts store.ArtifactIndex
	Rooms     RoomDirectory
	// Search is the message search backend (internal/search): the store's
	// FULLTEXT index, or OpenSearch. SearchUpdates keeps an external index
	// current: the hub's commit hook queues new rows, and edits and deletes
	// queue the message they changed (nil for the store index, which needs
	// none). Queuing never blocks or fails the write.
	Search        search.Index
	SearchUpdates *search.Updater
	// Notifications observes published events (mentions, agent job outcomes)
	// for member notifications and serves the notification settings routes.
	// Nil disables both; observation never blocks a publish.
	Notifications *notify.Service
	// Schedules persists scheduled prompts (N2) and drives the scheduler
	// (RunScheduler); RoomRoles reads room roles/existence for schedule
	// moderation. A nil Schedules answers the schedule routes 503.
	Schedules store.RoomScheduleStore
	RoomRoles RoomRoleStore
	// PlatformAgents refuses a chat to a platform agent whose host is
	// offline (409 platform_agent_offline) before any job exists. Nil
	// disables the guard.
	PlatformAgents *PlatformAgentGuard
	// Dispatches backs the agent dispatch route (dispatch.go): the parent
	// job chain, a parent's dispatches and bridge owners. Nil answers the
	// route 503. Audit records every dispatch decision (extension_audit kind
	// agent_dispatch); nil records nothing.
	Dispatches DispatchStore
	Audit      store.ExtensionAuditStore
	// Content resolves the expert a chat asks its agent to wear (content
	// components). Nil refuses a chat that names an expert.
	Content store.ContentComponentStore
	// Peers is the scale-out replica set (scaleout.go): requests for a room
	// or member another replica owns are forwarded there, and work produced
	// here for such keys crosses over internal peer routes. Nil is a single
	// node (every key owned here).
	Peers *Peers
	// HeartbeatEvery is the room stream's keep-alive and membership re-check
	// interval (0 = 15s).
	HeartbeatEvery time.Duration
}

func (s *Service) heartbeatEvery() time.Duration {
	if s.HeartbeatEvery > 0 {
		return s.HeartbeatEvery
	}
	return 15 * time.Second
}

// ChatStore persists room-feed messages, reads history pages, and tracks
// per-user read state for unread counts.
type ChatStore interface {
	// AppendRoomMessages is the hub's journal (realtime.Journal): the room's
	// group commit stores each batch of durable events through it.
	AppendRoomMessages(ctx context.Context, rows []store.RoomMessage) error
	RoomHistory(ctx context.Context, roomID, channelID string, before int64, limit int) ([]store.RoomMessage, error)
	RoomFeed(ctx context.Context, roomID string, after int64, limit int) ([]store.RoomMessage, error)
	MarkRead(ctx context.Context, roomID, channelID, userID string, seq, atMs int64) error
	UnreadCount(ctx context.Context, roomID, channelID, userID string) (int, error)
	MaxRoomSeq(ctx context.Context, roomID, channelID string) (int64, error)
	LookupClientMsg(ctx context.Context, roomID, clientMsgID string) (int64, bool)
	DeleteRoomMessage(ctx context.Context, roomID string, seq int64, requester string, atMs int64) (bool, error)
	EditRoomMessage(ctx context.Context, roomID string, seq int64, requester, text string, atMs int64) (edited bool, err error)
	// GetRoomMessage returns one live message by sequence (found=false when
	// missing or deleted): edits and deletes scope their live event to the
	// message's channel with it.
	GetRoomMessage(ctx context.Context, roomID string, seq int64) (store.RoomMessage, bool, error)
}

// ChannelStore resolves a room's channel routing. It is the minimal subset of
// the store's channel API the realtime service needs: the default channel for
// requests that omit channelId, membership validation for an explicit one, and
// the channel list for unread aggregation.
type ChannelStore interface {
	// GetChannel returns a live channel of the room, found=false otherwise.
	GetChannel(ctx context.Context, roomID, channelID string) (store.RoomChannel, bool)
	// DefaultChannel returns the room's default channel, found=false when none.
	DefaultChannel(ctx context.Context, roomID string) (store.RoomChannel, bool)
	// ListChannels returns the room's live channels, default first.
	ListChannels(ctx context.Context, roomID string) []store.RoomChannel
	// ChannelKind reads a channel's kind, reporting read failures so the
	// people-chat guards fail closed.
	ChannelKind(ctx context.Context, roomID, channelID string) (kind string, found bool, err error)
}

// BridgeJobStore persists durable agent jobs created from agent-directed chat.
type BridgeJobStore interface {
	PutAgentJob(ctx context.Context, j store.AgentJob) (store.AgentJob, error)
}

// Handler builds the HTTP mux for the realtime service. It lazily creates a
// Presence tracker and agent roster bound to the hub when not injected, and
// wraps the mux with svcauth.EnsureTenant so requests always carry a tenant
// scope for the store layer (the gateway's Middleware keeps its verified value
// when it ran first).
func (s *Service) Handler() http.Handler {
	// Presence and roster events are announced only by the room's owner.
	if s.Presence == nil {
		s.Presence = realtime.NewPresence(s.publishOwned)
	}
	if s.Roster == nil {
		s.Roster = realtime.NewAgentRoster(s.publishOwned)
	}
	if s.PluginHooks == nil {
		s.PluginHooks = NewPluginHookDispatcher(nil, nil, nil)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rooms/{roomId}/events", s.handleEvents)
	mux.HandleFunc("GET /v1/rooms/{roomId}/agents", s.handleAgents)
	mux.HandleFunc("POST /v1/rooms/{roomId}/chat", s.handleChat)
	mux.HandleFunc("POST /v1/rooms/{roomId}/attachments", s.handleUploadAttachment)
	mux.HandleFunc("GET /v1/rooms/{roomId}/attachments/{attachmentId}", s.handleDownloadAttachment)
	mux.HandleFunc("GET /v1/rooms/{roomId}/history", s.handleHistory)
	mux.HandleFunc("POST /v1/rooms/{roomId}/typing", s.handleTyping)
	mux.HandleFunc("POST /v1/rooms/{roomId}/read", s.handleMarkRead)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/messages/{seq}", s.handleDeleteMessage)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}/messages/{seq}", s.handleEditMessage)
	mux.HandleFunc("GET /v1/rooms-unread", s.handleUnread)
	s.registerTeamRoutes(mux)
	s.registerScheduleRoutes(mux)
	s.registerPluginRoutes(mux)
	s.Notifications.Register(mux)
	// Internal-only routes: callable exclusively by a signed control-plane
	// identity (svcauth.RequireSigned). An anonymous or forged publish could
	// otherwise inject a chat_message that drives any member's agent.
	internal := func(h http.HandlerFunc) http.Handler { return svcauth.RequireSigned(svcauth.KeyFromEnv(), h) }
	mux.Handle("POST /internal/v1/rooms/{roomId}/publish", internal(s.handleInternalPublish))
	// Generic content-addressed blob store, room-less: the gateway proxies here
	// (svcauth-signed) to distribute sidecar-core artifact binaries. Distinct
	// from the room-scoped, type-gated attachment path above.
	mux.Handle("POST /internal/v1/blobs", internal(s.handleInternalBlobPut))
	mux.Handle("GET /internal/v1/blobs", internal(s.handleInternalBlobCapability))
	mux.Handle("GET /internal/v1/blobs/{key...}", internal(s.handleInternalBlobGet))
	mux.Handle("POST /internal/v1/blobs/presign", internal(s.handleInternalBlobPresign))
	// Handoff package (room service orchestrates export/import): the whole
	// feed as a forward cursor, and the history-only import write.
	mux.Handle("GET /internal/v1/rooms/{roomId}/feed", internal(s.handleInternalFeed))
	mux.Handle("POST /internal/v1/rooms/{roomId}/import", internal(s.handleInternalImport))
	// Agent dispatch (room tools): a bridge, signed as its owner by the
	// gateway, posts a task for another agent on behalf of the requester of
	// the job it is running. Room-keyed, so it runs on the room's owner.
	mux.Handle("POST /internal/v1/rooms/{roomId}/dispatch", internal(s.handleInternalDispatch))
	// Replica-to-replica routes (scaleout.go).
	s.registerPeerRoutes(mux, internal)
	// Scale-out: release the state of keys this replica stops owning after
	// every peer-set refresh, and route each keyed request to its owner.
	s.Peers.Set().OnRefresh(s.reconcileOwnership)
	return shard.Forward(s.Peers.Set(), RouteKey, svcauth.EnsureTenant(mux))
}

// resolveChannel maps a request's channelId to its routing target. An explicit
// channel must be a live channel of the room (errUnknownChannel otherwise); an
// omitted one resolves the room's default channel so channel-less clients keep
// landing in one well-known stream. A room with no default channel
// (pre-channels data, or a broken create invariant) falls back to the legacy
// empty channel — where pre-channel rows live — after logging: messages routed
// there are invisible to the default channel's history. With no channel store
// (in-memory dev) an explicit id passes through unvalidated.
func (s *Service) resolveChannel(ctx context.Context, roomID, channelID string) (string, error) {
	if channelID = strings.TrimSpace(channelID); channelID != "" {
		if s.Channels != nil {
			if _, ok := s.Channels.GetChannel(ctx, roomID, channelID); !ok {
				return "", errUnknownChannel
			}
		}
		return channelID, nil
	}
	if s.Channels != nil {
		if def, ok := s.Channels.DefaultChannel(ctx, roomID); ok {
			return def.ChannelID, nil
		}
		log.Printf("realtime: room=%s has no default channel; routing to the empty channel", roomID)
	}
	return "", nil
}

// isPeopleChannel reports whether channelID is the room's people chat. The
// room-wide pseudo channel "" and a store-less (in-memory dev) service are
// never people chat. A read failure is returned so every guard built on it
// fails closed (a people-chat event is never delivered to an agent stream or
// a plugin because a lookup blipped).
func (s *Service) isPeopleChannel(ctx context.Context, roomID, channelID string) (bool, error) {
	if channelID == "" || s.Channels == nil {
		return false, nil
	}
	kind, _, err := s.Channels.ChannelKind(ctx, roomID, channelID)
	if err != nil {
		return false, err
	}
	return kind == store.ChannelKindPeople, nil
}

// agentMayReadChannel refuses an agent-side reader (a platform agent host)
// the people chat, failing closed on a lookup error. It writes the error and
// returns false when the read must not proceed.
func (s *Service) agentMayReadChannel(w http.ResponseWriter, ctx context.Context, roomID, channelID string) bool {
	people, err := s.isPeopleChannel(ctx, roomID, channelID)
	if err != nil {
		obs.ServerError(w, "channel lookup failed", err)
		http.Error(w, "channel lookup failed", http.StatusInternalServerError)
		return false
	}
	if people {
		http.Error(w, "the people chat is for people only", http.StatusForbidden)
		return false
	}
	return true
}

// peopleFilter decides, per event, whether an all-channels subscriber that
// did not opt into the people chat (the bridge: agent streams never pass
// includePeople=1) must skip it. A channel's kind never changes, so verdicts
// are memoized for the stream's lifetime; a failed lookup skips the event and
// is retried on the next one.
type peopleFilter struct {
	svc     *Service
	ctx     context.Context
	roomID  string
	verdict map[string]bool // channelID -> is people chat
}

func (f *peopleFilter) skip(ev wire.Event) bool {
	if ev.ChannelID == "" {
		return false
	}
	if people, ok := f.verdict[ev.ChannelID]; ok {
		return people
	}
	people, err := f.svc.isPeopleChannel(f.ctx, f.roomID, ev.ChannelID)
	if err != nil {
		log.Printf("realtime: room=%s channel=%s kind lookup failed; withholding event: %v", f.roomID, ev.ChannelID, err)
		return true
	}
	f.verdict[ev.ChannelID] = people
	return people
}

// revocation reports whether ev ends access to its room: the room was
// deleted (user "", every member) or one member was removed. The payload is
// a typed wire.MemberRemovedPayload in-process and a decoded JSON map when
// it arrived through the internal ingest.
func revocation(ev wire.Event) (user string, revoked bool) {
	switch ev.Kind {
	case wire.EventRoomDeleted:
		return "", true
	case wire.EventMemberRemoved:
		switch p := ev.Payload.(type) {
		case wire.MemberRemovedPayload:
			return p.UserID, p.UserID != ""
		case *wire.MemberRemovedPayload:
			if p != nil {
				return p.UserID, p.UserID != ""
			}
		case map[string]any:
			u, _ := p["userId"].(string)
			return u, u != ""
		}
	}
	return "", false
}

// accessRevoked reports whether ev ends the stream of subject.
func accessRevoked(ev wire.Event, subject string) bool {
	user, revoked := revocation(ev)
	return revoked && (user == "" || user == subject)
}

// handleEvents streams room events as SSE, delivering the replay snapshot first
// then incremental deliveries until the client disconnects.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	// A stream outlives any cache entry: it opens on the room service's
	// current answer (never a cached one, which could still admit a member
	// removed seconds ago) and re-checks at every heartbeat below.
	m, err := membership.FreshRoomScope(r.Context(), s.Membership, roomID, userID, membership.AnyMember)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	// A platform agent host's stream (its bridge driving the hosted agent)
	// is an agent's: never present, never advertising a roster, and never
	// shown the people chat.
	human := m.IsHuman()
	flusher, ok := w.(http.Flusher)
	if !ok {
		obs.ServerError(w, "streaming unsupported", nil)
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Channel scope: a channel-scoped subscriber receives one channel (omitted
	// resolves to the room's default; the people-chat panel names the people
	// chat explicitly). allChannels=1 receives every channel EXCEPT the people
	// chat, unless the subscriber also opts in with includePeople=1 — the web
	// room view does, the bridge never does, so a people-chat event can never
	// reach an agent's stream. Room-wide events (empty ChannelID: presence,
	// roster, agent state) always deliver.
	allChannels := r.URL.Query().Get("allChannels") == "1"
	var people *peopleFilter
	if allChannels && (r.URL.Query().Get("includePeople") != "1" || !human) {
		people = &peopleFilter{svc: s, ctx: r.Context(), roomID: roomID, verdict: map[string]bool{}}
	}
	channelID := ""
	if !allChannels {
		if channelID, err = s.resolveChannel(r.Context(), roomID, r.URL.Query().Get("channelId")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if !human && !s.agentMayReadChannel(w, r.Context(), roomID, channelID) {
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx-style edges buffer proxied bodies unless told otherwise; an SSE
	// stream must reach the client as it is written.
	w.Header().Set("X-Accel-Buffering", "no")

	var sub *realtime.Subscriber
	if r.URL.Query().Get("replay") == "0" {
		sub = s.Hub.SubscribeLive(roomID)
	} else {
		sub = s.Hub.Subscribe(roomID)
	}
	defer s.Hub.Unsubscribe(roomID, sub)
	// A non-presence subscriber (the bridge, which subscribes to drive agents)
	// observes the room without counting as a present human. Without this, the
	// bridge inflates presence and its reconnects churn the roster.
	countPresence := r.URL.Query().Get("presence") != "0" && human
	sseKind := realtime.SSEKindBridge
	if countPresence {
		sseKind = realtime.SSEKindMember
	}
	defer realtime.TrackSSE(s.Hub.Metrics, sseKind)()
	if countPresence {
		s.Presence.Enter(roomID, userID)
		defer s.Presence.Leave(roomID, userID)
	}
	// The bridge advertises the agents it serves in this room via ?agents=
	// (core/provider,…), tying their liveness to its subscription: the roster
	// withdraws them after a grace window when the bridge disconnects. The owner
	// is the authenticated subject, so a bridge can only advertise its own
	// agents. Presence-counting human subscribers never carry ?agents=.
	// An agent host advertises nothing: a platform agent is offered through
	// the room's platform-agent listing, not as the host's own roster.
	if adv := parseAgentRefs(r.URL.Query().Get("agents"), r.URL.Query().Get("sandboxed")); human && len(adv) > 0 {
		s.Roster.Advertise(roomID, userID, adv)
		defer s.Roster.Withdraw(roomID, userID)
	}
	// Open the stream with body bytes: an edge proxy may hold the response
	// headers until the first body byte, so a quiet room would otherwise
	// "open" only at the first heartbeat (15s). Readers ignore comment lines.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	// Heartbeat: browsers' EventSource and intermediary proxies drop an idle
	// connection after a read timeout. Without periodic bytes, a quiet room
	// silently disconnects the stream ("request timed out"), which the client
	// then reconnects — churning presence join/leave and, worse, missing any
	// agent reply produced during the dead window. A comment line every 15s
	// keeps the stream alive without emitting a spurious event.
	heartbeat := time.NewTicker(s.heartbeatEvery())
	defer heartbeat.Stop()
	// write streams one event; it reports whether the subscriber's access
	// ended (member removed / room deleted): the client has been told why, so
	// the stream closes rather than running until it reconnects.
	write := func(ev wire.Event) (revoked bool) {
		if !allChannels && ev.ChannelID != "" && ev.ChannelID != channelID {
			return false
		}
		if people != nil && people.skip(ev) {
			return false
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		// Only durable events carry a sequence (the SSE id); a transient
		// event has none and must not reset the stream's last event id.
		if ev.Sequence > 0 {
			fmt.Fprintf(w, "id: %d\n", ev.Sequence)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data)
		return accessRevoked(ev, userID)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// The revocation event is live-only and may never reach this
			// replica (an ownership move, a failed ingest): the stream
			// re-checks the membership it opened on, and ends once it is gone.
			if _, err := membership.RoomScope(ctx, s.Membership, roomID, userID, membership.AnyMember); err != nil {
				return
			}
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-sub.Events():
			if !open {
				return
			}
			// Write what is already queued, then flush once: a group commit
			// delivers a batch as one burst, and a flush per event lets a
			// busy stream fall behind (and overflow, which closes it). Only
			// this handler receives, so the queued receives never block.
			revoked := write(ev)
			for n := len(sub.Events()); n > 0 && open && !revoked; n-- {
				if ev, open = <-sub.Events(); open {
					revoked = write(ev)
				}
			}
			flusher.Flush()
			if revoked || !open {
				return
			}
		}
	}
}

// handleAgents returns the room's current agent roster: the union of the agents
// every member's bridge advertises as reachable here. The composer picker reads
// this instead of listing the caller's own local targets, so it works when the
// gateway is remote and cannot reach a user's machine. Members read it (fail
// closed on non-membership); the live SSE stream keeps it fresh via
// agent_roster events.
func (s *Service) handleAgents(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if _, ok := s.member(w, r, roomID, userID, membership.AnyMember); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"agents": s.Roster.Snapshot(roomID)})
}

// parseAgentRefs decodes the ?agents= advertisement param: a comma-separated
// list of "core/provider" (or bare "provider") tokens. The optional
// ?sandboxed= param lists, in the same token form, the advertised agents whose
// borrowed drives run in the owner's agent sandbox. Normalization and de-dup
// happen in the roster; this only splits and drops empties.
func parseAgentRefs(raw, sandboxedRaw string) []realtime.AgentRef {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	sandboxed := map[string]bool{}
	for _, tok := range strings.Split(sandboxedRaw, ",") {
		if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
			sandboxed[tok] = true
		}
	}
	var out []realtime.AgentRef
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		coreName, provider := core.SplitTargetID(tok)
		out = append(out, realtime.AgentRef{Core: coreName, Provider: provider, Sandboxed: sandboxed[strings.ToLower(tok)]})
	}
	return out
}

// handleChat publishes a group-chat message to the room lane, routed to one
// channel of the room's message stream (the default when the body omits
// channelId).
func (s *Service) handleChat(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	// Only a member who may chat posts (PostChat checks it): never a visitor,
	// and never a platform agent host's membership (its agent's replies go
	// through activity).
	var body struct {
		Text     string `json:"text"`
		AtUnixMs int64  `json:"atUnixMs"`
		// ClientMsgId, when set, makes the send idempotent: a retry with the same
		// id returns the original sequence instead of posting a duplicate.
		ClientMsgId string `json:"clientMsgId"`
		// ChannelId routes the message to one channel of the room; omitted or
		// empty means the room's default channel.
		ChannelId string `json:"channelId"`
		// Recipient routes the message to one member's agent; omitted/empty
		// means people-only (no agent is driven). See ChatRecipient.
		Recipient *ChatRecipient `json:"recipient,omitempty"`
		// AttachmentIds reference uploaded attachments (via POST .../attachments)
		// to embed in this message; validated to belong to the room.
		AttachmentIds []string `json:"attachmentIds,omitempty"`
		// Expert asks the agent recipient to wear an expert (content
		// component id, optional version).
		Expert *wire.ChatExpert `json:"expert,omitempty"`
	}
	// Cap the request body so a client cannot stream an unbounded payload; the
	// text limit in PostChat is the product bound, this is defense-in-depth.
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	posted, err := s.PostChat(r.Context(), ChatPost{
		RoomID:        roomID,
		Author:        userID,
		Text:          body.Text,
		ClientMsgID:   body.ClientMsgId,
		ChannelID:     body.ChannelId,
		AtUnixMs:      body.AtUnixMs,
		Recipient:     body.Recipient,
		AttachmentIDs: body.AttachmentIds,
		Expert:        body.Expert,
	})
	if err != nil {
		writeChatPostErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"sequence": posted.Sequence})
}

// writeChatPostErr maps a PostChat error to its HTTP status.
func writeChatPostErr(w http.ResponseWriter, err error) {
	var bad errBadAttachment
	var offline errPlatformAgentOffline
	switch {
	case membership.IsRefusal(err):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.As(err, &offline):
		writePlatformAgentOffline(w, offline)
	case errors.Is(err, errMessageTooLong):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, errEmptyMessage), errors.Is(err, errAgentInPeopleChat), errors.As(err, &bad),
		errors.Is(err, errExpertNotAvailable), errors.Is(err, errExpertWithoutAgent):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errUnknownChannel):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		obs.ServerError(w, "message persistence failed", err)
		http.Error(w, "message persistence failed", http.StatusInternalServerError)
	}
}

// handleInternalPublish accepts an event from another control-plane service
// (bnbloc-activity) and fans it out to this room's subscribers. This endpoint is
// cluster-internal: the gateway never routes public traffic to /internal/.
func (s *Service) handleInternalPublish(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ev wire.Event
	r.Body = http.MaxBytesReader(w, r.Body, maxInternalPublishBytes)
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	ev.RoomID = roomID
	// A removal or room deletion drops the cached membership before the
	// event goes out: a client that reconnects on it is refused at once.
	if user, revoked := revocation(ev); revoked && s.Membership != nil {
		s.Membership.Forget(roomID, user)
	}
	if ev.Kind == wire.KindAgentMessages {
		people, err := s.isPeopleChannel(r.Context(), roomID, ev.ChannelID)
		if err != nil {
			obs.ServerError(w, "channel lookup failed", err)
			http.Error(w, "channel lookup failed", http.StatusInternalServerError)
			return
		}
		if people {
			http.Error(w, errAgentInPeopleChat.Error(), http.StatusBadRequest)
			return
		}
	}
	// A publisher that leaves the time unset (activity's reply path) gets the
	// server's: the stored created_at_ms orders history and global search.
	ev.AtUnixMs = serverTimeIfUnset(ev.AtUnixMs)
	// Same durable-then-deliver contract as chat. The event is stored under
	// the tenant the signed publisher asserted (bound into the context by
	// svcauth), so a non-default tenant's replies land in its own history.
	d, err := s.durable(r.Context(), ev, "", "")
	if err != nil {
		obs.ServerError(w, "publish failed", err)
		http.Error(w, "publish failed", http.StatusInternalServerError)
		return
	}
	published, err := s.Hub.PublishCommit(ev, d)
	if err != nil {
		if !errors.Is(err, store.ErrDuplicateClientMsg) {
			log.Printf("realtime: persist room=%s kind=%s: %v", ev.RoomID, ev.Kind, err)
		}
		obs.ServerError(w, "publish failed", err)
		http.Error(w, "publish failed", http.StatusInternalServerError)
		return
	}
	s.deliverPluginEvent(r.Context(), published)
	s.onMemberJoined(r.Context(), published)
	w.WriteHeader(http.StatusOK)
}

// deliverPluginEvent is the post-publish tap: plugin hook delivery and the
// notification observer. Both are best-effort and never block the publisher.
// The people chat never reaches plugin hooks (a plugin is agent-side code);
// its mentions still notify members.
func (s *Service) deliverPluginEvent(ctx context.Context, ev wire.Event) {
	if s.PluginHooks != nil {
		people, err := s.isPeopleChannel(ctx, ev.RoomID, ev.ChannelID)
		if err != nil {
			log.Printf("realtime: room=%s channel=%s kind lookup failed; skipping plugin hooks: %v", ev.RoomID, ev.ChannelID, err)
		} else if !people && s.PluginHooks.Hooked(ev.Kind) {
			s.PluginHooks.Deliver(ctx, s.Membership, ev)
			// Members owned by other replicas hold their plugin streams
			// there: hand the event to every peer for its own fan-out.
			s.Peers.forwardPluginEvent(tenant.FromContext(ctx), ev)
		}
	}
	s.Notifications.Observe(ctx, ev)
}

// messageChannel returns the channel of a live room message, so a transient
// edit/delete event is scoped to it (and the people-chat stream filter sees
// it). found=false when the message is missing or deleted.
func (s *Service) messageChannel(ctx context.Context, roomID string, seq int64) (string, bool, error) {
	m, found, err := s.Chat.GetRoomMessage(ctx, roomID, seq)
	if err != nil || !found {
		return "", found, err
	}
	return m.ChannelID, true, nil
}

// durable builds what the hub's group commit stores for a room-feed event,
// for history paging: the row under the request's tenant, through the chat
// store. The hub stores only feed-bearing kinds and delivers only once the
// row is durable, so a failure aborts delivery and is returned to the
// publisher. clientMsgID is the optional send-idempotency id (chat only; ""
// otherwise). With no chat store (in-memory dev) nothing is stored.
func (s *Service) durable(ctx context.Context, ev wire.Event, author, clientMsgID string) (realtime.Durable, error) {
	if s.Chat == nil || !wire.PersistedFeedKind(ev.Kind) {
		return realtime.Durable{}, nil
	}
	tid, err := tenant.Require(ctx)
	if err != nil {
		return realtime.Durable{}, err
	}
	if author == "" {
		if p, ok := ev.Payload.(map[string]any); ok {
			if u, ok := p["userId"].(string); ok {
				author = u
			}
		}
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return realtime.Durable{}, err
	}
	return realtime.Durable{Journal: s.Chat, Row: store.RoomMessage{
		TenantID:    tid,
		Author:      author,
		ClientMsgID: clientMsgID,
		Payload:     payload,
	}}, nil
}

// handleHistory returns a page of persisted room-feed messages older than an
// optional `before` sequence cursor (newest page when absent), ascending by
// sequence, so a client can load earlier history beyond the live replay buffer.
// The page is scoped to one channel: the `channelId` query param, defaulting
// to the room's default channel when omitted.
func (s *Service) handleHistory(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	m, ok := s.member(w, r, roomID, userID, membership.AnyMember)
	if !ok {
		return
	}
	if s.Chat == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{}})
		return
	}
	before := parseInt(r.URL.Query().Get("before"))
	limit := int(parseInt(r.URL.Query().Get("limit")))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	channelID, err := s.resolveChannel(r.Context(), roomID, r.URL.Query().Get("channelId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !m.IsHuman() && !s.agentMayReadChannel(w, r.Context(), roomID, channelID) {
		return
	}
	msgs, err := s.Chat.RoomHistory(r.Context(), roomID, channelID, before, limit)
	if err != nil {
		obs.ServerError(w, "history read failed", err)
		http.Error(w, "history read failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		row := map[string]any{
			"sequence": m.Seq,
			"kind":     m.Kind,
			"payload":  json.RawMessage(m.Payload),
			"atUnixMs": m.CreatedAtMs,
		}
		if m.EditedAtMs != nil {
			row["editedAtMs"] = *m.EditedAtMs
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": out})
}

// parseInt parses a base-10 int64 from a query value, 0 on empty/invalid.
func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// handleMarkRead advances the caller's last-read sequence in a room's channel,
// so unread counts reflect what they have seen. Body: {"seq": <sequence>,
// "channelId": <optional channel; the room's default when omitted>}.
func (s *Service) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if _, ok := s.member(w, r, roomID, userID, membership.AnyMember); !ok {
		return
	}
	var body struct {
		Seq int64 `json:"seq"`
		// ChannelId scopes the cursor to one channel; omitted or empty means
		// the room's default channel.
		ChannelId string `json:"channelId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if s.Chat != nil {
		channelID, err := s.resolveChannel(r.Context(), roomID, body.ChannelId)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := s.Chat.MarkRead(r.Context(), roomID, channelID, userID, body.Seq, time.Now().UnixMilli()); err != nil {
			log.Printf("realtime: mark-read room=%s user=%s seq=%d: %v", roomID, userID, body.Seq, err)
			http.Error(w, "mark-read failed", http.StatusInternalServerError)
			return
		}
		// Pre-channel rows live in the empty channel but are surfaced as the
		// default channel's unread. Reading the default channel must clear them
		// too, otherwise that count can never be cleared by any action.
		if s.Channels != nil {
			if def, ok := s.Channels.DefaultChannel(r.Context(), roomID); ok && def.ChannelID == channelID {
				if err := s.Chat.MarkRead(r.Context(), roomID, "", userID, body.Seq, time.Now().UnixMilli()); err != nil {
					log.Printf("realtime: mark-read legacy room=%s user=%s seq=%d: %v", roomID, userID, body.Seq, err)
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleDeleteMessage soft-deletes a room message the caller authored, then
// publishes a transient message_deleted event so live clients remove it. The
// author check is enforced in the store; a non-author (or missing) message
// yields 403 and nothing is broadcast. Deletion is excluded from history.
func (s *Service) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if _, ok := s.member(w, r, roomID, userID, membership.AnyMember); !ok {
		return
	}
	seq := parseInt(r.PathValue("seq"))
	if seq <= 0 || s.Chat == nil {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	// Read the message's channel before the delete hides it: the transient
	// event is scoped to that channel.
	channelID, _, err := s.messageChannel(r.Context(), roomID, seq)
	if err != nil {
		obs.ServerError(w, "delete failed", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	deleted, err := s.Chat.DeleteRoomMessage(r.Context(), roomID, seq, userID, time.Now().UnixMilli())
	if err != nil {
		obs.ServerError(w, "delete failed", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	if !deleted {
		// Not the author, or no such message. Do not reveal which.
		http.Error(w, "cannot delete this message", http.StatusForbidden)
		return
	}
	s.SearchUpdates.Enqueue(store.MessageKey{TenantID: tenant.FromContext(r.Context()), RoomID: roomID, Seq: seq})
	// Transient: tell live clients to drop the message. Not persisted (it is not
	// a chat/agent kind), so history simply omits the deleted row.
	s.Hub.Publish(wire.Event{
		RoomID:    roomID,
		ChannelID: channelID,
		Kind:      "message_deleted",
		Payload:   map[string]any{"sequence": seq},
		AtUnixMs:  time.Now().UnixMilli(),
	})
	w.WriteHeader(http.StatusOK)
}

// handleEditMessage rewrites the text of a room message the caller authored,
// then publishes a transient message_edited event so live clients patch it. The
// author check and delete guard are enforced in the store; a non-author,
// missing, or deleted message yields 403 and nothing is broadcast.
func (s *Service) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if _, ok := s.member(w, r, roomID, userID, membership.AnyMember); !ok {
		return
	}
	seq := parseInt(r.PathValue("seq"))
	if seq <= 0 || s.Chat == nil {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty message", http.StatusBadRequest)
		return
	}
	if len(body.Text) > maxChatTextBytes {
		http.Error(w, "message too long", http.StatusRequestEntityTooLarge)
		return
	}
	// The live event is scoped to the message's channel, read before the
	// write so a lookup failure leaves the message unchanged.
	channelID, _, err := s.messageChannel(r.Context(), roomID, seq)
	if err != nil {
		obs.ServerError(w, "edit failed", err)
		http.Error(w, "edit failed", http.StatusInternalServerError)
		return
	}
	now := time.Now().UnixMilli()
	edited, err := s.Chat.EditRoomMessage(r.Context(), roomID, seq, userID, text, now)
	if err != nil {
		obs.ServerError(w, "edit failed", err)
		http.Error(w, "edit failed", http.StatusInternalServerError)
		return
	}
	if !edited {
		// Not the author, no such message, or already deleted. Do not reveal which.
		http.Error(w, "cannot edit this message", http.StatusForbidden)
		return
	}
	s.SearchUpdates.Enqueue(store.MessageKey{TenantID: tenant.FromContext(r.Context()), RoomID: roomID, Seq: seq})
	// Transient: tell live clients to patch the message text. Not persisted (it
	// is not a chat/agent kind); history reads the updated row directly.
	// Scoped to the message's channel: the edited text must not reach a stream
	// that may not see that channel (the people chat).
	s.Hub.Publish(wire.Event{
		RoomID:    roomID,
		ChannelID: channelID,
		Kind:      "message_edited",
		Payload:   map[string]any{"sequence": seq, "text": text, "editedAtMs": now},
		AtUnixMs:  now,
	})
	w.WriteHeader(http.StatusOK)
}

// handleUnread returns per-room unread counts for the caller. Rooms are passed
// as a comma-separated "rooms" query param (the client knows its room list), so
// this needs no server-side membership enumeration; each room is still
// membership-checked. Shape: {"unread": {"<roomId>": <count>}, "channels":
// {"<roomId>": [{"channelId": ..., "count": ...}]}} — the room total spans
// every stream that can hold the caller's messages (the legacy empty channel
// plus each live channel), and the per-channel list carries only channels with
// unread messages, default channel first.
func (s *Service) handleUnread(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var rooms []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(r.URL.Query().Get("rooms"), ",") {
		roomID, err := roomctx.Normalize(strings.TrimSpace(raw))
		if err != nil || roomID == "" || seen[roomID] {
			continue
		}
		seen[roomID] = true
		rooms = append(rooms, roomID)
	}
	if len(rooms) > maxUnreadRooms {
		http.Error(w, fmt.Sprintf("too many rooms (max %d per request)", maxUnreadRooms), http.StatusBadRequest)
		return
	}
	out := map[string]int{}
	perChannel := map[string][]map[string]any{}
	if s.Chat != nil && s.Membership != nil {
		for _, roomID := range rooms {
			// A read across rooms the caller names: each room is read in its own
			// tenant (the membership probe answers it), so a member of rooms in
			// several organizations gets every room's count whatever tenant the
			// request is signed for.
			m, member := s.Membership.Lookup(roomID, userID)
			if !member {
				continue
			}
			ctx := r.Context()
			if m.Tenant != "" {
				ctx = tenant.NewContext(ctx, m.Tenant)
			}
			total := 0
			// The legacy empty channel holds rows written before channels existed.
			// They count toward the room total, but they must also be attributed to
			// a channel the member can actually open: a count that belongs to no
			// channel shows a room badge nobody can locate or clear, because every
			// channel view reads its own id and never the empty bucket.
			legacy := 0
			if n, err := s.Chat.UnreadCount(ctx, roomID, "", userID); err == nil {
				legacy = n
				total += n
			}
			if s.Channels != nil {
				defaultID := ""
				if def, ok := s.Channels.DefaultChannel(ctx, roomID); ok {
					defaultID = def.ChannelID
				}
				for _, ch := range s.Channels.ListChannels(ctx, roomID) {
					n, err := s.Chat.UnreadCount(ctx, roomID, ch.ChannelID, userID)
					if err != nil {
						continue
					}
					total += n
					if ch.ChannelID == defaultID {
						n += legacy // pre-channel history reads as the default channel
					}
					if n > 0 {
						perChannel[roomID] = append(perChannel[roomID], map[string]any{
							"channelId": ch.ChannelID,
							"count":     n,
						})
					}
				}
			}
			if total > 0 {
				out[roomID] = total
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"unread": out, "channels": perChannel})
}

// handleTyping publishes a transient "presence_typing" event to the room so
// other members can show a "<user> is typing…" hint. It is ephemeral: not
// persisted and not part of history (persist() only stores chat/agent
// messages). Body {"channelId": <optional>} scopes the hint to one channel
// (the room's default when omitted); an empty body keeps the legacy behavior.
func (s *Service) handleTyping(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if _, ok := s.member(w, r, roomID, userID, membership.Chat); !ok {
		return
	}
	var body struct {
		// ChannelId scopes the hint to one channel; omitted or empty means the
		// room's default channel.
		ChannelId string `json:"channelId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)
	// An empty body (legacy clients) is fine; a malformed or oversized one is not.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	channelID, err := s.resolveChannel(r.Context(), roomID, body.ChannelId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.Hub.Publish(wire.Event{
		RoomID:    roomID,
		ChannelID: channelID,
		Kind:      "presence_typing",
		Payload:   map[string]any{"userId": userID},
		AtUnixMs:  time.Now().UnixMilli(),
	})
	w.WriteHeader(http.StatusOK)
}
