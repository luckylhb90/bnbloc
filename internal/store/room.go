package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room domain errors.
var (
	ErrRoomNotFound   = errors.New("store: room not found")
	ErrInviteNotFound = errors.New("store: invite not found")
	// ErrInviteExists refuses an invite code already taken (codes are
	// global: the primary key carries no tenant).
	ErrInviteExists     = errors.New("store: invite code already exists")
	ErrInviteExpired    = errors.New("store: invite expired")
	ErrInviteFull       = errors.New("store: invite capacity reached")
	ErrNotShared        = errors.New("store: room is not shared")
	ErrOwnerCannotLeave = errors.New("store: owner cannot leave; transfer or delete")
	// ErrNewOwnerNotMember refuses TransferOwner to a user who is not a
	// human member of the room.
	ErrNewOwnerNotMember = errors.New("store: new owner is not a member")
	// ErrRoomExists rejects creating a room whose id is already taken. Room
	// ids are globally unique (rooms' primary key is room_id alone) and never
	// reused, even after a delete, so a create can never adopt — and take
	// ownership of — another room, in this tenant or any other.
	ErrRoomExists = errors.New("store: room id already exists")
)

// Role is a member's role within a room.
type Role string

// Room roles.
const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RoleVisitor Role = "visitor"
)

// CanInvite reports whether a role may create invites.
func (r Role) CanInvite() bool { return r == RoleOwner || r == RoleAdmin }

// Visibility controls how a room is discovered and joined.
type Visibility string

// Room visibilities.
const (
	// VisibilityShared rooms are joinable directly from the plaza, no invite.
	VisibilityShared Visibility = "shared"
	// VisibilityPrivate rooms require an invite code and stay locked in the plaza.
	VisibilityPrivate Visibility = "private"
)

// DefaultPreviewable is whether a new room is readable before joining when
// its creator says nothing: a shared room is (anyone could join and read it
// one click later), a private one is not. Visibility says how a room is
// joined; Previewable, who may read it first, is the owner's own switch from
// then on (docs/prd/2026-10-22-shared-room-preview.md).
func DefaultPreviewable(v Visibility) bool { return v == VisibilityShared }

// NormalizeVisibility maps arbitrary input to a known visibility, defaulting to
// private (the safe default for a newly created room).
func NormalizeVisibility(v string) Visibility {
	if Visibility(strings.TrimSpace(v)) == VisibilityShared {
		return VisibilityShared
	}
	return VisibilityPrivate
}

// SyncMode selects how members of a room converge their project code.
type SyncMode string

// Room sync modes.
const (
	// SyncModeGit: members share a Git remote; convergence is commit/push/pull.
	// The room coordinates; Git owns the code. Works today.
	SyncModeGit SyncMode = "git"
	// SyncModeMutagen: bridges mirror a project directory in real time over a
	// relay tunnel. Experimental; requires per-member bridges.
	SyncModeMutagen SyncMode = "mutagen"
	// SyncModeNone: no code sync; the room is chat/agent visibility only.
	SyncModeNone SyncMode = "none"
)

// NormalizeSyncMode maps arbitrary input to a known sync mode, defaulting to
// git (the only mode that works with no extra infrastructure).
func NormalizeSyncMode(v string) SyncMode {
	switch SyncMode(strings.TrimSpace(v)) {
	case SyncModeMutagen:
		return SyncModeMutagen
	case SyncModeNone:
		return SyncModeNone
	default:
		return SyncModeGit
	}
}

// Room is a collaboration scope. RoomID == workspaceId. Welcome and
// QuickPrompts are what the empty room shows its members; GetRoom reads
// them, the listings do not.
type Room struct {
	TenantID    string
	RoomID      string
	Name        string
	Description string
	Visibility  Visibility
	// Previewable lets any signed-in member of the room's organization read
	// the room without joining it, read-only (membership.Previewer). It is
	// independent of Visibility, which only says how the room is joined.
	Previewable  bool
	SyncMode     SyncMode
	CreatedBy    string
	CreatedAtMs  int64
	Welcome      string
	QuickPrompts []wire.RoomQuickPrompt
	// The room's rules and onboarding (docs/prd/2026-10-28-channels-and-
	// threads.md §2.5a): the rules text, the version a change to it bumps,
	// whether a member agrees to the current version before posting (C16,
	// off by default), the channels a new member joins, and who creates
	// channels (C15, wire.ChannelCreation*; "" reads as anyone).
	Rules                 string
	RulesVersion          int
	RequireRulesAgreement bool
	RecommendedChannels   []string
	ChannelCreation       string
}

