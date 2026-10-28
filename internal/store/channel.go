package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomChannel is a named message stream inside a room. A channel partitions
// only the message stream; room-level state (members, agent borrows, git sync,
// files) is shared across all channels in the room. Exactly one channel per
// room is the default, which receives messages that are not addressed to a
// specific channel. A channel is soft-deleted by stamping DeletedAtMs, so its
// history remains addressable while it disappears from live listings.
//
// Its setup (docs/prd/2026-10-28-channels-and-threads.md §2.1): a purpose
// and rules, a section (SectionID, "" = the room's top level) and a position
// in it, an owner (its creator by default), an archive mark (an archived
// channel is read-only and leaves the default listing; its history stays
// readable) and its settings document.
type RoomChannel struct {
	ChannelID string
	TenantID  string
	RoomID    string
	Name      string
	IsDefault bool
	// Kind is ChannelKindStandard ("") for an ordinary channel or
	// ChannelKindPeople for the room's people chat: a human-only side
	// conversation agents never see (no agent recipient, never delivered to
	// agent streams or room context, no plugin hooks).
	Kind         string
	CreatedBy    string
	CreatedAtMs  int64
	DeletedAtMs  *int64
	Purpose      string
	Rules        string
	SectionID    string
	Position     int
	OwnerID      string
	ArchivedAtMs *int64
	Settings     wire.ChannelSettings
}

// Channel kinds. The kind is fixed at creation.
const (
	ChannelKindStandard = ""
	ChannelKindPeople   = "people"
)

// IsPeople reports whether c is the room's people chat.
func (c RoomChannel) IsPeople() bool { return c.Kind == ChannelKindPeople }

// Archived reports whether c is archived (read-only until restored).
func (c RoomChannel) Archived() bool { return c.ArchivedAtMs != nil }

// cloned returns c with its settings' pointer copied, so a reader never
// shares the store's copy.
func (c RoomChannel) cloned() RoomChannel {
	if a := c.Settings.DefaultAgent; a != nil {
		cp := *a
		c.Settings.DefaultAgent = &cp
	}
	return c
}

// PeopleChannelName is the display name stored on a room's people chat.
const PeopleChannelName = "People chat"

// peopleChannelID is the room's people-chat channel id, derived from its
// (tenant, room) so concurrent lazy creates converge on ONE row (the id is
// the primary key): there is exactly one people chat per room by construction.
func peopleChannelID(tenantID, roomID string) string {
	sum := sha256.Sum256([]byte(ChannelKindPeople + "\x00" + tenantID + "\x00" + roomID))
	return "pc_" + hex.EncodeToString(sum[:8])
}

// ErrChannelNotFound is returned by UpdateChannel and SetChannelArchived
// when the target channel is absent or soft-deleted: a change is conditional
// on liveness, so one racing a delete can never resurrect the row the delete
// just retired.
var ErrChannelNotFound = errors.New("store: channel not found")

// ErrChannelExists is returned by PutChannel when the channel id is already
// taken, in any room of any tenant.
var ErrChannelExists = errors.New("store: channel id already exists")

// ErrSectionNotFound is returned by UpdateSection for an absent or deleted
// section; ErrSectionExists by PutSection for a taken section id.
var (
	ErrSectionNotFound = errors.New("store: section not found")
	ErrSectionExists   = errors.New("store: section id already exists")
)

// ChannelPatch is a partial update of a channel's setup: a nil field is left
// as it is; Settings replaces the whole document.
type ChannelPatch struct {
	Name      *string
	Purpose   *string
	Rules     *string
	SectionID *string
	Position  *int
	OwnerID   *string
	Settings  *wire.ChannelSettings
}

// RoomSection is a named group of a room's channels with its own owners and
// rules (§2.1a). Visibility is wire.SectionOpen or wire.SectionMembers;
// JoinPolicy is how a member joins it (C17). A section is soft-deleted.
type RoomSection struct {
	SectionID   string
	TenantID    string
	RoomID      string
	Name        string
	Position    int
	Visibility  string
	Rules       string
	Owners      []string
	JoinPolicy  string
	CreatedBy   string
	CreatedAtMs int64
	DeletedAtMs *int64
}

// IsOwner reports whether user is one of the section's owners (subjects
// compare exactly).
func (s RoomSection) IsOwner(user string) bool { return user != "" && slices.Contains(s.Owners, user) }

func (s RoomSection) cloned() RoomSection {
	s.Owners = slices.Clone(s.Owners)
	if s.Owners == nil {
		s.Owners = []string{}
	}
	return s
}

