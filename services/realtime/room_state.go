package realtimehttp

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room state in memory (docs/prd/2026-10-30-message-path-performance.md
// §4.1): what a post and the notification producer decide with (a room's
// channels, sections, section memberships and admission; the room and its
// members' roles and rules agreements; a channel's followers) is read once
// and kept in memory, as Slack's channel servers and Discord's guild
// processes do, instead of read from MySQL on every message. The access rule
// stays store.LoadChannelAccess: the cache holds its inputs.
//
// Kept until the room's own events change it (observe; another replica's
// arrive on the bus, observeRemote: a replica keeping a room's state
// receives the room from the bus meanwhile, watch), and never longer than
// roomStateTTL (a lost event's bound). A failed read and a miss (a channel
// not found) are never kept.

const (
	roomStateTTL = 30 * time.Second
	followersTTL = 10 * time.Second
)

type roomStateCache struct {
	now func() time.Time
	// watch, when set, has the room received from the bus while its state
	// is kept here (Hub.Watch): the other replicas' changes reach it.
	watch func(roomID string)
	mu    sync.Mutex
	rooms map[string]*roomCacheEntry // tenant + room
}

type roomCacheEntry struct {
	at             time.Time
	channels       map[string]store.RoomChannel
	defaultCh      *store.RoomChannel
	sections       []store.RoomSection
	haveSections   bool
	memberSections map[string][]string
	admission      map[string]store.ChannelAdmission
	room           *store.Room
	members        map[string]store.Membership
	followers      map[string]cachedFollowers
}

type cachedFollowers struct {
	at      time.Time
	follows []store.ChannelFollow
}

func newRoomStateCache(now func() time.Time) *roomStateCache {
	if now == nil {
		now = time.Now
	}
	return &roomStateCache{now: now, rooms: map[string]*roomCacheEntry{}}
}

func roomCacheKey(ctx context.Context, roomID string) string {
	return tenant.FromContext(ctx) + "\x00" + roomID
}

// with runs fn on the room's entry under the lock, a fresh one when it is
// missing or older than roomStateTTL.
func (c *roomStateCache) with(ctx context.Context, roomID string, fn func(e *roomCacheEntry)) {
	key, now := roomCacheKey(ctx, roomID), c.now()
	if c.watch != nil {
		c.mu.Lock()
		e := c.rooms[key]
		c.mu.Unlock()
		if e == nil || now.Sub(e.at) >= roomStateTTL {
			c.watch(roomID) // before the reads that fill it: none of a change is missed
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.rooms[key]
	if e == nil || now.Sub(e.at) >= roomStateTTL {
		e = &roomCacheEntry{at: now, channels: map[string]store.RoomChannel{}, memberSections: map[string][]string{},
			admission: map[string]store.ChannelAdmission{}, members: map[string]store.Membership{}, followers: map[string]cachedFollowers{}}
		c.rooms[key] = e
	}
	fn(e)
}

// forget drops the room's whole state.
func (c *roomStateCache) forget(ctx context.Context, roomID string) {
	c.mu.Lock()
	delete(c.rooms, roomCacheKey(ctx, roomID))
	c.mu.Unlock()
}

// observe applies one of the room's published events: what it changed is
// read again on next use. Its cases are the kinds roomStateKind lists.
func (c *roomStateCache) observe(ctx context.Context, ev wire.Event) {
	switch ev.Kind {
	case wire.EventChannelUpdated, wire.EventRoomDeleted, wire.EventRoomUpdated:
		c.forget(ctx, ev.RoomID)
	case wire.EventSectionMembership:
		p, _ := wire.DecodePayload[wire.SectionMembershipPayload](ev.Payload)
		c.with(ctx, ev.RoomID, func(e *roomCacheEntry) { delete(e.memberSections, p.UserID) })
	case wire.EventMemberJoined, wire.EventMemberRemoved:
		p, _ := wire.DecodePayload[wire.MemberJoinedPayload](ev.Payload)
		c.with(ctx, ev.RoomID, func(e *roomCacheEntry) {
			delete(e.members, p.UserID)
			delete(e.memberSections, p.UserID)
		})
	case wire.EventChannelFollow:
		c.with(ctx, ev.RoomID, func(e *roomCacheEntry) { delete(e.followers, ev.ChannelID) })
	case wire.EventThreadUpdated:
		p, _ := wire.DecodePayload[wire.ThreadUpdatedPayload](ev.Payload)
		c.with(ctx, ev.RoomID, func(e *roomCacheEntry) {
			delete(e.channels, p.ThreadID)
			delete(e.admission, p.ThreadID)
		})
	}
}

// roomStateKind reports a kind that changes the room's state (observe).
func roomStateKind(kind string) bool {
	switch kind {
	case wire.EventChannelUpdated, wire.EventRoomDeleted, wire.EventRoomUpdated, wire.EventSectionMembership,
		wire.EventMemberJoined, wire.EventMemberRemoved, wire.EventChannelFollow, wire.EventThreadUpdated:
		return true
	}
	return false
}

// observeRemote applies another replica's event (the hub's bus). It carries
// no tenant, so a kind that changes room state drops the room's whole state
// under every tenant (room ids are global): such events are rare, and the
// state is read again on next use.
func (c *roomStateCache) observeRemote(ev wire.Event) {
	if !roomStateKind(ev.Kind) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.rooms {
		if _, roomID, ok := strings.Cut(key, "\x00"); ok && roomID == ev.RoomID {
			delete(c.rooms, key)
		}
	}
}

// observeRemote is what another replica's event changes here.
func (s *Service) observeRemote(ev wire.Event) {
	if ev.Kind == wire.EventRoomView {
		s.Views.Remote(ev)
		return
	}
	if s.roomState != nil {
		s.roomState.observeRemote(ev)
	}
	if ev.Kind == wire.EventChannelFollow {
		s.postMarks.forgetRemote(ev)
	}
}

// cachedChannels is the channel store as realtime reads it: the hot reads
// from the room's state, every write passed through and forgotten.
type cachedChannels struct {
	ChannelStore
	c *roomStateCache
}

func (s cachedChannels) GetChannel(ctx context.Context, roomID, channelID string) (store.RoomChannel, bool, error) {
	var ch store.RoomChannel
	var hit bool
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { ch, hit = e.channels[channelID] })
	if hit {
		return ch, true, nil
	}
	ch, found, err := s.ChannelStore.GetChannel(ctx, roomID, channelID)
	if err == nil && found {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.channels[channelID] = ch })
	}
	return ch, found, err
}

