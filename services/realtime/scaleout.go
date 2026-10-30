package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Scale-out by key ownership (docs/prd/2026-10-10-enterprise-scale.md §2).
//
// Every piece of realtime's in-process state is owned by exactly one replica
// and lives only there:
//
//   - a room (key "room:<id>"): its hub subscribers, per-room durable
//     sequence counter, replay buffer, presence refcounts and leave timers,
//     the agent roster and its withdraw timers, and the room's publish lock;
//   - a member (key "member:<id>"): their desktop plugin event streams and the
//     pending-ack table of deliveries sent on them.
//
// Requests are routed to the owner by shard.Forward (RouteKey below); callers
// keep calling the load-balanced Service. Work produced on a replica for a
// key it does not own crosses over an internal peer route: a room event's
// plugin deliveries go to every peer (each fans out to the member streams it
// owns), a notification goes to the member's owner, and a scheduled post is
// made by the room's owner. Store-backed reads (unread, search, prompts,
// history pages, blobs) need no owner; they are served anywhere, as are the
// background workers that already claim with leases (notification outbox,
// scheduler claims).

// RoomKey and MemberKey are the ownership keys of a room and a member.
func RoomKey(roomID string) string     { return "room:" + roomID }
func MemberKey(memberID string) string { return "member:" + memberID }

// RouteKey maps a request to its ownership key: room-scoped routes
// (/v1/rooms/{roomId}/…, /internal/v1/rooms/{roomId}/…) to the room, the
// member plugin stream and its ack to the authenticated member, and the
// internal member routes to the addressed member. Everything else is served
// where it lands.
func RouteKey(r *http.Request) (string, bool) {
	path := r.URL.EscapedPath()
	for _, prefix := range []string{"/v1/rooms/", "/internal/v1/rooms/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			if id, ok := firstSegment(rest); ok {
				if room, err := roomctx.Normalize(id); err == nil {
					return RoomKey(room), true
				}
			}
			return "", false
		}
	}
	if path == "/v1/my/plugin-events" || strings.HasPrefix(path, "/v1/my/plugin-events/") {
		if member := svcauth.Subject(r); member != "" {
			return MemberKey(member), true
		}
		return "", false
	}
	if rest, ok := strings.CutPrefix(path, "/internal/v1/members/"); ok {
		if member, ok := firstSegment(rest); ok && strings.TrimSpace(member) != "" {
			return MemberKey(member), true
		}
	}
	return "", false
}

func firstSegment(escaped string) (string, bool) {
	seg, _, _ := strings.Cut(escaped, "/")
	v, err := url.PathUnescape(seg)
	return v, err == nil && v != ""
}

// Peers is realtime's view of its replica set plus the signed client it uses
// for internal peer calls. A nil *Peers is a single node: every key is owned
// here and nothing crosses replicas.
type Peers struct {
	set    *shard.Set
	key    string
	client *http.Client

	mu     sync.Mutex
	queues map[string]chan peerPluginEvent

	// hints holds one components_changed hand-off slot per peer: at most one
	// call in flight per peer and at most one more coalesced behind it.
	hintMu sync.Mutex
	hints  map[string]*peerHintSlot
}

// peerHintSlot is one peer's components_changed hand-off: busy while a call
// is in flight, pending the (merged) hint to send after it.
type peerHintSlot struct {
	busy    bool
	pending *wire.ComponentsChanged
}

// peerPluginEvent is one room event handed to a peer for plugin fan-out.
type peerPluginEvent struct {
	tenantID string
	ev       wire.Event
}

// Peer call bounds.
const (
	// peerCallTimeout bounds a short internal peer call.
	peerCallTimeout = 5 * time.Second
	// peerDesktopProbeTimeout bounds the notification worker's "does this
	// member hold a desktop stream" question; a miss falls back to the
	// server notifiers (durable), so it must be quick.
	peerDesktopProbeTimeout = 2 * time.Second
	// peerNotificationTimeout outlasts the member owner's own outcome wait:
	// the dispatcher reports within the ack timeout plus one expiry sweep.
	peerNotificationTimeout = pendingAckTimeout + pendingSweepInterval + 15*time.Second
	// peerPluginQueueSize bounds one peer's plugin hand-off queue (the same
	// bound as the local fan-out queue); a full queue drops, like locally.
	peerPluginQueueSize = pluginQueueSize
)