// ChannelCreationPolicy is the room's channel creation policy, its default
// (C15: anyone) when unset.
func (r Room) ChannelCreationPolicy() string {
	if r.ChannelCreation == "" {
		return wire.ChannelCreationAnyone
	}
	return r.ChannelCreation
}

// RulesAgreementDue reports whether m must agree to the room's current rules
// before posting (C16): the room requires it, it has rules (a version), and
// m agreed to an older version or none. A room that requires agreeing but
// never had rules (version 0) asks nothing (decision: pending owner; there
// is nothing to agree to).
func (r Room) RulesAgreementDue(m Membership) bool {
	return r.RequireRulesAgreement && r.RulesVersion > 0 && m.RulesAgreedVersion < r.RulesVersion
}

// RoomWithCount augments a Room with its current member count for plaza rows.
type RoomWithCount struct {
	Room
	MemberCount int
}

// RoomPatch is a partial update to a room. Nil fields are left unchanged.
type RoomPatch struct {
	Name         *string
	Description  *string
	Visibility   *Visibility
	Previewable  *bool
	SyncMode     *SyncMode
	Welcome      *string
	QuickPrompts *[]wire.RoomQuickPrompt
	// Rules replaces the room's rules; a text that differs from the current
	// one bumps RulesVersion (in the same write).
	Rules                 *string
	RequireRulesAgreement *bool
	RecommendedChannels   *[]string
	ChannelCreation       *string
}

// RoomMembershipDetail is a rich per-user room row for list differentiation.
type RoomMembershipDetail struct {
	Room
	Role        Role
	IsOwner     bool
	MemberCount int
}

// Membership records a user's role in a room.
type Membership struct {
	TenantID   string
	RoomID     string
	UserID     string
	Role       Role
	JoinedAtMs int64
	// AgentHost marks the hidden membership a platform agent's host holds in
	// a room the agent was added to (docs/prd/2026-10-10-zero-install-
	// onboarding.md §2). It lets the host's bridge read the room and post its
	// agent's replies, but it is not a human membership: it is left out of
	// the host's room lists and of member counts, carries the visitor role,
	// and every human-only surface (chat, presence, notifications, plugin
	// events, the people chat) refuses it. Any human membership of the same
	// user replaces it.
	AgentHost bool
	// RulesAgreedVersion is the version of the room's rules the member last
	// agreed to (0 = never; C16).
	RulesAgreedVersion int
}

// Human reports a human membership (not an agent host's): the Go side of
// sqlHumanMember.
func (m Membership) Human() bool { return !m.AgentHost }

// Invite is a room invite code with expiry and capacity.
type Invite struct {
	Code          string
	TenantID      string
	RoomID        string
	Role          Role
	ExpiresAtMs   int64
	Capacity      int
	RedeemedCount int
	CreatedBy     string
}

