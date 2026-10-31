package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The member stream (GET /v1/my/events,
// docs/prd/2026-10-30-replicas-without-owners.md §9): a web tab's one
// stream. It opens with stream_opened; the client scopes it (POST
// /v1/my/events/scope) with the room it shows in full, and scopes it again
// when that changes, never reopening it.
//
//   - Rooms: the member's rooms in the stream's tenant, read by the stream
//     itself from the room directory when it opens and after a gap, and
//     followed: a join reaches it on the member's own key (member_rooms,
//     announced when bnbloc-room publishes member_joined), a removal or a
//     deletion on the room. A change is told to the client (member_rooms),
//     which reads its room list again. The client never names rooms, so
//     there is no list to check and no bound on it.
//   - Unread: the rooms' state (unread_state) when the stream opens, when
//     its rooms change, after a gap and when asked (a new watcher), then
//     at most once an unreadFlush what changed (unread_delta): new
//     messages that count by unreadFor's rule, deletions, a mark of read on
//     any device.
//   - The open room: what a person's view of the room shows (roomView, the
//     same rules as a room's own stream): its replay after the client's
//     sequence, the presence snapshot, full events of the open channels,
//     channel_activity for the others; presence counted while it is open.
//     Access to it ending is told as the revocation event; the stream goes
//     on.
//
// It is one hub subscriber on every room it carries and the member's own
// key (Hub.SubscribeRooms / SetRooms), so it runs on any replica.

const (
	unreadFlush = time.Second
	// memberStreamBuffer is the stream's buffer: one subscriber for every
	// room it carries.
	memberStreamBuffer = 512
	// scopeTimeout bounds a scope request's wait for the stream to take it.
	scopeTimeout = 5 * time.Second
)

// activityRoom is one room whose unread the stream counts.
type activityRoom struct {
	ctx    context.Context
	access *channelAccess
	// follows are the threads the member follows (nil: not read yet).
	follows map[string]bool
}

// memberStreams are the member streams this replica serves, by id.
type memberStreams struct {
	mu   sync.Mutex
	byID map[string]*memberStream
}

type memberStream struct {
	id, userID string
	scopes     chan scopeRequest
}

type scopeRequest struct {
	streamScope
	done chan error
}

// streamScope is a scope request checked by the replica that took it (it
// may be another than the stream's, which it reaches over the bus).
type streamScope struct {
	Open  *openRoom `json:"open,omitempty"`
	State bool      `json:"state,omitempty"`
}

// scopedRoom is a room the stream carries: the member's, in its tenant.
type scopedRoom struct {
	ID string            `json:"id"`
	M  membership.Member `json:"m"`
}

// openRoom is the room to show, checked when it was scoped (an empty
// RoomID: none).
type openRoom struct {
	RoomID   string            `json:"roomId,omitempty"`
	M        membership.Member `json:"m"`
	Channels []string          `json:"channels,omitempty"`
	Since    int64             `json:"since,omitempty"`
}

func (ms *memberStreams) add(st *memberStream) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.byID == nil {
		ms.byID = map[string]*memberStream{}
	}
	ms.byID[st.id] = st
}

func (ms *memberStreams) remove(id string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	delete(ms.byID, id)
}

func (ms *memberStreams) get(id string) *memberStream {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.byID[id]
}

var errNoStream = errors.New("no such stream")