// NewPeers wraps a started-or-not shard set (nil = single node → nil).
func NewPeers(set *shard.Set) *Peers {
	if set == nil {
		return nil
	}
	return &Peers{set: set, key: svcauth.KeyFromEnv(), client: set.Client(), queues: map[string]chan peerPluginEvent{}, hints: map[string]*peerHintSlot{}}
}

// Set is the underlying shard set (nil for a single node).
func (p *Peers) Set() *shard.Set {
	if p == nil {
		return nil
	}
	return p.set
}

// OwnsRoom / OwnsMember report whether this replica owns the key.
func (p *Peers) OwnsRoom(roomID string) bool     { return p.roomOwner(roomID) == "" }
func (p *Peers) OwnsMember(memberID string) bool { return p.memberOwner(memberID) == "" }

func (p *Peers) roomOwner(roomID string) string {
	if p == nil {
		return ""
	}
	return p.set.Owner(RoomKey(roomID))
}

func (p *Peers) memberOwner(memberID string) string {
	if p == nil {
		return ""
	}
	return p.set.Owner(MemberKey(memberID))
}

// call makes one signed internal call to peer as the realtime service in
// tenantID. in (when non-nil) is sent as JSON; the response status and body
// are returned. The request is marked as already routed by ownership, so the
// peer serves it locally.
func (p *Peers) call(ctx context.Context, method, peer, path, tenantID string, in any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(buf)
	}
	req, err := p.set.PeerRequest(ctx, method, peer, path, body)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	svcauth.Key(p.key).Assert(req, realtimeServiceSubject, tenantID, "")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, out, err
}

// forwardPluginEvent hands a room event that bears a plugin hook to every
// other live peer, each of which fans it out to the member streams it owns.
// Like the local Deliver it never blocks the publish path: each peer has a
// bounded queue drained by one worker (publish order per peer), and a full
// queue drops the event (logged).
func (p *Peers) forwardPluginEvent(tenantID string, ev wire.Event) {
	if p == nil {
		return
	}
	self := p.set.Self()
	p.mu.Lock() // held across the non-blocking sends: prune never closes a queue mid-send
	defer p.mu.Unlock()
	for _, peer := range p.set.Peers() {
		if peer == self {
			continue
		}
		q := p.queues[peer]
		if q == nil {
			q = make(chan peerPluginEvent, peerPluginQueueSize)
			p.queues[peer] = q
			go p.drain(peer, q)
		}
		select {
		case q <- peerPluginEvent{tenantID: tenantID, ev: ev}:
		default:
			log.Printf("realtime: plugin hand-off to peer %s full; dropped %s for room=%s", peer, ev.Kind, ev.RoomID)
		}
	}
}

func (p *Peers) drain(peer string, q chan peerPluginEvent) {
	for item := range q {
		ctx, cancel := context.WithTimeout(context.Background(), peerCallTimeout)
		status, body, err := p.call(ctx, http.MethodPost, peer, "/internal/v1/plugin-events", item.tenantID, item.ev)
		cancel()
		if err != nil || status/100 != 2 {
			log.Printf("realtime: plugin hand-off to peer %s for room=%s failed: status=%d err=%v %s", peer, item.ev.RoomID, status, err, bytes.TrimSpace(body))
		}
	}
}

// forwardComponentsChanged hands a catalog hint to every other live peer,
// each of which queues it on the member streams it owns. Never blocks: each
// peer has one call in flight at most, and hints arriving meanwhile merge
// into one follow-up (a hint is "re-read your catalog", so merging widens
// its scope instead of queueing; over-notifying only costs a refresh).
func (p *Peers) forwardComponentsChanged(c wire.ComponentsChanged) {
	if p == nil {
		return
	}
	self := p.set.Self()
	for _, peer := range p.set.Peers() {
		if peer == self {
			continue
		}
		p.hintMu.Lock()
		slot := p.hints[peer]
		if slot == nil {
			slot = &peerHintSlot{}
			p.hints[peer] = slot
		}
		if slot.busy {
			slot.pending = mergeHints(slot.pending, c)
			p.hintMu.Unlock()
			continue
		}
		slot.busy = true
		p.hintMu.Unlock()
		go p.sendHints(peer, slot, c)
	}
}