// SectionPatch is a partial update of a section.
type SectionPatch struct {
	Name     *string
	Rules    *string
	Position *int
	Owners   *[]string
}

// ChannelFollow is one member's following of a channel (membership as
// following, C18): it decides what their rail shows, never what they may
// read. Starred and Hidden are their own order; LastPostedAtMs is their last
// post in the channel, which slow mode reads.
type ChannelFollow struct {
	TenantID       string
	RoomID         string
	ChannelID      string
	UserID         string
	JoinedAtMs     int64
	Starred        bool
	Hidden         bool
	LastPostedAtMs int64
}

// FollowMarks are the marks a follow sets; nil leaves a mark as it is.
type FollowMarks struct {
	Starred *bool
	Hidden  *bool
}

// PackageChannelSettings is what of a channel's settings a room package
// holds (capture: who posts, agents admitted, slow mode; never a default
// agent, which names someone's), and PackagedChannelSettings the settings a
// package's channel is given (install): the one copy of the settings
// document between a room and a package, kept here beside the rule that
// reads its agents switch (ChannelAdmitsAgents).
func PackageChannelSettings(s wire.ChannelSettings) wire.RoomPackageChannelSettings {
	return wire.RoomPackageChannelSettings{WhoPosts: s.WhoPosts, NoAgents: s.NoAgents, SlowModeSeconds: s.SlowModeSeconds}
}

func PackagedChannelSettings(s wire.RoomPackageChannelSettings) wire.ChannelSettings {
	return wire.ChannelSettings{WhoPosts: s.WhoPosts, NoAgents: s.NoAgents, SlowModeSeconds: s.SlowModeSeconds}
}

// ChannelAdmission is what decides whether agents may use a channel: its
// kind (the people chat never admits them) and its settings' NoAgents.
type ChannelAdmission struct {
	Kind     string
	NoAgents bool
}

// ChannelStore persists a room's channels, their sections and who follows
// them. Implementations must be safe for concurrent use and must scope every
// operation by the tenant bound to the context, failing closed when the
// context carries none.
type ChannelStore interface {
	// PutChannel creates a channel; ErrChannelExists when the id is taken
	// (channel ids are global, so it never rewrites an existing row).
	PutChannel(ctx context.Context, c RoomChannel) error
	// GetChannel returns a live (not deleted, archived or not) channel by id
	// within a room, found=false when absent or soft-deleted.
	GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool, error)
	// ListChannels returns a room's live channels of every kind (the people
	// chat and archived channels included), default first, then by
	// position, then by created_at ascending, then by channel id. Callers
	// that present or copy ordinary channels filter themselves.
	ListChannels(ctx context.Context, roomID string) ([]RoomChannel, error)
	// DefaultChannel returns a room's default channel, found=false when none.
	DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool, error)
	// DeleteChannel soft-deletes a channel. Deleting an absent or already
	// deleted channel is a no-op (the first deletion time is kept); callers
	// enforce the default-channel guard.
	DeleteChannel(ctx context.Context, roomID, channelID string, atMs int64) error
	// UpdateChannel applies patch to a live channel, returning
	// ErrChannelNotFound when it is absent or soft-deleted — unlike
	// PutChannel, it never resurrects a deleted row.
	UpdateChannel(ctx context.Context, roomID, channelID string, patch ChannelPatch) error
	// SetChannelArchived archives a live channel at *atMs (an archived one
	// keeps its first time) or, with nil, restores it; ErrChannelNotFound
	// when it is absent or deleted.
	SetChannelArchived(ctx context.Context, roomID, channelID string, atMs *int64) error
	// EnsurePeopleChannel returns the room's people chat, creating it (as
	// creator, at atMs) when absent. Idempotent and race-free: concurrent
	// callers get the same channel.
	EnsurePeopleChannel(ctx context.Context, roomID, creator string, atMs int64) (RoomChannel, error)
	// ChannelAdmission returns what decides whether agents may use a
	// channel of the room, soft-deleted or not; found=false when no such
	// channel exists. Unlike GetChannel it reports read failures, so a
	// delivery filter can fail closed.
	ChannelAdmission(ctx context.Context, roomID, channelID string) (ChannelAdmission, bool, error)

	// PutSection creates a section; ErrSectionExists when the id is taken.
	PutSection(ctx context.Context, s RoomSection) error
	// GetSection returns a live section of the room, found=false otherwise.
	GetSection(ctx context.Context, roomID, sectionID string) (RoomSection, bool, error)
	// ListSections returns a room's live sections by position, then
	// created_at, then id.
	ListSections(ctx context.Context, roomID string) ([]RoomSection, error)
	// UpdateSection applies patch to a live section; ErrSectionNotFound
	// otherwise.
	UpdateSection(ctx context.Context, roomID, sectionID string, patch SectionPatch) error
	// DeleteSection soft-deletes a section and moves its channels to the
	// room's top level, at once. An absent or deleted section is a no-op.
	DeleteSection(ctx context.Context, roomID, sectionID string, atMs int64) error

	// FollowChannel makes userID follow a channel of the room (joined at
	// atMs the first time; a repeat keeps that time) and applies marks.
	FollowChannel(ctx context.Context, roomID, channelID, userID string, atMs int64, marks FollowMarks) error
	// UnfollowChannel ends userID's following of the channel (their marks
	// go with it); absent is no error.
	UnfollowChannel(ctx context.Context, roomID, channelID, userID string) error
	// ChannelFollows returns the channels of the room userID follows.
	ChannelFollows(ctx context.Context, roomID, userID string) ([]ChannelFollow, error)
	// ChannelFollowerCounts counts the followers of each followed channel of
	// the room.
	ChannelFollowerCounts(ctx context.Context, roomID string) (map[string]int, error)
	// ClaimChannelPost records userID's post in the channel at atMs and
	// makes them follow it (posting joins, C18). With minGapMs > 0 (slow
	// mode) the claim is refused when their last post there is less than
	// minGapMs before atMs: retryAfterMs > 0 says how long to wait, and
	// nothing is written. The check and the write are one step, so two
	// concurrent posts cannot both pass.
	ClaimChannelPost(ctx context.Context, roomID, channelID, userID string, atMs, minGapMs int64) (retryAfterMs int64, err error)
}