func (s *Service) handleMyEvents(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireSubject(w, r)
	if !ok {
		return
	}
	flusher, isFlusher := w.(http.Flusher)
	if !isFlusher {
		obs.ServerError(w, "streaming unsupported", nil)
		httpx.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	// The member's key first: a join announced while the rooms are read is
	// not missed (the rooms then include it, and the join adds nothing).
	memberKey := wire.MemberActivityKey(userID)
	sub := s.Hub.SubscribeRooms([]string{memberKey}, memberStreamBuffer)
	defer s.Hub.Unsubscribe(memberKey, sub)
	rooms, err := s.memberRooms(ctx, userID)
	if err != nil {
		obs.ServerError(w, "room directory unavailable", err)
		httpx.Error(w, "room directory unavailable", http.StatusServiceUnavailable)
		return
	}
	st := &memberStream{id: s.Replicas.streamPrefix() + ids.New("ms_"), userID: userID, scopes: make(chan scopeRequest)}
	s.memberStreams.add(st)
	defer s.memberStreams.remove(st.id)
	defer realtime.TrackSSE(s.Hub.Metrics, realtime.SSEKindMember)()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(ev wire.Event) {
		data := ev.Encoded
		if data == nil {
			var err error
			if data, err = json.Marshal(ev); err != nil {
				return
			}
		}
		if ev.Sequence > 0 {
			fmt.Fprintf(w, "id: %d\n", ev.Sequence)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data)
	}
	tz := r.URL.Query().Get("tz")
	now := func() int64 { return time.Now().UnixMilli() }
	m := &memberStreamState{s: s, ctx: ctx, userID: userID, tz: tz, send: send, rooms: map[string]*activityRoom{}, delta: map[string]*wire.RoomUnreadDelta{}}
	defer m.closeOpen()
	m.setRooms(rooms)
	keys := func() []string {
		out := []string{memberKey}
		for id := range m.rooms {
			out = append(out, id)
		}
		if m.open != nil && m.rooms[m.open.roomID] == nil {
			out = append(out, m.open.roomID)
		}
		return out
	}

	if !s.Hub.SetRooms(sub, keys()) {
		return
	}
	send(wire.Event{Kind: wire.EventStreamOpened, AtUnixMs: now(), Payload: wire.StreamOpenedPayload{StreamID: st.id}})
	m.sendState()
	flusher.Flush()
	// roomsChanged applies what the events of the rooms changed of them
	// (joins, removals): the subscription follows, the client is told, the
	// unread state is sent again.
	roomsChanged := func() bool {
		if !m.applyRooms() {
			return true
		}
		return s.Hub.SetRooms(sub, keys())
	}

	unreadTick := time.NewTicker(unreadFlush)
	defer unreadTick.Stop()
	laneTick := time.NewTicker(activityEvery)
	defer laneTick.Stop()
	heartbeat := time.NewTicker(s.heartbeatEvery())
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-obs.Draining():
			return
		case req := <-st.scopes:
			var err error
			replay := false
			switch {
			case req.Open == nil:
			case m.open != nil && req.Open.RoomID == m.open.roomID:
				// Another channel of the same room: the view follows it, no
				// replay (a channel's events folded into activity until now
				// are read from history by the client).
				m.open.view.lane.setOpen(channelSet(req.Open.Channels))
			default:
				m.closeOpen()
				if req.Open.RoomID != "" {
					err = m.openRoom(*req.Open)
					replay = err == nil
				}
			}
			if !s.Hub.SetRooms(sub, keys()) {
				req.done <- errNoStream
				return
			}
			if req.State {
				m.sendState()
			}
			if replay {
				m.replayOpen(req.Open.Since)
			}
			req.done <- err
			flusher.Flush()
		case <-unreadTick.C:
			if m.flushDelta() {
				flusher.Flush()
			}
		case now := <-laneTick.C:
			if m.open != nil && m.open.view.tick(now, send) {
				flusher.Flush()
			}
		case <-heartbeat.C:
			// A revocation may never reach this replica (a failed ingest):
			// the open room's access is checked again, and its end told.
			if m.open != nil {
				if _, err := membership.RoomScope(m.open.view.ctx, s.Membership, m.open.roomID, userID, m.open.recheck); err != nil {
					m.endOpen()
				}
			}
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-sub.Events():
			if !open {
				return // fell behind twice: the client reopens and starts again
			}
			m.event(ev)
			for n := len(sub.Events()); n > 0 && open; n-- {
				if ev, open = <-sub.Events(); open {
					m.event(ev)
				}
			}
			if open && !roomsChanged() {
				return
			}
			if open && sub.TakeGap() {
				// Something may have been missed (a join or a removal too):
				// the rooms are read again, the unread starts again from
				// the state, the open room from history.
				if rooms, err := s.memberRooms(ctx, userID); err == nil {
					m.replaceRooms(rooms)
					if !roomsChanged() {
						return
					}
				}
				m.delta = map[string]*wire.RoomUnreadDelta{}
				m.sendState()
				if m.open != nil {
					send(wire.Event{RoomID: m.open.roomID, Kind: wire.EventStreamGap, AtUnixMs: now(), Payload: wire.StreamGapPayload{AfterSeq: m.open.lastSeq}})
				}
			}
			flusher.Flush()
			if !open {
				return
			}
		}
	}
}