// sendHints delivers c to peer, then whatever merged behind it.
func (p *Peers) sendHints(peer string, slot *peerHintSlot, c wire.ComponentsChanged) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), peerCallTimeout)
		status, body, err := p.call(ctx, http.MethodPost, peer, "/internal/v1/components/changed", c.TenantID, c)
		cancel()
		if err != nil || status/100 != 2 {
			log.Printf("realtime: components_changed hand-off to peer %s failed: status=%d err=%v %s", peer, status, err, bytes.TrimSpace(body))
		}
		p.hintMu.Lock()
		if slot.pending == nil {
			slot.busy = false
			p.hintMu.Unlock()
			return
		}
		c = *slot.pending
		slot.pending = nil
		p.hintMu.Unlock()
	}
}

// mergeHints widens a pending hint to cover c too: a differing tenant or
// member becomes "every".
func mergeHints(pending *wire.ComponentsChanged, c wire.ComponentsChanged) *wire.ComponentsChanged {
	if pending == nil {
		return &c
	}
	m := *pending
	if m.TenantID != c.TenantID {
		m.TenantID = ""
	}
	if m.Member != c.Member {
		m.Member = ""
	}
	m.AtMs = max(m.AtMs, c.AtMs)
	return &m
}

// prune stops the hand-off workers of peers that left the live view (pod IPs
// change on every restart, so the set of addresses ever seen is unbounded).
func (p *Peers) prune() {
	if p == nil {
		return
	}
	live := map[string]bool{}
	for _, peer := range p.set.Peers() {
		live[peer] = true
	}
	p.mu.Lock()
	for peer, q := range p.queues {
		if !live[peer] {
			close(q)
			delete(p.queues, peer)
		}
	}
	p.mu.Unlock()
	p.hintMu.Lock()
	for peer, slot := range p.hints {
		if !live[peer] && !slot.busy {
			delete(p.hints, peer)
		}
	}
	p.hintMu.Unlock()
}

// reconcileOwnership releases the in-process state of every key this replica
// does not own (after each peer-set refresh): room streams are closed (the
// clients reconnect to the new owner and catch up from history) and the
// rooms' counters and replay buffers forgotten; member plugin streams are
// closed (the desktop reconnects to the member's new owner). Presence and the
// roster need no separate step: the closed streams' deferred Leave/Withdraw
// run here, and their (owner-gated) events are never published by a
// non-owner.
func (s *Service) reconcileOwnership(bool) {
	rooms := s.Hub.ReleaseRooms(s.Peers.OwnsRoom)
	if s.roomState != nil {
		s.roomState.release(s.Peers.OwnsRoom)
	}
	members := s.PluginHooks.closeStreams(s.Peers.OwnsMember)
	s.Peers.prune()
	if rooms > 0 || members > 0 {
		log.Printf("realtime: ownership moved: closed %d room stream(s) and %d plugin stream(s) for keys now owned by another replica", rooms, members)
	}
}

// publishOwned is the publish callback of presence and the roster: their
// events are published only by the room's owner. A non-owner still tracks
// the refcounts of streams it serves (a forwarded request while the views
// disagree, or a stream about to be released) so its timers settle, but it
// never announces them into a room it does not own.
func (s *Service) publishOwned(ev wire.Event) {
	if s.Peers.OwnsRoom(ev.RoomID) {
		s.Hub.Publish(ev)
	}
}

// ---------------------------------------------------------------- internal peer routes

// realtimeServiceSubject is the identity a replica asserts on its calls to
// a peer.
var realtimeServiceSubject = svcauth.ServiceSubject("realtime")

func (s *Service) registerPeerRoutes(mux *http.ServeMux, peers, catalog func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /internal/v1/plugin-events", peers(s.handleInternalPluginEvent))
	mux.Handle("GET /internal/v1/members/{member}/desktop", peers(s.handleInternalMemberDesktop))
	mux.Handle("POST /internal/v1/members/{member}/notifications", peers(s.handleInternalMemberNotification))
	mux.Handle("POST /internal/v1/rooms/{roomId}/schedules/{scheduleId}/post", peers(s.handleInternalScheduledPost))
	mux.Handle("POST /internal/v1/components/changed", catalog(s.handleInternalComponentsChanged))
}