func (s cachedChannels) DefaultChannel(ctx context.Context, roomID string) (store.RoomChannel, bool, error) {
	var def *store.RoomChannel
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { def = e.defaultCh })
	if def != nil {
		return *def, true, nil
	}
	ch, found, err := s.ChannelStore.DefaultChannel(ctx, roomID)
	if err == nil && found {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.defaultCh = &ch })
	}
	return ch, found, err
}

func (s cachedChannels) ListSections(ctx context.Context, roomID string) ([]store.RoomSection, error) {
	var secs []store.RoomSection
	var hit bool
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { secs, hit = e.sections, e.haveSections })
	if hit {
		return secs, nil
	}
	secs, err := s.ChannelStore.ListSections(ctx, roomID)
	if err == nil {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.sections, e.haveSections = secs, true })
	}
	return secs, err
}

func (s cachedChannels) MemberSections(ctx context.Context, roomID, userID string) ([]string, error) {
	var ids []string
	var hit bool
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { ids, hit = e.memberSections[userID] })
	if hit {
		return ids, nil
	}
	ids, err := s.ChannelStore.MemberSections(ctx, roomID, userID)
	if err == nil {
		if ids == nil {
			ids = []string{}
		}
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.memberSections[userID] = ids })
	}
	return ids, err
}

func (s cachedChannels) ChannelAdmission(ctx context.Context, roomID, channelID string) (store.ChannelAdmission, bool, error) {
	var a store.ChannelAdmission
	var hit bool
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { a, hit = e.admission[channelID] })
	if hit {
		return a, true, nil
	}
	a, found, err := s.ChannelStore.ChannelAdmission(ctx, roomID, channelID)
	if err == nil && found {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.admission[channelID] = a })
	}
	return a, found, err
}