// memberStreamState is a member stream's state, owned by its goroutine.
type memberStreamState struct {
	s      *Service
	ctx    context.Context
	userID string
	tz     string
	send   func(wire.Event)
	rooms  map[string]*activityRoom
	delta  map[string]*wire.RoomUnreadDelta
	open   *openView
	// joined and left are the room changes the stream's events brought
	// since applyRooms last ran; joinedChecked says the joins were read
	// from the directory (replaceRooms), not announced (to be checked).
	joined, left  []string
	joinedChecked bool
}

// openView is the open room as the stream shows it.
type openView struct {
	roomID  string
	view    *roomView
	recheck membership.Need
	// lastSeq is the last durable sequence sent of the room: the replay and
	// the live events overlap, and a repeat is not sent twice.
	lastSeq int64
	leave   func()
}

// setRooms makes rooms the ones the stream carries.
func (m *memberStreamState) setRooms(rooms []scopedRoom) {
	next := make(map[string]*activityRoom, len(rooms))
	for _, r := range rooms {
		if cur := m.rooms[r.ID]; cur != nil {
			next[r.ID] = cur
			continue
		}
		next[r.ID] = m.newRoom(r)
	}
	m.rooms = next
	for id := range m.delta {
		if next[id] == nil {
			delete(m.delta, id)
		}
	}
}

func (m *memberStreamState) newRoom(r scopedRoom) *activityRoom {
	ctx := m.ctx
	if r.M.Tenant != "" {
		ctx = tenant.NewContext(ctx, r.M.Tenant)
	}
	return &activityRoom{ctx: ctx, access: &channelAccess{svc: m.s, ctx: ctx, roomID: r.ID, reader: readerOf(m.userID, r.M)}}
}

// replaceRooms makes rooms (read again from the directory) the ones the
// stream carries, noting the difference as joins and departures for
// applyRooms to tell.
func (m *memberStreamState) replaceRooms(rooms []scopedRoom) {
	next := make(map[string]bool, len(rooms))
	for _, r := range rooms {
		next[r.ID] = true
		if m.rooms[r.ID] == nil {
			m.rooms[r.ID] = m.newRoom(r)
			m.joined = append(m.joined, r.ID)
		}
	}
	for id := range m.rooms {
		if !next[id] {
			m.leave(id)
		}
	}
	// Joins found here are checked already.
	m.joinedChecked = true
}

// leave stops carrying a room the member is no longer in.
func (m *memberStreamState) leave(roomID string) {
	if m.rooms[roomID] == nil {
		return
	}
	delete(m.rooms, roomID)
	delete(m.delta, roomID)
	m.left = append(m.left, roomID)
}

// applyRooms checks the rooms joined since it last ran (the member's rooms
// in the stream's tenant only), and, when the rooms changed, tells the
// client (member_rooms) and sends the unread state again. It reports
// whether they changed.
func (m *memberStreamState) applyRooms() bool {
	if len(m.joined) == 0 && len(m.left) == 0 {
		return false
	}
	joined := m.joined
	if !m.joinedChecked {
		joined = nil
		var ask []string
		for _, id := range m.joined {
			if m.rooms[id] == nil && !slices.Contains(ask, id) {
				ask = append(ask, id)
			}
		}
		for _, r := range m.s.carriedRooms(m.ctx, ask, m.userID) {
			m.rooms[r.ID] = m.newRoom(r)
			joined = append(joined, r.ID)
		}
	}
	left := m.left
	m.joined, m.left, m.joinedChecked = nil, nil, false
	if len(joined) == 0 && len(left) == 0 {
		return false
	}
	m.send(wire.Event{Kind: wire.EventMemberRooms, AtUnixMs: time.Now().UnixMilli(), Payload: wire.MemberRoomsPayload{Joined: joined, Left: left}})
	m.sendState()
	return true
}

func (m *memberStreamState) sendState() {
	ids := make([]string, 0, len(m.rooms))
	for id := range m.rooms {
		ids = append(ids, id)
	}
	m.send(wire.Event{Kind: wire.UnreadStateKind, AtUnixMs: time.Now().UnixMilli(), Payload: m.s.unreadFor(m.ctx, m.userID, ids)})
}