// handleInternalComponentsChanged queues a catalog hint
// (wire.ComponentsChanged) on the member streams this replica holds. The
// gateway calls it after a change of the executable component catalog; a
// call that did not come from a peer (no shard.ForwardedHeader) is handed
// on to every other replica, which serves it locally and never re-forwards.
func (s *Service) handleInternalComponentsChanged(w http.ResponseWriter, r *http.Request) {
	var c wire.ComponentsChanged
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &c) {
		return
	}
	c.TenantID, c.Member = strings.TrimSpace(c.TenantID), strings.TrimSpace(c.Member)
	if c.AtMs <= 0 {
		c.AtMs = time.Now().UnixMilli()
	}
	s.PluginHooks.componentsChanged(c)
	if r.Header.Get(shard.ForwardedHeader) == "" {
		s.Peers.forwardComponentsChanged(c)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInternalPluginEvent fans a room event its owner published out to the
// plugin streams of the members this replica owns (the room owner hands
// every hook-bearing event to every peer). The event's tenant is the signed
// tenant; the people-chat guard already ran on the room owner.
func (s *Service) handleInternalPluginEvent(w http.ResponseWriter, r *http.Request) {
	var ev wire.Event
	if !httpx.DecodeJSON(w, r, maxInternalPublishBytes, &ev) {
		return
	}
	if ev.RoomID == "" {
		httpx.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	s.PluginHooks.Deliver(r.Context(), s.Membership, ev)
	w.WriteHeader(http.StatusNoContent)
}

// handleInternalMemberDesktop answers, for the member's owner, whether the
// member holds a desktop plugin stream in the signed tenant.
func (s *Service) handleInternalMemberDesktop(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"desktop": s.PluginHooks.HasDesktop(tenant.FromContext(r.Context()), r.PathValue("member"))})
}

// handleInternalMemberNotification delivers one member's notification to
// their desktop plugin streams (held here, the member's owner) and answers
// the outcome once known: delivered by a plugin, or not.
func (s *Service) handleInternalMemberNotification(w http.ResponseWriter, r *http.Request) {
	var n wire.PluginNotification
	if !httpx.DecodeJSON(w, r, maxInternalPublishBytes, &n) {
		return
	}
	if n.UserID != r.PathValue("member") {
		httpx.Error(w, "notification is for another member", http.StatusBadRequest)
		return
	}
	outcome := make(chan bool, 1)
	s.PluginHooks.DeliverNotification(tenant.FromContext(r.Context()), n, func(delivered bool) { outcome <- delivered })
	select {
	case delivered := <-outcome:
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"delivered": delivered})
	case <-r.Context().Done():
		// The caller gave up (it falls back on its own); the outcome still
		// lands in the buffered channel and is dropped.
	}
}

// Scheduled-post outcome codes: the scheduler pauses a schedule on these
// (exactly as it does for the local errors) and retries anything else.
const (
	scheduledPostUnknownChannel = "unknown_channel"
	scheduledPostEmpty          = "empty_message"
	scheduledPostTooLong        = "message_too_long"
	scheduledPostNotPermitted   = "not_permitted"
	scheduledPostNoAgent        = "waits_for_agent"
)

var scheduledPostErrs = map[string]error{
	scheduledPostUnknownChannel: errUnknownChannel,
	scheduledPostEmpty:          errEmptyMessage,
	scheduledPostTooLong:        errMessageTooLong,
	scheduledPostNoAgent:        errWaitsForAgent,
	// Every membership refusal of the creator (membership.IsRefusal).
	scheduledPostNotPermitted: membership.ErrMayNotChat,
}

