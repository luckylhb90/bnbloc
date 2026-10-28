package room

import (
	"context"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Who a room-service read is for (store.MayReadChannel's reader): the
// caller. An agent's read reaches this service through the gateway's agent
// door with its job's grant (?job=), which proxies it as the job's requester
// (C14: an agent reads as the person it works for); without a grant the
// bridge reads as its owner, whose channel listing for agents (?agents=1)
// leaves out what agents may not read. Every route here that lists
// channels, sections or a channel's content loads the access with
// readerAccess and asks it (TestChannelReadsAskTheRule).

// requestReader is who r reads for: the caller, with their room role. A
// previewer of a shared room (no member) reads nobody's sections.
func (s *Service) requestReader(ctx context.Context, r *http.Request, roomID string) (store.Reader, error) {
	user := caller(r)
	m, ok, err := s.Store.GetMember(ctx, roomID, user)
	if err != nil {
		return store.Reader{}, err
	}
	if !ok || !m.Human() {
		return store.Reader{}, nil
	}
	return store.Reader{User: user, Admin: m.Role.CanInvite()}, nil
}

// readerAccess loads what MayReadChannel decides for the request's reader,
// answering a read failure itself (500: fail closed).
func (s *Service) readerAccess(w http.ResponseWriter, r *http.Request, roomID string) (store.ChannelAccess, bool) {
	reader, err := s.requestReader(r.Context(), r, roomID)
	var a store.ChannelAccess
	if err == nil {
		a, err = store.LoadChannelAccess(r.Context(), s.Channels, roomID, reader)
	}
	if err != nil {
		obs.ServerError(w, "read channel access failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.ChannelAccess{}, false
	}
	return a, true
}

// memberAccess loads the access of a person (a write route checks it before
// it acts on a channel's content).
func (s *Service) memberAccess(ctx context.Context, roomID string, who channelCaller) (store.ChannelAccess, error) {
	return store.LoadChannelAccess(ctx, s.Channels, roomID, store.Reader{User: who.user, Admin: who.role.CanInvite()})
}

// readableIn answers, for a person across their rooms (a cross-room
// listing: My apps), whether they may read a channel of a room: each room's
// access is loaded once. A failed load refuses the room's members-only
// channels (fail closed) and is returned for the caller to log.
type readableIn struct {
	s     *Service
	ctx   context.Context
	user  string
	rooms map[string]store.ChannelAccess
	chans map[string]map[string]store.RoomChannel
}

func (s *Service) newReadableIn(ctx context.Context, user string) *readableIn {
	return &readableIn{s: s, ctx: ctx, user: user, rooms: map[string]store.ChannelAccess{}, chans: map[string]map[string]store.RoomChannel{}}
}

func (ri *readableIn) mayRead(roomID, channelID string) (bool, error) {
	a, ok := ri.rooms[roomID]
	if !ok {
		var err error
		if a, err = store.LoadChannelAccess(ri.ctx, ri.s.Channels, roomID, store.Reader{User: ri.user}); err != nil {
			return false, err
		}
		ri.rooms[roomID] = a
	}
	if channelID == "" || !a.HasMembersOnly() {
		return true, nil
	}
	byID, ok := ri.chans[roomID]
	if !ok {
		list, err := ri.s.Channels.ListChannels(ri.ctx, roomID)
		if err != nil {
			return false, err
		}
		byID = map[string]store.RoomChannel{}
		for _, c := range list {
			byID[c.ChannelID] = c
		}
		ri.chans[roomID] = byID
	}
	c, found := byID[channelID]
	return !found || a.MayReadChannel(c), nil
}