func (m *memberStreamState) flushDelta() bool {
	if len(m.delta) == 0 {
		return false
	}
	out := wire.UnreadDeltaPayload{Rooms: make(map[string]wire.RoomUnreadDelta, len(m.delta))}
	for id, d := range m.delta {
		out.Rooms[id] = *d
	}
	m.delta = map[string]*wire.RoomUnreadDelta{}
	m.send(wire.Event{Kind: wire.UnreadDeltaKind, AtUnixMs: time.Now().UnixMilli(), Payload: out})
	return true
}

func (m *memberStreamState) at(roomID string) *wire.RoomUnreadDelta {
	d := m.delta[roomID]
	if d == nil {
		d = &wire.RoomUnreadDelta{}
		m.delta[roomID] = d
	}
	return d
}

// openRoom starts showing a room: a person's lanes view of it, presence
// counted unless it is a preview.
func (m *memberStreamState) openRoom(o openRoom) error {
	ctx := m.ctx
	if o.M.Tenant != "" {
		ctx = tenant.NewContext(ctx, o.M.Tenant)
	}
	readers := &channelAccess{svc: m.s, ctx: ctx, roomID: o.RoomID, reader: readerOf(m.userID, o.M)}
	if err := readers.load(); err != nil {
		return fmt.Errorf("channel access lookup failed: %w", err)
	}
	v := &openView{roomID: o.RoomID, recheck: membership.AnyMember, leave: func() {}}
	if o.M.Preview {
		v.recheck = membership.Read
	}
	v.view = &roomView{s: m.s, ctx: ctx, roomID: o.RoomID, userID: m.userID, m: o.M, human: o.M.IsHuman(),
		allChannels: true, lane: newLaneStream(o.RoomID, m.userID, channelSet(o.Channels)), readers: readers}
	if o.M.IsHuman() && !o.M.Preview {
		m.s.Presence.Enter(o.RoomID, m.userID)
		stopPulse := m.s.pulsePresenceStart(ctx, o.RoomID, m.userID, m.tz)
		roomID, userID := o.RoomID, m.userID
		v.leave = func() {
			stopPulse()
			m.s.Presence.Leave(roomID, userID)
		}
	}
	m.open = v
	return nil
}

// replayOpen sends the open room's replay after since (or a stream_gap when
// the buffer does not reach back) and who is here.
func (m *memberStreamState) replayOpen(since int64) {
	v := m.open
	if v == nil {
		return
	}
	v.lastSeq = since
	events, gap := m.s.Hub.Snapshot(v.roomID, since)
	for _, ev := range events {
		m.openEvent(ev)
		if m.open == nil {
			return
		}
	}
	if !v.view.m.Preview {
		m.send(m.s.Views.Snapshot(v.roomID))
	}
	if gap {
		m.send(wire.Event{RoomID: v.roomID, Kind: wire.EventStreamGap, AtUnixMs: time.Now().UnixMilli(), Payload: wire.StreamGapPayload{AfterSeq: since}})
	}
}

// closeOpen stops showing the open room.
func (m *memberStreamState) closeOpen() {
	if m.open != nil {
		m.open.leave()
		m.open = nil
	}
}

// endOpen tells the client its access to the open room ended (found at the
// re-check) and stops showing it.
func (m *memberStreamState) endOpen() {
	roomID := m.open.roomID
	m.closeOpen()
	m.send(wire.Event{RoomID: roomID, Kind: wire.EventMemberRemoved, AtUnixMs: time.Now().UnixMilli(), Payload: wire.MemberRemovedPayload{UserID: m.userID}})
	m.leave(roomID)
}

// event handles one event of the rooms the stream carries (and of the
// member's own key).
func (m *memberStreamState) event(ev wire.Event) {
	switch ev.Kind {
	case wire.EventMemberRooms:
		if p, ok := wire.DecodePayload[wire.MemberRoomsPayload](ev.Payload); ok && ev.RoomID == wire.MemberActivityKey(m.userID) {
			m.joined = append(m.joined, p.Joined...)
		}
		return
	case wire.EventMemberRemoved:
		if p, ok := wire.DecodePayload[wire.MemberRemovedPayload](ev.Payload); ok && p.UserID == m.userID {
			m.leave(ev.RoomID)
		}
	case wire.EventRoomDeleted:
		m.leave(ev.RoomID)
	}
	m.s.noteActivity(m.userID, m.rooms, ev, m.at)
	if m.open != nil && ev.RoomID == m.open.roomID {
		m.openEvent(ev)
	}
}

