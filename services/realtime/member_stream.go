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
// /v1/my/events/scope) with the rooms whose unread it carries and the room
// it shows in full, and scopes it again when either changes, never
// reopening it.
//
//   - Unread: the scoped rooms' state (unread_state) on every scope of
//     rooms and after a gap, then at most once an unreadFlush what changed
//     (unread_delta): new messages that count by unreadFor's rule,
//     deletions, a mark of read on any device.
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
	Rooms *[]scopedRoom `json:"rooms,omitempty"`
	Open  *openRoom     `json:"open,omitempty"`
}

// scopedRoom is a room the member is in, checked when it was scoped.
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
	st := &memberStream{id: s.Replicas.streamPrefix() + ids.New("ms_"), userID: userID, scopes: make(chan scopeRequest)}
	s.memberStreams.add(st)
	defer s.memberStreams.remove(st.id)
	memberKey := wire.MemberActivityKey(userID)
	sub := s.Hub.SubscribeRooms([]string{memberKey}, memberStreamBuffer)
	defer s.Hub.Unsubscribe(memberKey, sub)
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
	ctx := r.Context()
	tz := r.URL.Query().Get("tz")
	now := func() int64 { return time.Now().UnixMilli() }
	send(wire.Event{Kind: wire.EventStreamOpened, AtUnixMs: now(), Payload: wire.StreamOpenedPayload{StreamID: st.id}})
	flusher.Flush()

	m := &memberStreamState{s: s, ctx: ctx, userID: userID, tz: tz, send: send, rooms: map[string]*activityRoom{}, delta: map[string]*wire.RoomUnreadDelta{}}
	defer m.closeOpen()
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
			if req.Rooms != nil {
				m.setRooms(*req.Rooms)
			}
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
			if req.Rooms != nil {
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
			if open && sub.TakeGap() {
				// Something may have been missed: the unread starts again
				// from the state, the open room from history.
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

func (m *memberStreamState) setRooms(rooms []scopedRoom) {
	next := make(map[string]*activityRoom, len(rooms))
	for _, r := range rooms {
		if cur := m.rooms[r.ID]; cur != nil {
			next[r.ID] = cur
			continue
		}
		ctx := m.ctx
		if r.M.Tenant != "" {
			ctx = tenant.NewContext(ctx, r.M.Tenant)
		}
		next[r.ID] = &activityRoom{ctx: ctx, access: &channelAccess{svc: m.s, ctx: ctx, roomID: r.ID, reader: readerOf(m.userID, r.M)}}
	}
	m.rooms = next
	for id := range m.delta {
		if next[id] == nil {
			delete(m.delta, id)
		}
	}
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
	delete(m.rooms, roomID)
}

// event handles one event of the rooms the stream carries.
func (m *memberStreamState) event(ev wire.Event) {
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

// handleMyEventsScope scopes the caller's member stream. Every room named
// is checked here: the rooms whose unread it carries must be the caller's
// (others are left out), the open room readable by them (a member's, or a
// preview's).
func (s *Service) handleMyEventsScope(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireSubject(w, r)
	if !ok {
		return
	}
	var body wire.StreamScopeRequest
	if !httpx.DecodeJSON(w, r, 64<<10, &body) {
		return
	}
	var sc streamScope
	if body.Rooms != nil {
		named, err := normalizeRooms(*body.Rooms, wire.MaxStreamRooms)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rooms := make([]scopedRoom, 0, len(named))
		for _, id := range named {
			if m, member := s.Membership.Lookup(id, userID); member && !m.Preview {
				rooms = append(rooms, scopedRoom{ID: id, M: m})
			}
		}
		sc.Rooms = &rooms
	}
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
	case wire.EventMemberRemoved:
		if p, ok := wire.DecodePayload[wire.MemberRemovedPayload](ev.Payload); ok && p.UserID == userID {
			delete(rooms, ev.RoomID)
		}
	case wire.EventRoomDeleted:
		delete(rooms, ev.RoomID)
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