// RoomStore persists rooms, memberships, and invites. Implementations must be
// safe for concurrent use. Keys are the roomId scope (== workspaceId).
type RoomStore interface {
	// CreateRoom inserts a new room. It fails with ErrRoomExists when the id is
	// taken in any tenant (including a deleted room): creation never upserts.
	CreateRoom(ctx context.Context, r Room) error
	// CreateRoomWithOwner atomically creates a room and inserts its owner
	// membership in a single transaction. If the owner insert fails the room
	// creation is rolled back.
	CreateRoomWithOwner(ctx context.Context, r Room, owner Membership) error
	GetRoom(ctx context.Context, roomID string) (Room, error)
	// RoomTenant returns a room's authoritative tenant keyed on room_id alone
	// (globally unique), so a room-scoped request can resolve its tenant without
	// already knowing it. false when the room is absent/deleted.
	RoomTenant(ctx context.Context, roomID string) (string, bool, error)
	// DeletedRooms returns the ids among roomIDs whose room was deleted (its
	// soft-delete marker set), in any tenant: positive evidence of a
	// deletion, never the absence of a room (an unknown or live id is not
	// returned). A member's bridge removes a deleted room's agent
	// workspaces on it (docs/bugs/2026-10-22-deleted-room-workspaces-never-removed.md).
	DeletedRooms(ctx context.Context, roomIDs []string) ([]string, error)
	// CreateRoomWithOwnerAndChannel atomically creates a room, its owner
	// membership, and the room's default channel in a single transaction
	// (design lock: a room always exists with exactly one default channel). If
	// any insert fails the whole create is rolled back.
	CreateRoomWithOwnerAndChannel(ctx context.Context, r Room, owner Membership, defaultChannel RoomChannel) error
	// AddMember inserts or updates a human membership of a live room of the
	// ctx tenant; ErrRoomNotFound when the tenant has no such room. A re-add
	// changes only the role (the join time stays) and turns an agent-host
	// membership into a human one.
	AddMember(ctx context.Context, m Membership) error
	RemoveMember(ctx context.Context, roomID, userID string) error
	// MemberRole is GetMember's role (an agent host reads as a visitor).
	MemberRole(ctx context.Context, roomID, userID string) (Role, bool, error)
	// GetMember returns a user's membership of a room in the ctx tenant,
	// agent-host memberships included (Membership.AgentHost).
	GetMember(ctx context.Context, roomID, userID string) (Membership, bool, error)
	IsMember(ctx context.Context, roomID, userID string) (bool, error)
	// MembershipAnyTenant returns the membership keyed on (room_id, user_id)
	// alone, ignoring the caller's tenant — the room-scope fallback for a
	// member whose room lives in a different tenant than their token. Only a
	// membership in the room's own tenant, of a live room, counts.
	MembershipAnyTenant(ctx context.Context, roomID, userID string) (Membership, bool, error)
	// Members returns every membership of a room, agent hosts flagged, in
	// join order (then user id).
	Members(ctx context.Context, roomID string) ([]Membership, error)
	// RoomsForUser returns the rooms the user is a human member of (agent-host
	// memberships are not the user's rooms), by room id.
	RoomsForUser(ctx context.Context, userID string) ([]string, error)
	// RoomsForUserAllTenants returns every room the user is a human member of
	// across all tenants — the bridge discovery shape. The bridge subscribes,
	// advertises, and claims in each of the owner's rooms, so a session token
	// minted in one tenant must not hide rooms in another. UI listings use the
	// tenant-scoped RoomsForUser. Ordered by room id.
	RoomsForUserAllTenants(ctx context.Context, userID string) ([]string, error)
	// AgentHostRoomsAllTenants returns the live rooms, across all tenants,
	// where the user holds an agent-host membership (the bridge's
	// includeAgentHost discovery).
	AgentHostRoomsAllTenants(ctx context.Context, userID string) ([]string, error)
	// AddAgentHost gives userID an agent-host membership of roomID unless they
	// already hold any membership there (a human membership is never
	// downgraded). ErrRoomNotFound when the room is absent or deleted.
	AddAgentHost(ctx context.Context, roomID, userID string, atMs int64) error
	// RemoveAgentHost deletes userID's agent-host membership of roomID; a
	// human membership is never touched. Absent is not an error.
	RemoveAgentHost(ctx context.Context, roomID, userID string) error
	// CreateInvite stores a new invite code; ErrInviteExists when the code is
	// taken in any tenant (it is never rewritten).
	CreateInvite(ctx context.Context, inv Invite) error
	// PurgeDeadInvites deletes the invites no one can redeem any more (expired
	// at nowMs, or used to their capacity), in every tenant: invite codes are
	// one namespace and a short code is a scarce one, so a creator frees the
	// dead ones before drawing.
	PurgeDeadInvites(ctx context.Context, nowMs int64) error
	// RedeemInvite atomically validates and consumes an invite for a user,
	// returning the created membership. Redeeming for an already-member user is
	// idempotent and does not increment the redeemed count. An invite is
	// live strictly before ExpiresAtMs (liveAt): at nowMs == ExpiresAtMs it
	// is ErrInviteExpired.
	RedeemInvite(ctx context.Context, code, userID string, nowMs int64) (Membership, error)
	// SearchRooms returns non-deleted rooms whose name or description contains
	// the query (case-insensitive, taken literally: % and _ are no
	// wildcards), newest-first (then room id), with member counts. An empty
	// query returns all rooms. total is the match count before paging; a page
	// holds clampLimit(limit, DefaultSearchRoomsPage, MaxSearchRoomsPage)
	// rooms.
	SearchRooms(ctx context.Context, query string, limit, offset int) (rooms []RoomWithCount, total int, err error)
	// UpdateRoom applies a partial patch (name/description/visibility).
	UpdateRoom(ctx context.Context, roomID string, patch RoomPatch) error
	// DeleteRoom soft-deletes a room (its id is never reused) and removes every
	// row that belongs to it: memberships, invites, channels, messages, read
	// state, agent sessions/messages/jobs, grants/leases/audit, attachments
	// metadata, artifacts and their versions, and agent prefs.
	DeleteRoom(ctx context.Context, roomID string) error
	// TransferOwner makes newOwner the room owner and demotes the prior owner to
	// member. newOwner must already be a human member (not an agent host):
	// ErrNewOwnerNotMember otherwise; ErrRoomNotFound for a room not live here.
	TransferOwner(ctx context.Context, roomID, newOwner string) error
	// RoomsForUserDetailed returns rich rows for the user's rooms (human
	// memberships only), newest room first (then room id).
	RoomsForUserDetailed(ctx context.Context, userID string) ([]RoomMembershipDetail, error)
	// MemberCount returns the number of human members in a room.
	MemberCount(ctx context.Context, roomID string) (int, error)
	// AgreeRoomRules records that userID, a human member, agreed to version
	// of the room's rules. ErrRulesChanged when version is not the room's
	// current one (nobody agrees to rules they did not read), ErrRoomNotFound
	// when the room is not live here or userID is no human member of it.
	AgreeRoomRules(ctx context.Context, roomID, userID string, version int) error
}