// openEvent passes one event of the open room through its view.
func (m *memberStreamState) openEvent(ev wire.Event) {
	v := m.open
	if ev.Sequence > 0 && wire.PersistedFeedKind(ev.Kind) {
		if ev.Sequence <= v.lastSeq {
			return // the replay sent it
		}
		v.lastSeq = ev.Sequence
	}
	if v.view.write(ev, m.send) {
		m.closeOpen()
	}
}

// handleMyEventsScope scopes the caller's member stream. The open room is
// checked here: readable by the caller (a member's, or a preview's).
func (s *Service) handleMyEventsScope(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireSubject(w, r)
	if !ok {
		return
	}
	var body wire.StreamScopeRequest
	if !httpx.DecodeJSON(w, r, 64<<10, &body) {
		return
	}
	sc := streamScope{State: body.State}
	if body.Open != nil {
		o := openRoom{}
		if body.Open.RoomID != "" {
			roomID, err := roomctx.Normalize(body.Open.RoomID)
			if err != nil {
				httpx.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			m, err := membership.FreshRoomScope(r.Context(), s.Membership, roomID, userID, membership.Read)
			if err != nil {
				httpx.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			channels, ok := openSet(body.Open.Channels)
			if !ok {
				httpx.Error(w, fmt.Sprintf("open.channels: at most %d channel ids", wire.MaxStreamOpen), http.StatusBadRequest)
				return
			}
			o = openRoom{RoomID: roomID, M: m, Channels: slices.Sorted(maps.Keys(channels)), Since: max(body.Open.Since, 0)}
		}
		sc.Open = &o
	}
	// The stream may be held by another replica (the load balancer sent
	// this request elsewhere): the checked scope goes to it over the bus.
	err := s.applyScope(body.StreamID, userID, sc)
	if errors.Is(err, errNoStream) && s.memberStreams.get(body.StreamID) == nil && s.Replicas != nil {
		err = s.Replicas.forwardScope(body.StreamID, userID, sc)
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errNoStream):
		// Gone (closed): the client reopens.
		httpx.Error(w, err.Error(), http.StatusNotFound)
	default:
		log.Printf("realtime: member stream scope user=%s: %v", userID, err)
		obs.ServerError(w, "stream scope failed", err)
		httpx.Error(w, "stream scope failed", http.StatusInternalServerError)
	}
}

// announceJoin carries a join to the member's streams on every replica:
// bnbloc-room's member_joined is published to the room, which their streams
// do not carry yet, so it is told again on the member's own key. The
// stream checks it with the authority (carriedRooms).
func (s *Service) announceJoin(ctx context.Context, ev wire.Event) {
	if ev.Kind != wire.EventMemberJoined {
		return
	}
	user := joinedMember(ev)
	if user == "" {
		return
	}
	if _, err := s.publish(ctx, wire.Event{RoomID: wire.MemberActivityKey(user), Kind: wire.EventMemberRooms,
		Payload: wire.MemberRoomsPayload{Joined: []string{ev.RoomID}}, AtUnixMs: ev.AtUnixMs}, realtime.Durable{}); err != nil {
		log.Printf("realtime: room=%s: the join of %s not told to their streams: %v", ev.RoomID, user, err)
	}
}

// memberRooms are the rooms a member stream of ctx's tenant carries, read
// from the authority: the room directory's ids, then their memberships in
// one probe a page.
func (s *Service) memberRooms(ctx context.Context, userID string) ([]scopedRoom, error) {
	if s.Rooms == nil || s.Membership == nil {
		return nil, nil
	}
	ids, err := s.Rooms.MemberRoomIDs(ctx, userID, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return s.carriedRooms(ctx, ids, userID), nil
}

// carriedRooms keeps, of ids, the rooms a member stream of ctx's tenant
// carries: the user's own (human) memberships of rooms of that tenant. A
// join announced on the member's key is checked here too, so nothing but
// the authority decides what a stream carries.
func (s *Service) carriedRooms(ctx context.Context, ids []string, userID string) []scopedRoom {
	if len(ids) == 0 || s.Membership == nil {
		return nil
	}
	tid := tenant.FromContext(ctx)
	members := membership.LookupMany(s.Membership, ids, userID)
	out := make([]scopedRoom, 0, len(members))
	for _, id := range ids {
		m, ok := members[id]
		if !ok || !m.IsHuman() || m.Preview || (m.Tenant != "" && tid != "" && m.Tenant != tid) {
			continue
		}
		out = append(out, scopedRoom{ID: id, M: m})
	}
	return out
}

// applyScope hands a checked scope to the caller's stream on this replica
// and waits for it to take it.
func (s *Service) applyScope(streamID, userID string, sc streamScope) error {
	st := s.memberStreams.get(streamID)
	if st == nil || st.userID != userID {
		return errNoStream
	}
	req := scopeRequest{streamScope: sc, done: make(chan error, 1)}
	select {
	case st.scopes <- req:
	case <-time.After(scopeTimeout):
		return errNoStream
	}
	select {
	case err := <-req.done:
		return err
	case <-time.After(scopeTimeout):
		return errNoStream
	}
}

// channelSet is the open channels as a set.
func channelSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, c := range list {
		out[c] = true
	}
	return out
}

// noteActivity records what one event changes in the member's counts.
func (s *Service) noteActivity(userID string, rooms map[string]*activityRoom, ev wire.Event, at func(string) *wire.RoomUnreadDelta) {
	switch ev.Kind {
	case wire.KindChatMessage, wire.KindAgentMessages:
		if rm := rooms[ev.RoomID]; rm != nil && ev.Sequence > 0 && s.countsAsUnread(userID, ev.RoomID, rm, ev.ChannelID) {
			d := at(ev.RoomID)
			d.Seqs = append(d.Seqs, ev.Sequence)
		}
	case wire.EventMessageDeleted:
		if rooms[ev.RoomID] != nil {
			if p, ok := wire.DecodePayload[struct {
				Sequence int64 `json:"sequence"`
			}](ev.Payload); ok && p.Sequence > 0 {
				d := at(ev.RoomID)
				d.Deleted = append(d.Deleted, p.Sequence)
			}
		}
	case wire.EventReadMarked:
		if p, ok := wire.DecodePayload[wire.ReadMarkedPayload](ev.Payload); ok && rooms[p.RoomID] != nil {
			at(p.RoomID).Read = true
		}
	case wire.EventChannelFollow:
		if p, ok := wire.DecodePayload[wire.ChannelFollowPayload](ev.Payload); ok && p.UserID == userID {
			if rm := rooms[ev.RoomID]; rm != nil {
				rm.follows = nil
			}
		}
	case wire.EventSectionMembership, wire.EventChannelUpdated:
		// Who may read what changed: the access is read again.
		if rm := rooms[ev.RoomID]; rm != nil {
			rm.access = &channelAccess{svc: s, ctx: rm.ctx, roomID: ev.RoomID, reader: rm.access.reader}
		}
	}
}

// countsAsUnread is unreadFor's rule for one new message: a channel the
// member may read, a thread only when they follow it.
func (s *Service) countsAsUnread(userID, roomID string, rm *activityRoom, channelID string) bool {
	if channelID == "" || s.Channels == nil {
		return true
	}
	if !rm.access.mayRead(channelID) {
		return false
	}
	ch, found, err := s.channelStore().GetChannel(rm.ctx, roomID, channelID)
	if err != nil || !found {
		return false
	}
	if !ch.IsThread() {
		return true
	}
	if rm.follows == nil {
		rm.follows = map[string]bool{}
		if fs, err := s.channelStore().ChannelFollows(rm.ctx, roomID, userID); err == nil {
			for _, f := range fs {
				rm.follows[f.ChannelID] = true
			}
		}
	}
	return rm.follows[channelID]
}

// normalizeRooms is a list of room ids as a cross-room read names them:
// normalized, each once, at most limit.
func normalizeRooms(list []string, limit int) ([]string, error) {
	var rooms []string
	seen := map[string]bool{}
	for _, raw := range list {
		roomID, err := roomctx.Normalize(strings.TrimSpace(raw))
		if err != nil || roomID == "" || seen[roomID] {
			continue
		}
		seen[roomID] = true
		rooms = append(rooms, roomID)
	}
	if len(rooms) > limit {
		return nil, fmt.Errorf("too many rooms (max %d)", limit)
	}
	return rooms, nil
}
