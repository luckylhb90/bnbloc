package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// RoomChannel is a named message stream inside a room. A channel partitions
// only the message stream; room-level state (members, agent borrows, git sync,
// files) is shared across all channels in the room. Exactly one channel per
// room is the default, which receives messages that are not addressed to a
// specific channel. A channel is soft-deleted by stamping DeletedAtMs, so its
// history remains addressable while it disappears from live listings.
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
	Kind        string
	CreatedBy   string
	CreatedAtMs int64
	DeletedAtMs *int64
}

// Channel kinds. The kind is fixed at creation.
const (
	ChannelKindStandard = ""
	ChannelKindPeople   = "people"
)

// IsPeople reports whether c is the room's people chat.
func (c RoomChannel) IsPeople() bool { return c.Kind == ChannelKindPeople }

// PeopleChannelName is the display name stored on a room's people chat.
const PeopleChannelName = "People chat"

// peopleChannelID is the room's people-chat channel id, derived from its
// (tenant, room) so concurrent lazy creates converge on ONE row (the id is
// the primary key): there is exactly one people chat per room by construction.
func peopleChannelID(tenantID, roomID string) string {
	sum := sha256.Sum256([]byte(ChannelKindPeople + "\x00" + tenantID + "\x00" + roomID))
	return "pc_" + hex.EncodeToString(sum[:8])
}

// ErrChannelNotFound is returned by RenameChannel when the target channel is
// absent or soft-deleted: rename is conditional on liveness, so a rename
// racing a delete can never resurrect the row the delete just retired.
var ErrChannelNotFound = errors.New("store: channel not found")

// ErrChannelExists is returned by PutChannel when the channel id is already
// taken, in any room of any tenant.
var ErrChannelExists = errors.New("store: channel id already exists")

// ChannelStore persists a room's channels. Implementations must be safe for
// concurrent use and must scope every operation by the tenant bound to the
// context (room ids may be reused across tenants), failing closed when the
// context carries none.
type ChannelStore interface {
	// PutChannel creates a channel; ErrChannelExists when the id is taken
	// (channel ids are global, so it never rewrites an existing row).
	PutChannel(ctx context.Context, c RoomChannel) error
	// GetChannel returns a live channel by id within a room, found=false when
	// absent or soft-deleted.
	GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool, error)
	// ListChannels returns a room's live channels of every kind (the people
	// chat included), default first then by created_at ascending. Callers that
	// present or copy ordinary channels filter IsPeople themselves.
	ListChannels(ctx context.Context, roomID string) ([]RoomChannel, error)
	// DefaultChannel returns a room's default channel, found=false when none.
	DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool, error)
	// DeleteChannel soft-deletes a channel. Deleting an absent channel is a
	// no-op; callers enforce the default-channel guard.
	DeleteChannel(ctx context.Context, roomID, channelID string, atMs int64) error
	// RenameChannel renames a live channel, returning ErrChannelNotFound when
	// it is absent or soft-deleted — unlike PutChannel, it never resurrects a
	// deleted row.
	RenameChannel(ctx context.Context, roomID, channelID, name string) error
	// EnsurePeopleChannel returns the room's people chat, creating it (as
	// creator, at atMs) when absent. Idempotent and race-free: concurrent
	// callers get the same channel.
	EnsurePeopleChannel(ctx context.Context, roomID, creator string, atMs int64) (RoomChannel, error)
	// ChannelKind returns the kind of a channel of the room, soft-deleted or
	// not; found=false when no such channel exists. Unlike GetChannel it
	// reports read failures, so a delivery filter can fail closed.
	ChannelKind(ctx context.Context, roomID, channelID string) (kind string, found bool, err error)
}

// ChannelKindReader is the read ChannelAdmitsAgents needs.
type ChannelKindReader interface {
	ChannelKind(ctx context.Context, roomID, channelID string) (kind string, found bool, err error)
}

// ChannelAdmitsAgents is the ONE people-chat rule: an agent may read and
// write a channel of the room unless it is the room's people chat. The
// room-wide "" is no channel, and a nil reader (in-memory dev) admits. A
// read failure is returned, never "admits": every guard built on it fails
// closed (realtime's delivery filters and agent ingests, the handoff import,
// activity before it stores an agent reply).
func ChannelAdmitsAgents(ctx context.Context, r ChannelKindReader, roomID, channelID string) (bool, error) {
	if channelID == "" || r == nil {
		return true, nil
	}
	kind, _, err := r.ChannelKind(ctx, roomID, channelID)
	if err != nil {
		return false, err
	}
	return kind != ChannelKindPeople, nil
}

// MemoryChannel is an in-memory ChannelStore. It is a view over a MemoryRoom's
// channel table sharing that store's mutex, so CreateRoomWithOwnerAndChannel
// inserts the room, its owner, and the default channel in one critical
// section. NewMemoryChannel allocates a private backing room for standalone
// use; channels created together with a room are visible only to the view from
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
	return s.room.putChannelLocked(tnt, c)
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
	return c, true, nil
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
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].CreatedAtMs < out[j].CreatedAtMs
	})
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
			return c, true, nil
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
	if !ok {
		return nil
	}
	c.DeletedAtMs = &atMs
	room[channelID] = c
	return nil
}

func (s *MemoryChannel) RenameChannel(ctx context.Context, roomID, channelID, name string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	room := s.room.channels[channelKey(tnt, roomID)]
	c, ok := room[channelID]
	if !ok || c.DeletedAtMs != nil {
		return ErrChannelNotFound
	}
	c.Name = name
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
		return c, nil
	}
	c := RoomChannel{
		ChannelID: id, TenantID: tnt, RoomID: roomID, Name: PeopleChannelName,
		Kind: ChannelKindPeople, CreatedBy: creator, CreatedAtMs: atMs,
	}
	if err := s.room.putChannelLocked(tnt, c); err != nil {
		return RoomChannel{}, err
	}
	return c, nil
}

func (s *MemoryChannel) ChannelKind(ctx context.Context, roomID, channelID string) (string, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return "", false, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	c, ok := s.room.channels[channelKey(tnt, roomID)][channelID]
	if !ok {
		return "", false, nil
	}
	return c.Kind, true, nil
}
