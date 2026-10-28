package activity

import (
	"context"
	"log"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// Who may read which channel here (store.MayReadChannel, the one rule;
// docs/prd/2026-10-28-channels-and-threads.md C11): a run record or a job
// row belongs to its channel, so it is listed only to a reader of that
// channel.

// ChannelAccessReader is the channel store's reads the rule needs.
type ChannelAccessReader interface {
	store.ChannelAccessReader
	GetChannel(ctx context.Context, roomID, channelID string) (store.RoomChannel, bool, error)
}

// channelReaders are the caller's verdicts across rooms, read once per room
// and channel.
type channelReaders struct {
	s      *Service
	ctx    context.Context
	user   string
	rooms  map[string]store.ChannelAccess
	failed map[string]bool
	seen   map[string]bool // room\x00channel -> may read
}

// channelReaders is the request caller's reader (a person: the bridge's
// owner reads their own runs).
func (s *Service) channelReaders(r *http.Request) *channelReaders {
	return &channelReaders{s: s, ctx: r.Context(), user: svcauth.Subject(r),
		rooms: map[string]store.ChannelAccess{}, failed: map[string]bool{}, seen: map[string]bool{}}
}

// mayRead reports whether the caller may read channelID of roomID ("" is
// room-wide). A failed read refuses (fail closed).
func (cr *channelReaders) mayRead(roomID, channelID string) bool {
	if channelID == "" || cr.s.Access == nil {
		return true
	}
	key := roomID + "\x00" + channelID
	if v, ok := cr.seen[key]; ok {
		return v
	}
	a, ok := cr.rooms[roomID]
	if !ok && !cr.failed[roomID] {
		reader := store.Reader{User: cr.user}
		if m, found := cr.s.memberOf(roomID, cr.user); found {
			reader.Admin = m.Role.CanInvite()
		} else {
			reader.User = ""
		}
		var err error
		if a, err = store.LoadChannelAccess(cr.ctx, cr.s.Access, roomID, reader); err != nil {
			log.Printf("activity: room=%s channel access lookup failed; withholding: %v", roomID, err)
			cr.failed[roomID] = true
		} else {
			cr.rooms[roomID] = a
			ok = true
		}
	}
	if !ok {
		return false
	}
	v := true
	if a.HasMembersOnly() {
		c, found, err := cr.s.Access.GetChannel(cr.ctx, roomID, channelID)
		v = err == nil && (!found || a.MayReadChannel(c))
		if err != nil {
			log.Printf("activity: room=%s channel=%s lookup failed; withholding: %v", roomID, channelID, err)
		}
	}
	cr.seen[key] = v
	return v
}

// memberOf reads userID's membership of roomID (the cached directory).
func (s *Service) memberOf(roomID, userID string) (membership.Member, bool) {
	if s.Membership == nil {
		return membership.Member{}, false
	}
	return s.Membership.Lookup(roomID, userID)
}