// ErrRulesChanged refuses an agreement to a version of a room's rules that is
// not its current one.
var ErrRulesChanged = errors.New("store: the room's rules changed")

// Plaza (SearchRooms) page sizes: clampLimit(limit, DefaultSearchRoomsPage,
// MaxSearchRoomsPage) on both backends.
const (
	DefaultSearchRoomsPage = 50
	MaxSearchRoomsPage     = 200
)

func searchRoomsPage(limit int) int {
	return clampLimit(limit, DefaultSearchRoomsPage, MaxSearchRoomsPage)
}

// MemoryRoom is an in-memory RoomStore for M0/M1 and tests.
type MemoryRoom struct {
	mu       sync.Mutex
	rooms    map[string]Room
	members  map[string]map[string]Membership // roomID -> userID -> membership
	invites  map[string]Invite
	deleted  map[string]bool                   // roomID -> soft-deleted
	channels map[string]map[string]RoomChannel // channelKey -> channelID -> channel
	sections map[string]map[string]RoomSection // channelKey -> sectionID -> section
	follows  map[string]ChannelFollow          // followKey -> follow
	groups   map[string]ChannelGroup           // groupKey -> a member's group
	// sectionMembers and sectionRequests: a members-only section's members
	// and open join requests (sectionMemberKey).
	sectionMembers  map[string]SectionMember
	sectionRequests map[string]SectionJoinRequest
	// cascade are the memory stores DeleteRoom clears (cascadeDeleteTo).
	cascade []roomRowsOwner
}

// NewMemoryRoom constructs an empty in-memory room store.
func NewMemoryRoom() *MemoryRoom {
	return &MemoryRoom{
		rooms:    map[string]Room{},
		members:  map[string]map[string]Membership{},
		invites:  map[string]Invite{},
		deleted:  map[string]bool{},
		channels: map[string]map[string]RoomChannel{},
		sections: map[string]map[string]RoomSection{},
		follows:  map[string]ChannelFollow{},
		groups:   map[string]ChannelGroup{},

		sectionMembers:  map[string]SectionMember{},
		sectionRequests: map[string]SectionJoinRequest{},
	}
}

// Channels returns a ChannelStore view over this room store's channel table.
// The memory backends (NewBackends) wire Room and Channel this way so the
// default channel inserted by CreateRoomWithOwnerAndChannel is immediately
// visible to channel reads under the same lock.
func (s *MemoryRoom) Channels() *MemoryChannel { return &MemoryChannel{room: s} }

// tkey composes a tenant-scoped map key so two tenants sharing an id (roomID,
// invite code, …) never collide or cross-read in the in-memory store.
func tkey(tenantID, id string) string { return tenantID + "\x00" + id }

// CreateRoom stores a new room, defaulting visibility to private.
func (s *MemoryRoom) CreateRoom(ctx context.Context, r Room) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roomIDTakenLocked(r.RoomID) {
		return ErrRoomExists
	}
	if r.Visibility == "" {
		r.Visibility = VisibilityPrivate
	}
	if r.SyncMode == "" {
		r.SyncMode = SyncModeGit
	}
	r.TenantID = tid
	r.QuickPrompts = slices.Clone(r.QuickPrompts)
	r.RecommendedChannels = slices.Clone(r.RecommendedChannels)
	r.ChannelCreation = r.ChannelCreationPolicy()
	s.rooms[tkey(tid, r.RoomID)] = r
	return nil
}

// roomIDTakenLocked reports whether any tenant holds a room (live or deleted)
// with this id, mirroring MySQL's room_id primary key. Callers hold s.mu.
func (s *MemoryRoom) roomIDTakenLocked(roomID string) bool {
	_, ok := s.roomKeyLocked(roomID)
	return ok
}

// roomKeyLocked returns the tenant-scoped map key of the room with this id in
// whichever tenant holds it. Callers hold s.mu.
func (s *MemoryRoom) roomKeyLocked(roomID string) (string, bool) {
	suffix := "\x00" + roomID
	for key := range s.rooms {
		if strings.HasSuffix(key, suffix) {
			return key, true
		}
	}
	return "", false
}

