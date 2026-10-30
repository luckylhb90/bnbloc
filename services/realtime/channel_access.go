package realtimehttp

import (
	"context"
	"log"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Who may read a channel here (store.MayReadChannel, the one rule; docs/
// prd/2026-10-28-channels-and-threads.md C11-C14): every realtime path that
// shows a channel or its content asks it through this file. The reader is
// the caller, as a person (an agent reads through its owner's bridge, whose
// own sections are the owner's; the job's requester decides an agent's
// records through the room service's listing, roomlog.AgentChannelList).

// readerOf is the store reader of a request's caller: themselves, an admin
// when the room's owner or an admin. A preview (no member) reads nobody's
// sections.
func readerOf(userID string, m membership.Member) store.Reader {
	if m.Preview {
		return store.Reader{}
	}
	return store.Reader{User: userID, Admin: m.Role.CanInvite(), Agent: !m.IsHuman()}
}

// channelAccess is one reader's verdicts for a room's channels, read once
// and kept per channel.
type channelAccess struct {
	svc     *Service
	ctx     context.Context
	roomID  string
	reader  store.Reader
	access  store.ChannelAccess
	loaded  bool
	verdict map[string]bool // channel id -> may read
}

// load reads the reader's access (fail closed: a failed read leaves the
// access unloaded, and mayRead refuses every channel of a section).
func (a *channelAccess) load() error {
	var r store.ChannelAccessReader
	if a.svc.Channels != nil {
		r = a.svc.channelStore()
	}
	acc, err := store.LoadChannelAccess(a.ctx, r, a.roomID, a.reader)
	if err != nil {
		return err
	}
	a.access, a.loaded, a.verdict = acc, true, map[string]bool{}
	return nil
}

// mayRead reports whether the reader may read channelID ("" is room-wide:
// always). A lookup that fails refuses (fail closed) and is retried on the
// next call.
func (a *channelAccess) mayRead(channelID string) bool {
	if channelID == "" {
		return true
	}
	if !a.loaded {
		if err := a.load(); err != nil {
			log.Printf("realtime: room=%s channel access lookup failed; withholding: %v", a.roomID, err)
			return false
		}
	}
	if !a.access.HasMembersOnly() {
		return true
	}
	if v, ok := a.verdict[channelID]; ok {
		return v
	}
	if a.svc.Channels == nil {
		return true
	}
	c, found, err := a.svc.channelStore().GetChannel(a.ctx, a.roomID, channelID)
	if err != nil {
		log.Printf("realtime: room=%s channel=%s lookup failed; withholding: %v", a.roomID, channelID, err)
		return false
	}
	// A deleted channel's events (its last edits) go where its section was:
	// with no live row, only a room without members-only sections delivers
	// them, which HasMembersOnly answered above.
	v := found && a.access.MayReadChannel(c)
	a.verdict[channelID] = v
	return v
}

// observe forgets the verdicts when ev changes what the reader may read: a
// channel or section changed (a channel moved into a members-only section),
// or a section's membership did.
func (a *channelAccess) observe(ev wire.Event) {
	switch ev.Kind {
	case wire.EventChannelUpdated, wire.EventSectionMembership:
		a.loaded = false
	}
}

// newReaderFilter is the stream's reader access for the caller of r.
func (s *Service) newReaderFilter(w http.ResponseWriter, r *http.Request, roomID, userID string, m membership.Member) (*channelAccess, bool) {
	a := &channelAccess{svc: s, ctx: r.Context(), roomID: roomID, reader: readerOf(userID, m)}
	if err := a.load(); err != nil {
		obs.ServerError(w, "channel access lookup failed", err)
		httpx.Error(w, "channel access lookup failed", http.StatusInternalServerError)
		return nil, false
	}
	return a, true
}

// memberMayRead is the background form (a notification, a plugin hook):
// whether userID, as a person, may read channelID of roomID. A failed read
// refuses.
func (s *Service) memberMayRead(ctx context.Context, roomID, userID string, m membership.Member, channelID string) bool {
	a := &channelAccess{svc: s, ctx: ctx, roomID: roomID, reader: readerOf(userID, m)}
	return a.mayRead(channelID)
}

// roomReaders are one person's channel access across rooms (a cross-room
// read: global search), loaded per room on first use.
type roomReaders struct {
	svc   *Service
	ctx   context.Context
	user  string
	rooms map[string]*channelAccess
}

// searchReaders is userID's access in their rooms (member: the rooms the
// room service lists for them). The room role is not read: it decides only
// who may know a section exists, never who reads it (C12).
func (s *Service) searchReaders(ctx context.Context, userID string, member []membership.RoomRef) *roomReaders {
	return &roomReaders{svc: s, ctx: ctx, user: userID, rooms: make(map[string]*channelAccess, len(member))}
}

// mayRead reports whether the person may read channelID of roomID.
func (rr *roomReaders) mayRead(roomID, channelID string) bool {
	a, ok := rr.rooms[roomID]
	if !ok {
		a = &channelAccess{svc: rr.svc, ctx: rr.ctx, roomID: roomID, reader: store.Reader{User: rr.user}}
		rr.rooms[roomID] = a
	}
	return a.mayRead(channelID)
}

// memberMayReadChannel is memberMayRead for a background reader that holds
// no membership answer (a notification): the member's own, as the room
// service reports it.
func (s *Service) memberMayReadChannel(ctx context.Context, roomID, channelID, userID string) bool {
	m, ok := membership.Member{}, false
	if s.Membership != nil {
		m, ok = s.Membership.Lookup(roomID, userID)
	}
	if !ok {
		return false
	}
	return s.memberMayRead(ctx, roomID, userID, m, channelID)
}