// ChannelAdmissionReader is the read ChannelAdmitsAgents needs.
type ChannelAdmissionReader interface {
	ChannelAdmission(ctx context.Context, roomID, channelID string) (ChannelAdmission, bool, error)
}

// ChannelAdmitsAgents is the ONE rule of whether agents may read and write a
// channel of the room: never the room's people chat, and never a channel
// whose settings bar them (NoAgents). The room-wide "" is no channel, and a
// nil reader (in-memory dev) admits. A read failure is returned, never
// "admits": every guard built on it fails closed (realtime's delivery
// filters and agent ingests, the handoff import, activity before it stores an
// agent reply).
func ChannelAdmitsAgents(ctx context.Context, r ChannelAdmissionReader, roomID, channelID string) (bool, error) {
	if channelID == "" || r == nil {
		return true, nil
	}
	a, _, err := r.ChannelAdmission(ctx, roomID, channelID)
	if err != nil {
		return false, err
	}
	return a.Admits(), nil
}

// Admits is the rule ChannelAdmitsAgents applies to what it read: never the
// people chat, never a channel whose settings bar agents.
func (a ChannelAdmission) Admits() bool { return a.Kind != ChannelKindPeople && !a.NoAgents }

// Admission is what of c decides agents' admission (a listing that already
// holds the channel applies Admits to it, never its own copy of the rule).
func (c RoomChannel) Admission() ChannelAdmission {
	return ChannelAdmission{Kind: c.Kind, NoAgents: c.Settings.NoAgents}
}

// sortChannels is the one channel order: default first, then position, then
// oldest, then id (MySQL's ORDER BY spells the same).
func sortChannels(out []RoomChannel) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].ChannelID < out[j].ChannelID
	})
}

// sortSections is the one section order: position, then oldest, then id.
func sortSections(out []RoomSection) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].SectionID < out[j].SectionID
	})
}

// applyChannelPatch is the one in-memory application of a ChannelPatch.
func applyChannelPatch(c *RoomChannel, p ChannelPatch) {
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.Purpose != nil {
		c.Purpose = *p.Purpose
	}
	if p.Rules != nil {
		c.Rules = *p.Rules
	}
	if p.SectionID != nil {
		c.SectionID = *p.SectionID
	}
	if p.Position != nil {
		c.Position = *p.Position
	}
	if p.OwnerID != nil {
		c.OwnerID = *p.OwnerID
	}
	if p.Settings != nil {
		c.Settings = *p.Settings
	}
}

// MemoryChannel is an in-memory ChannelStore. It is a view over a MemoryRoom's
// channel, section and follow tables sharing that store's mutex, so
// CreateRoomWithOwnerAndChannel inserts the room, its owner, and the default
// channel in one critical section, and DeleteRoom clears them all.
// NewMemoryChannel allocates a private backing room for standalone use;
// channels created together with a room are visible only to the view from
// (*MemoryRoom).Channels (that is how NewBackends wires the memory backends —
// pairing a room store with an unrelated NewMemoryChannel forks state).
type MemoryChannel struct {
	room *MemoryRoom
}