// liveRoomKeyLocked is roomKeyLocked restricted to a non-deleted room.
func (s *MemoryRoom) liveRoomKeyLocked(roomID string) (string, bool) {
	key, ok := s.roomKeyLocked(roomID)
	if !ok || s.deleted[key] {
		return "", false
	}
	return key, true
}

// roomLive reports whether the tenant's room exists and is not deleted.
func (s *MemoryRoom) roomLive(tid, roomID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.liveRoomLocked(tid, roomID)
	return ok
}

// liveRoomLocked returns the map key of tid's live room roomID: the one
// liveness check of the tenant-scoped methods (MySQL's tenant filter plus
// sqlNotDeleted). Callers hold s.mu.
func (s *MemoryRoom) liveRoomLocked(tid, roomID string) (string, bool) {
	rk := tkey(tid, roomID)
	_, ok := s.rooms[rk]
	return rk, ok && !s.deleted[rk]
}

// CreateRoomWithOwner atomically creates a room and inserts its owner
// membership. Both maps are updated under the same lock; if the owner add
// fails the room is rolled back.
func (s *MemoryRoom) CreateRoomWithOwner(ctx context.Context, r Room, owner Membership) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createRoomWithOwnerLocked(tid, r, owner)
}

// createRoomWithOwnerLocked inserts a room and its owner membership; callers
// hold s.mu. If the owner add fails the room is rolled back.
func (s *MemoryRoom) createRoomWithOwnerLocked(tid string, r Room, owner Membership) error {
	if s.roomIDTakenLocked(r.RoomID) {
		return ErrRoomExists
	}
	if r.Visibility == "" {
		r.Visibility = VisibilityPrivate
	}
	if r.SyncMode == "" {
		r.SyncMode = SyncModeGit
	}
	r.TenantID = tid
	r.QuickPrompts = slices.Clone(r.QuickPrompts)
	r.RecommendedChannels = slices.Clone(r.RecommendedChannels)
	r.ChannelCreation = r.ChannelCreationPolicy()
	rk := tkey(tid, r.RoomID)
	s.rooms[rk] = r
	owner.TenantID = tid
	if err := s.addMemberLocked(tid, owner); err != nil {
		delete(s.rooms, rk)
		return err
	}
	return nil
}

// CreateRoomWithOwnerAndChannel atomically creates a room, its owner
// membership, and the room's default channel. All three land under one lock,
// so a room can never be observed without its default channel.
func (s *MemoryRoom) CreateRoomWithOwnerAndChannel(ctx context.Context, r Room, owner Membership, def RoomChannel) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.createRoomWithOwnerLocked(tid, r, owner); err != nil {
		return err
	}
	return s.putChannelLocked(tid, def)
}

// putChannelLocked upserts a channel keyed by the context tenant; callers
// hold s.mu.
func (s *MemoryRoom) putChannelLocked(tid string, c RoomChannel) error {
	c.TenantID = tid
	k := channelKey(tid, c.RoomID)
	if s.channels[k] == nil {
		s.channels[k] = map[string]RoomChannel{}
	}
	s.channels[k][c.ChannelID] = c
	return nil
}

// RoomTenant returns a room's tenant regardless of the caller's tenant, keyed
// on room_id alone (globally unique). false when absent/deleted.
func (s *MemoryRoom) RoomTenant(_ context.Context, roomID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.liveRoomKeyLocked(roomID)
	if !ok {
		return "", false, nil
	}
	return s.rooms[key].TenantID, true, nil
}