// ChannelFollowersOf is kept until a member's follow changes
// (wire.EventChannelFollow) and never longer than followersTTL: a recommended
// channel joined with the room, or a section's channels, publish no follow.
func (s cachedChannels) ChannelFollowersOf(ctx context.Context, roomID, channelID string) ([]store.ChannelFollow, error) {
	var f cachedFollowers
	var hit bool
	now := s.c.now()
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { f, hit = e.followers[channelID] })
	if hit && now.Sub(f.at) < followersTTL {
		return f.follows, nil
	}
	follows, err := s.ChannelStore.ChannelFollowersOf(ctx, roomID, channelID)
	if err == nil {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.followers[channelID] = cachedFollowers{at: now, follows: follows} })
	}
	return follows, err
}

func (s cachedChannels) forgetChannel(ctx context.Context, roomID, channelID string) {
	s.c.with(ctx, roomID, func(e *roomCacheEntry) {
		delete(e.channels, channelID)
		delete(e.admission, channelID)
		delete(e.followers, channelID)
	})
}

func (s cachedChannels) PutChannel(ctx context.Context, ch store.RoomChannel) error {
	err := s.ChannelStore.PutChannel(ctx, ch)
	s.forgetChannel(ctx, ch.RoomID, ch.ChannelID)
	return err
}

func (s cachedChannels) NoteThreadReply(ctx context.Context, roomID, threadID, author string, atMs int64) (store.RoomChannel, error) {
	th, err := s.ChannelStore.NoteThreadReply(ctx, roomID, threadID, author, atMs)
	s.forgetChannel(ctx, roomID, threadID)
	return th, err
}

func (s cachedChannels) ClaimChannelPost(ctx context.Context, roomID, channelID, userID string, atMs, minGapMs int64) (int64, error) {
	wait, err := s.ChannelStore.ClaimChannelPost(ctx, roomID, channelID, userID, atMs, minGapMs)
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { delete(e.followers, channelID) })
	return wait, err
}

func (s cachedChannels) FollowChannel(ctx context.Context, roomID, channelID, userID string, atMs int64, marks store.FollowMarks) error {
	err := s.ChannelStore.FollowChannel(ctx, roomID, channelID, userID, atMs, marks)
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { delete(e.followers, channelID) })
	return err
}

// cachedRoles is the room and member store as realtime's checks read it:
// GetRoom and GetMember from the room's state.
type cachedRoles struct {
	RoomRoleStore
	c *roomStateCache
}

func (s cachedRoles) GetRoom(ctx context.Context, roomID string) (store.Room, error) {
	var r *store.Room
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { r = e.room })
	if r != nil {
		return *r, nil
	}
	room, err := s.RoomRoleStore.GetRoom(ctx, roomID)
	if err == nil {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.room = &room })
	}
	return room, err
}

func (s cachedRoles) GetMember(ctx context.Context, roomID, userID string) (store.Membership, bool, error) {
	var m store.Membership
	var hit bool
	s.c.with(ctx, roomID, func(e *roomCacheEntry) { m, hit = e.members[userID] })
	if hit {
		return m, true, nil
	}
	m, found, err := s.RoomRoleStore.GetMember(ctx, roomID, userID)
	if err == nil && found {
		s.c.with(ctx, roomID, func(e *roomCacheEntry) { e.members[userID] = m })
	}
	return m, found, err
}

// initRoomState makes the room state once.
func (s *Service) initRoomState() {
	s.roomState = newRoomStateCache(nil)
	if s.Hub != nil && s.Hub.Bus != nil {
		s.roomState.watch = s.Hub.Watch
	}
}

// channelStore is the channel store as realtime reads it: through the room
// state (nil when the deployment has no channels).
func (s *Service) channelStore() ChannelStore {
	if s.Channels == nil {
		return nil
	}
	s.roomStateOnce.Do(s.initRoomState)
	return cachedChannels{ChannelStore: s.Channels, c: s.roomState}
}

// roleStore is the room and member store through the room state.
func (s *Service) roleStore() RoomRoleStore {
	if s.RoomRoles == nil {
		return nil
	}
	s.roomStateOnce.Do(s.initRoomState)
	return cachedRoles{RoomRoleStore: s.RoomRoles, c: s.roomState}
}