// NewMemoryChannel constructs a channel store over a private backing room.
func NewMemoryChannel() *MemoryChannel { return &MemoryChannel{room: NewMemoryRoom()} }

var _ ChannelStore = (*MemoryChannel)(nil)

// channelKey scopes a room id by its tenant.
func channelKey(tenantID, roomID string) string { return tenantID + "\x00" + roomID }

// followKey is one follow's key.
func followKey(tenantID, roomID, channelID, userID string) string {
	return tenantID + "\x00" + roomID + "\x00" + channelID + "\x00" + userID
}

func (s *MemoryChannel) PutChannel(ctx context.Context, c RoomChannel) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	for _, byID := range s.room.channels {
		if _, taken := byID[c.ChannelID]; taken {
			return ErrChannelExists
		}
	}
	return s.room.putChannelLocked(tnt, c.cloned())
}

func (s *MemoryChannel) GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, false, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	c, ok := s.room.channels[channelKey(tnt, roomID)][channelID]
	if !ok || c.DeletedAtMs != nil {
		return RoomChannel{}, false, nil
	}
	return c.cloned(), true, nil
}

func (s *MemoryChannel) ListChannels(ctx context.Context, roomID string) ([]RoomChannel, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := make([]RoomChannel, 0, len(s.room.channels[channelKey(tnt, roomID)]))
	for _, c := range s.room.channels[channelKey(tnt, roomID)] {
		if c.DeletedAtMs != nil {
			continue
		}
		out = append(out, c.cloned())
	}
	sortChannels(out)
	return out, nil
}

func (s *MemoryChannel) DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, false, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	for _, c := range s.room.channels[channelKey(tnt, roomID)] {
		if c.IsDefault && c.DeletedAtMs == nil {
			return c.cloned(), true, nil
		}
	}
	return RoomChannel{}, false, nil
}

func (s *MemoryChannel) DeleteChannel(ctx context.Context, roomID, channelID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	room := s.room.channels[channelKey(tnt, roomID)]
	c, ok := room[channelID]
	if !ok || c.DeletedAtMs != nil {
		return nil
	}
	c.DeletedAtMs = &atMs
	room[channelID] = c
	return nil
}

// liveChannelLocked returns the live channel channelID of the tenant's room
// and its table; callers hold s.room.mu.
func (s *MemoryChannel) liveChannelLocked(tnt, roomID, channelID string) (map[string]RoomChannel, RoomChannel, bool) {
	room := s.room.channels[channelKey(tnt, roomID)]
	c, ok := room[channelID]
	return room, c, ok && c.DeletedAtMs == nil
}

func (s *MemoryChannel) UpdateChannel(ctx context.Context, roomID, channelID string, patch ChannelPatch) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	room, c, ok := s.liveChannelLocked(tnt, roomID, channelID)
	if !ok {
		return ErrChannelNotFound
	}
	applyChannelPatch(&c, patch)
	room[channelID] = c.cloned()
	return nil
}

func (s *MemoryChannel) SetChannelArchived(ctx context.Context, roomID, channelID string, atMs *int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	room, c, ok := s.liveChannelLocked(tnt, roomID, channelID)
	if !ok {
		return ErrChannelNotFound
	}
	switch {
	case atMs == nil:
		c.ArchivedAtMs = nil
	case c.ArchivedAtMs == nil:
		at := *atMs
		c.ArchivedAtMs = &at
	}
	room[channelID] = c
	return nil
}

func (s *MemoryChannel) EnsurePeopleChannel(ctx context.Context, roomID, creator string, atMs int64) (RoomChannel, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	id := peopleChannelID(tnt, roomID)
	if c, ok := s.room.channels[channelKey(tnt, roomID)][id]; ok {
		return c.cloned(), nil
	}
	c := RoomChannel{
		ChannelID: id, TenantID: tnt, RoomID: roomID, Name: PeopleChannelName,
		Kind: ChannelKindPeople, CreatedBy: creator, OwnerID: creator, CreatedAtMs: atMs,
	}
	if err := s.room.putChannelLocked(tnt, c); err != nil {
		return RoomChannel{}, err
	}
	return c, nil
}

func (s *MemoryChannel) ChannelAdmission(ctx context.Context, roomID, channelID string) (ChannelAdmission, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return ChannelAdmission{}, false, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	c, ok := s.room.channels[channelKey(tnt, roomID)][channelID]
	if !ok {
		return ChannelAdmission{}, false, nil
	}
	return c.Admission(), true, nil
}