// DeletedRooms returns the soft-deleted rooms among roomIDs, in any tenant.
func (s *MemoryRoom) DeletedRooms(_ context.Context, roomIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range roomIDs {
		if key, ok := s.roomKeyLocked(id); ok && s.deleted[key] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// GetRoom returns a room or ErrRoomNotFound. Soft-deleted rooms are not found.
func (s *MemoryRoom) GetRoom(ctx context.Context, roomID string) (Room, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Room{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, ok := s.liveRoomLocked(tid, roomID)
	if !ok {
		return Room{}, ErrRoomNotFound
	}
	r := s.rooms[rk]
	r.QuickPrompts = slices.Clone(r.QuickPrompts)
	r.RecommendedChannels = slices.Clone(r.RecommendedChannels)
	return r, nil
}

// AddMember inserts or updates a human membership of a live room of the ctx
// tenant (ErrRoomNotFound otherwise). Like the MySQL upsert, a re-add changes
// only the role, keeps the original join time, and turns an agent-host row
// into a human one.
func (s *MemoryRoom) AddMember(ctx context.Context, m Membership) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, ok := s.liveRoomLocked(tid, m.RoomID)
	if !ok {
		return ErrRoomNotFound
	}
	if cur, ok := s.members[rk][m.UserID]; ok {
		m.JoinedAtMs = cur.JoinedAtMs
		m.RulesAgreedVersion = cur.RulesAgreedVersion
	}
	m.AgentHost = false
	return s.addMemberLocked(tid, m)
}

func (s *MemoryRoom) addMemberLocked(tid string, m Membership) error {
	m.TenantID = tid
	rk := tkey(tid, m.RoomID)
	if s.members[rk] == nil {
		s.members[rk] = map[string]Membership{}
	}
	s.members[rk][m.UserID] = m
	return nil
}

// RemoveMember deletes a membership.
func (s *MemoryRoom) RemoveMember(ctx context.Context, roomID, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.members[tkey(tid, roomID)]; set != nil {
		delete(set, userID)
	}
	return nil
}

// GetMember returns a user's membership of a room.
func (s *MemoryRoom) GetMember(ctx context.Context, roomID, userID string) (Membership, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Membership{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[tkey(tid, roomID)][userID]
	return m, ok, nil
}

// MemberRole returns a user's role in a room.
func (s *MemoryRoom) MemberRole(ctx context.Context, roomID, userID string) (Role, bool, error) {
	m, ok, err := s.GetMember(ctx, roomID, userID)
	return m.Role, ok, err
}

// IsMember reports membership.
func (s *MemoryRoom) IsMember(ctx context.Context, roomID, userID string) (bool, error) {
	_, ok, err := s.GetMember(ctx, roomID, userID)
	return ok, err
}

// MembershipAnyTenant returns the membership of the given room regardless of
// the caller's tenant. Room ids are globally unique, so the room resolves to
// the one tenant that holds it, and only a membership in THAT tenant of a live
// room counts — a membership row filed under any other tenant is never honored.
func (s *MemoryRoom) MembershipAnyTenant(ctx context.Context, roomID, userID string) (Membership, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.liveRoomKeyLocked(roomID)
	if !ok {
		return Membership{}, false, nil
	}
	m, member := s.members[key][userID]
	return m, member, nil
}

// AddAgentHost adds an agent-host membership unless userID is already a
// member of the room.
func (s *MemoryRoom) AddAgentHost(ctx context.Context, roomID, userID string, atMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, live := s.liveRoomLocked(tid, roomID)
	if !live {
		return ErrRoomNotFound
	}
	if _, member := s.members[rk][userID]; member {
		return nil
	}
	return s.addMemberLocked(tid, Membership{RoomID: roomID, UserID: userID, Role: RoleVisitor, JoinedAtMs: atMs, AgentHost: true})
}

// RemoveAgentHost deletes an agent-host membership (never a human one).
func (s *MemoryRoom) RemoveAgentHost(ctx context.Context, roomID, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.members[tkey(tid, roomID)]; set != nil && !set[userID].Human() {
		delete(set, userID)
	}
	return nil
}

// Members returns all memberships for a room.
func (s *MemoryRoom) Members(ctx context.Context, roomID string) ([]Membership, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.members[tkey(tid, roomID)]
	out := make([]Membership, 0, len(set))
	for _, m := range set {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].JoinedAtMs != out[j].JoinedAtMs {
			return out[i].JoinedAtMs < out[j].JoinedAtMs
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

// RoomsForUser returns the room ids the user is a member of.
func (s *MemoryRoom) RoomsForUser(ctx context.Context, userID string) ([]string, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, set := range s.members {
		if m, ok := set[userID]; ok && m.TenantID == tid && m.Human() {
			out = append(out, m.RoomID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RoomsForUserAllTenants returns every room the user is a member of across all
// tenants. This is the BRIDGE discovery shape: the bridge subscribes,
// advertises, and claims in every room its owner belongs to, so it must see
// them all regardless of which tenant its owner's session token was minted in.
// UI-facing listings stay tenant-scoped (RoomsForUser).
func (s *MemoryRoom) RoomsForUserAllTenants(ctx context.Context, userID string) ([]string, error) {
	return s.roomsAllTenants(ctx, userID, false)
}

// AgentHostRoomsAllTenants returns the live rooms where userID holds an
// agent-host membership, across all tenants.
func (s *MemoryRoom) AgentHostRoomsAllTenants(ctx context.Context, userID string) ([]string, error) {
	return s.roomsAllTenants(ctx, userID, true)
}

func (s *MemoryRoom) roomsAllTenants(ctx context.Context, userID string, agentHost bool) ([]string, error) {
	if _, err := tenant.Require(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, set := range s.members {
		m, ok := set[userID]
		if !ok || m.AgentHost != agentHost {
			continue
		}
		// Same rule as MembershipAnyTenant: only a membership in the room's
		// own tenant, of a live room.
		if _, live := s.liveRoomLocked(m.TenantID, m.RoomID); live {
			out = append(out, m.RoomID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CreateInvite stores an invite code.
func (s *MemoryRoom) CreateInvite(ctx context.Context, inv Invite) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cur := range s.invites {
		if cur.Code == inv.Code {
			return ErrInviteExists
		}
	}
	inv.TenantID = tid
	s.invites[tkey(tid, inv.Code)] = inv
	return nil
}

// PurgeDeadInvites deletes the invites that can no longer be redeemed.
func (s *MemoryRoom) PurgeDeadInvites(_ context.Context, nowMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, inv := range s.invites {
		if !liveAt(inv.ExpiresAtMs, nowMs) || inv.RedeemedCount >= inv.Capacity {
			delete(s.invites, k)
		}
	}
	return nil
}

// RedeemInvite atomically validates and consumes an invite.
func (s *MemoryRoom) RedeemInvite(ctx context.Context, code, userID string, nowMs int64) (Membership, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Membership{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[tkey(tid, code)]
	if !ok {
		return Membership{}, ErrInviteNotFound
	}
	// A soft-deleted room cannot be joined.
	rk, live := s.liveRoomLocked(tid, inv.RoomID)
	if !live {
		return Membership{}, ErrRoomNotFound
	}
	// Idempotent: an existing member redeeming again returns their membership
	// without consuming capacity.
	// An agent-host membership is not one: the invite makes the user a
	// human member.
	if set := s.members[rk]; set != nil {
		if existing, isMember := set[userID]; isMember && existing.Human() {
			return existing, nil
		}
	}
	if !liveAt(inv.ExpiresAtMs, nowMs) {
		return Membership{}, ErrInviteExpired
	}
	if inv.RedeemedCount >= inv.Capacity {
		return Membership{}, ErrInviteFull
	}
	inv.RedeemedCount++
	s.invites[tkey(tid, code)] = inv
	m := Membership{TenantID: tid, RoomID: inv.RoomID, UserID: userID, Role: inv.Role, JoinedAtMs: nowMs}
	_ = s.addMemberLocked(tid, m)
	return m, nil
}

// memberCountLocked returns the member count; caller holds s.mu.
func (s *MemoryRoom) memberCountLocked(rk string) int {
	n := 0
	for _, m := range s.members[rk] {
		if m.Human() {
			n++
		}
	}
	return n
}

// MemberCount returns the number of members in a room.
func (s *MemoryRoom) MemberCount(ctx context.Context, roomID string) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memberCountLocked(tkey(tid, roomID)), nil
}

// SearchRooms returns non-deleted rooms matching the query, newest-first.
func (s *MemoryRoom) SearchRooms(ctx context.Context, query string, limit, offset int) ([]RoomWithCount, int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.TrimSpace(query)
	var matched []Room
	for _, r := range s.rooms {
		if _, live := s.liveRoomLocked(tid, r.RoomID); !live || r.TenantID != tid {
			continue
		}
		if q != "" && !containsFold(r.Name, q) && !containsFold(r.Description, q) {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAtMs != matched[j].CreatedAtMs {
			return matched[i].CreatedAtMs > matched[j].CreatedAtMs
		}
		return matched[i].RoomID < matched[j].RoomID
	})
	total := len(matched)
	offset = min(max(offset, 0), total)
	page := matched[offset:min(offset+searchRoomsPage(limit), total)]
	out := make([]RoomWithCount, 0, len(page))
	for _, r := range page {
		out = append(out, RoomWithCount{Room: r, MemberCount: s.memberCountLocked(tkey(tid, r.RoomID))})
	}
	return out, total, nil
}

// UpdateRoom applies a partial patch.
func (s *MemoryRoom) UpdateRoom(ctx context.Context, roomID string, patch RoomPatch) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, ok := s.liveRoomLocked(tid, roomID)
	if !ok {
		return ErrRoomNotFound
	}
	r := s.rooms[rk]
	if patch.Name != nil {
		r.Name = *patch.Name
	}
	if patch.Description != nil {
		r.Description = *patch.Description
	}
	if patch.Previewable != nil {
		r.Previewable = *patch.Previewable
	}
	if patch.Visibility != nil {
		r.Visibility = *patch.Visibility
	}
	if patch.SyncMode != nil {
		r.SyncMode = *patch.SyncMode
	}
	if patch.Welcome != nil {
		r.Welcome = *patch.Welcome
	}
	if patch.QuickPrompts != nil {
		r.QuickPrompts = slices.Clone(*patch.QuickPrompts)
	}
	if patch.Rules != nil {
		if *patch.Rules != r.Rules {
			r.RulesVersion++
		}
		r.Rules = *patch.Rules
	}
	if patch.RequireRulesAgreement != nil {
		r.RequireRulesAgreement = *patch.RequireRulesAgreement
	}
	if patch.RecommendedChannels != nil {
		r.RecommendedChannels = slices.Clone(*patch.RecommendedChannels)
	}
	if patch.ChannelCreation != nil {
		r.ChannelCreation = *patch.ChannelCreation
	}
	s.rooms[rk] = r
	return nil
}

// AgreeRoomRules records a human member's agreement to the room's current
// rules version.
func (s *MemoryRoom) AgreeRoomRules(ctx context.Context, roomID, userID string, version int) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, ok := s.liveRoomLocked(tid, roomID)
	if !ok {
		return ErrRoomNotFound
	}
	m, ok := s.members[rk][userID]
	if !ok || !m.Human() {
		return ErrRoomNotFound
	}
	if s.rooms[rk].RulesVersion != version {
		return ErrRulesChanged
	}
	m.RulesAgreedVersion = version
	s.members[rk][userID] = m
	return nil
}

// DeleteRoom soft-deletes a room and removes every row this store owns for it
// (memberships, invites, channels), then has every memory store wired by
// NewBackends (cascadeDeleteTo) clear its rows of the room: the same set the
// MySQL DeleteRoom clears (roomOwnedTables).
func (s *MemoryRoom) DeleteRoom(ctx context.Context, roomID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	rk, ok := s.liveRoomLocked(tid, roomID)
	if !ok {
		s.mu.Unlock()
		return ErrRoomNotFound
	}
	s.deleted[rk] = true
	delete(s.members, rk)
	delete(s.channels, channelKey(tid, roomID))
	delete(s.sections, channelKey(tid, roomID))
	for k, f := range s.follows {
		if f.TenantID == tid && f.RoomID == roomID {
			delete(s.follows, k)
		}
	}
	for k, g := range s.groups {
		if g.TenantID == tid && g.RoomID == roomID {
			delete(s.groups, k)
		}
	}
	for k, m := range s.sectionMembers {
		if m.TenantID == tid && m.RoomID == roomID {
			delete(s.sectionMembers, k)
		}
	}
	for k, q := range s.sectionRequests {
		if q.TenantID == tid && q.RoomID == roomID {
			delete(s.sectionRequests, k)
		}
	}
	for k, inv := range s.invites {
		if inv.TenantID == tid && inv.RoomID == roomID {
			delete(s.invites, k)
		}
	}
	owners := s.cascade
	s.mu.Unlock()
	for _, o := range owners {
		o.deleteRoomRows(tid, roomID)
	}
	return nil
}

// cascadeDeleteTo wires the memory stores whose rows DeleteRoom clears.
func (s *MemoryRoom) cascadeDeleteTo(owners ...roomRowsOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cascade = append(s.cascade, owners...)
}

// TransferOwner promotes newOwner to owner and demotes the prior owner(s).
func (s *MemoryRoom) TransferOwner(ctx context.Context, roomID, newOwner string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, live := s.liveRoomLocked(tid, roomID)
	if !live {
		return ErrRoomNotFound
	}
	set := s.members[rk]
	target, ok := set[newOwner]
	if !ok || !target.Human() {
		return ErrNewOwnerNotMember
	}
	for uid, m := range set {
		if m.Role == RoleOwner {
			m.Role = RoleMember
			set[uid] = m
		}
	}
	target.Role = RoleOwner
	set[newOwner] = target
	return nil
}

// RoomsForUserDetailed returns rich rows for the user's non-deleted rooms.
func (s *MemoryRoom) RoomsForUserDetailed(ctx context.Context, userID string) ([]RoomMembershipDetail, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []RoomMembershipDetail{}
	for _, set := range s.members {
		m, ok := set[userID]
		if !ok || m.TenantID != tid || !m.Human() {
			continue
		}
		rk, live := s.liveRoomLocked(tid, m.RoomID)
		if !live {
			continue
		}
		r := s.rooms[rk]
		out = append(out, RoomMembershipDetail{
			Room:        r,
			Role:        m.Role,
			IsOwner:     m.Role == RoleOwner,
			MemberCount: s.memberCountLocked(rk),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].RoomID < out[j].RoomID
	})
	return out, nil
}
