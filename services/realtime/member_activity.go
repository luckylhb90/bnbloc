package realtimehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The member's activity stream (GET /v1/my/activity?rooms=, part B of
// docs/prd/2026-10-30-replicas-without-owners.md): one per client, across
// the rooms it names, instead of polling every room's unread counts. It
// sends the rooms' unread state once (and again whenever it may have missed
// something: a gap), then, at most once an activityFlush, what changed: the
// new messages the member may read, the ones deleted, and the rooms the
// member marked read on any device (their count is read again). The counts
// follow the rule GET /v1/rooms-unread answers with (unreadFor): channels
// the member may read, a thread only when they follow it.

const (
	activityFlush = time.Second
	// activityBuffer is the stream's buffer: one subscriber for every room
	// it names.
	activityBuffer = 256
)

// activityRoom is one room the stream counts.
type activityRoom struct {
	ctx    context.Context
	access *channelAccess
	// follows are the threads the member follows (nil: not read yet).
	follows map[string]bool
}

func (s *Service) handleMyActivity(w http.ResponseWriter, r *http.Request) {
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
	named, err := namedRooms(r)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rooms := map[string]*activityRoom{}
	keys := []string{wire.MemberActivityKey(userID)}
	if s.Membership != nil {
		for _, roomID := range named {
			m, member := s.Membership.Lookup(roomID, userID)
			if !member || m.Preview {
				continue
			}
			ctx := r.Context()
			if m.Tenant != "" {
				ctx = tenant.NewContext(ctx, m.Tenant)
			}
			rooms[roomID] = &activityRoom{ctx: ctx, access: &channelAccess{svc: s, ctx: ctx, roomID: roomID, reader: readerOf(userID, m)}}
			keys = append(keys, roomID)
		}
	}
	sub := s.Hub.SubscribeRooms(keys, activityBuffer)
	defer s.Hub.Unsubscribe(keys[0], sub)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, payload any) {
		data, err := json.Marshal(wire.Event{Kind: kind, Payload: payload, AtUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
	}
	state := func() {
		ids := make([]string, 0, len(rooms))
		for id := range rooms {
			ids = append(ids, id)
		}
		send(wire.UnreadStateKind, s.unreadFor(r.Context(), userID, ids))
	}
	state()
	flusher.Flush()

	delta := map[string]*wire.RoomUnreadDelta{}
	at := func(roomID string) *wire.RoomUnreadDelta {
		d := delta[roomID]
		if d == nil {
			d = &wire.RoomUnreadDelta{}
			delta[roomID] = d
		}
		return d
	}
	tick := time.NewTicker(activityFlush)
	defer tick.Stop()
	heartbeat := time.NewTicker(s.heartbeatEvery())
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-obs.Draining():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-tick.C:
			if len(delta) == 0 {
				continue
			}
			out := wire.UnreadDeltaPayload{Rooms: make(map[string]wire.RoomUnreadDelta, len(delta))}
			for id, d := range delta {
				out.Rooms[id] = *d
			}
			delta = map[string]*wire.RoomUnreadDelta{}
			send(wire.UnreadDeltaKind, out)
			flusher.Flush()
		case ev, open := <-sub.Events():
			if !open {
				return // fell behind twice: the client reopens and starts again
			}
			s.noteActivity(userID, rooms, ev, at)
			if sub.TakeGap() {
				// Something may have been missed: start again from the
				// state, not from deltas.
				delta = map[string]*wire.RoomUnreadDelta{}
				state()
				flusher.Flush()
			}
		}
	}
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