func (s *MemoryChannel) PutSection(ctx context.Context, sec RoomSection) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	for _, byID := range s.room.sections {
		if _, taken := byID[sec.SectionID]; taken {
			return ErrSectionExists
		}
	}
	sec.TenantID = tnt
	k := channelKey(tnt, sec.RoomID)
	if s.room.sections[k] == nil {
		s.room.sections[k] = map[string]RoomSection{}
	}
	s.room.sections[k][sec.SectionID] = sec.cloned()
	return nil
}

func (s *MemoryChannel) GetSection(ctx context.Context, roomID, sectionID string) (RoomSection, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomSection{}, false, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	sec, ok := s.room.sections[channelKey(tnt, roomID)][sectionID]
	if !ok || sec.DeletedAtMs != nil {
		return RoomSection{}, false, nil
	}
	return sec.cloned(), true, nil
}

func (s *MemoryChannel) ListSections(ctx context.Context, roomID string) ([]RoomSection, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := []RoomSection{}
	for _, sec := range s.room.sections[channelKey(tnt, roomID)] {
		if sec.DeletedAtMs == nil {
			out = append(out, sec.cloned())
		}
	}
	sortSections(out)
	return out, nil
}

func (s *MemoryChannel) UpdateSection(ctx context.Context, roomID, sectionID string, patch SectionPatch) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	room := s.room.sections[channelKey(tnt, roomID)]
	sec, ok := room[sectionID]
	if !ok || sec.DeletedAtMs != nil {
		return ErrSectionNotFound
	}
	if patch.Name != nil {
		sec.Name = *patch.Name
	}
	if patch.Rules != nil {
		sec.Rules = *patch.Rules
	}
	if patch.Position != nil {
		sec.Position = *patch.Position
	}
	if patch.Owners != nil {
		sec.Owners = *patch.Owners
	}
	room[sectionID] = sec.cloned()
	return nil
}

func (s *MemoryChannel) DeleteSection(ctx context.Context, roomID, sectionID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := channelKey(tnt, roomID)
	sec, ok := s.room.sections[k][sectionID]
	if !ok || sec.DeletedAtMs != nil {
		return nil
	}
	sec.DeletedAtMs = &atMs
	s.room.sections[k][sectionID] = sec
	for id, c := range s.room.channels[k] {
		if c.SectionID == sectionID {
			c.SectionID = ""
			s.room.channels[k][id] = c
		}
	}
	return nil
}

func (s *MemoryChannel) FollowChannel(ctx context.Context, roomID, channelID, userID string, atMs int64, marks FollowMarks) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := followKey(tnt, roomID, channelID, userID)
	f, ok := s.room.follows[k]
	if !ok {
		f = ChannelFollow{TenantID: tnt, RoomID: roomID, ChannelID: channelID, UserID: userID, JoinedAtMs: atMs}
	}
	if marks.Starred != nil {
		f.Starred = *marks.Starred
	}
	if marks.Hidden != nil {
		f.Hidden = *marks.Hidden
	}
	s.room.follows[k] = f
	return nil
}

func (s *MemoryChannel) UnfollowChannel(ctx context.Context, roomID, channelID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	delete(s.room.follows, followKey(tnt, roomID, channelID, userID))
	return nil
}

func (s *MemoryChannel) ChannelFollows(ctx context.Context, roomID, userID string) ([]ChannelFollow, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := []ChannelFollow{}
	for _, f := range s.room.follows {
		if f.TenantID == tnt && f.RoomID == roomID && f.UserID == userID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out, nil
}

func (s *MemoryChannel) ChannelFollowerCounts(ctx context.Context, roomID string) (map[string]int, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := map[string]int{}
	for _, f := range s.room.follows {
		if f.TenantID == tnt && f.RoomID == roomID {
			out[f.ChannelID]++
		}
	}
	return out, nil
}

func (s *MemoryChannel) ClaimChannelPost(ctx context.Context, roomID, channelID, userID string, atMs, minGapMs int64) (int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := followKey(tnt, roomID, channelID, userID)
	f, ok := s.room.follows[k]
	if !ok {
		f = ChannelFollow{TenantID: tnt, RoomID: roomID, ChannelID: channelID, UserID: userID, JoinedAtMs: atMs}
	}
	if minGapMs > 0 && ok && f.LastPostedAtMs > atMs-minGapMs {
		return f.LastPostedAtMs + minGapMs - atMs, nil
	}
	f.LastPostedAtMs = max(f.LastPostedAtMs, atMs)
	s.room.follows[k] = f
	return 0, nil
}