// handleInternalScheduledPost makes a claimed schedule's post on the room's
// owner: the scheduler claimed the slot on whichever replica ran the tick,
// but only the room owner may sequence and fan out the room's messages. The
// schedule is re-read from the store (the caller holds the claim); the post
// is the creator's, through PostChat, exactly as locally.
func (s *Service) handleInternalScheduledPost(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil || s.Schedules == nil {
		httpx.Error(w, "invalid schedule", http.StatusBadRequest)
		return
	}
	var body struct {
		SlotMs int64 `json:"slotMs"`
	}
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &body) {
		return
	}
	if body.SlotMs <= 0 {
		httpx.Error(w, "slotMs required", http.StatusBadRequest)
		return
	}
	sc, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	seq, err := s.postScheduled(r.Context(), sc, time.UnixMilli(body.SlotMs))
	if err != nil {
		if membership.IsRefusal(err) {
			httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": scheduledPostNotPermitted, "error": err.Error()})
			return
		}
		for code, sentinel := range scheduledPostErrs {
			if errors.Is(err, sentinel) {
				httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": code, "error": err.Error()})
				return
			}
		}
		obs.ServerError(w, "scheduled post failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"sequence": seq})
}

// postScheduledAtOwner posts a claimed slot on the room's owner: locally when
// this replica owns the room, else over the owner's internal route, with the
// owner's classified errors mapped back so the scheduler pauses or retries
// exactly as for a local post.
func (s *Service) postScheduledAtOwner(ctx context.Context, sc wire.RoomSchedule, slot time.Time) (int64, error) {
	owner := s.Peers.roomOwner(sc.RoomID)
	if owner == "" {
		return s.postScheduled(ctx, sc, slot)
	}
	cctx, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()
	path := "/internal/v1/rooms/" + url.PathEscape(sc.RoomID) + "/schedules/" + url.PathEscape(sc.ScheduleID) + "/post"
	status, body, err := s.Peers.call(cctx, http.MethodPost, owner, path, sc.TenantID, map[string]int64{"slotMs": slot.UnixMilli()})
	if err != nil {
		return 0, fmt.Errorf("room owner %s: %w", owner, err)
	}
	var out struct {
		Sequence int64  `json:"sequence"`
		Code     string `json:"code"`
		Error    string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	switch {
	case status == http.StatusOK:
		return out.Sequence, nil
	case status == http.StatusUnprocessableEntity && scheduledPostErrs[out.Code] != nil:
		return 0, scheduledPostErrs[out.Code]
	default:
		return 0, fmt.Errorf("room owner %s: status %d: %s", owner, status, strings.TrimSpace(firstNonEmpty(out.Error, string(body))))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---------------------------------------------------------------- member desktop route

// MemberDesktopRoute is the notification worker's DesktopRoute. The outbox
// worker runs on every replica (lease claims), but a member's desktop plugin
// streams live only on the member's owner, so a notification for a member
// owned elsewhere is answered and delivered there. A single node (nil peers)
// is the local dispatcher itself.
func MemberDesktopRoute(local *PluginHookDispatcher, peers *Peers) notify.DesktopRoute {
	if peers == nil {
		return local
	}
	return &memberDesktop{local: local, peers: peers}
}

type memberDesktop struct {
	local *PluginHookDispatcher
	peers *Peers
}

// HasDesktop asks the member's owner (bounded); an unreachable owner answers
// false, which routes the member to the durable server notifiers.
func (m *memberDesktop) HasDesktop(tenantID, member string) bool {
	owner := m.peers.memberOwner(member)
	if owner == "" {
		return m.local.HasDesktop(tenantID, member)
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerDesktopProbeTimeout)
	defer cancel()
	status, body, err := m.peers.call(ctx, http.MethodGet, owner, "/internal/v1/members/"+url.PathEscape(member)+"/desktop", tenantID, nil)
	var out struct {
		Desktop bool `json:"desktop"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &out) != nil {
		log.Printf("realtime: desktop probe for member=%s at owner %s failed (server notifiers will deliver): status=%d err=%v", member, owner, status, err)
		return false
	}
	return out.Desktop
}

// DeliverNotification hands the notification to the member's owner without
// blocking; done reports the owner's outcome, or false when it cannot be
// reached (the worker then queues the server fallback).
func (m *memberDesktop) DeliverNotification(tenantID string, n wire.PluginNotification, done func(delivered bool)) {
	owner := m.peers.memberOwner(n.UserID)
	if owner == "" {
		m.local.DeliverNotification(tenantID, n, done)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), peerNotificationTimeout)
		defer cancel()
		status, body, err := m.peers.call(ctx, http.MethodPost, owner, "/internal/v1/members/"+url.PathEscape(n.UserID)+"/notifications", tenantID, n)
		var out struct {
			Delivered bool `json:"delivered"`
		}
		if err != nil || status != http.StatusOK || json.Unmarshal(body, &out) != nil {
			log.Printf("realtime: notification hand-off for member=%s to owner %s failed: status=%d err=%v", n.UserID, owner, status, err)
			done(false)
			return
		}
		done(out.Delivered)
	}()
}
