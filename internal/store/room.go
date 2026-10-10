package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Room domain errors.
var (
	ErrRoomNotFound     = errors.New("store: room not found")
	ErrInviteNotFound   = errors.New("store: invite not found")
	ErrInviteExpired    = errors.New("store: invite expired")
	ErrInviteFull       = errors.New("store: invite capacity reached")
	ErrNotShared        = errors.New("store: room is not shared")
	ErrOwnerCannotLeave = errors.New("store: owner cannot leave; transfer or delete")
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

// Room is a collaboration scope. RoomID == workspaceId.
type Room struct {
	TenantID    string
	RoomID      string
	Name        string
	Description string
	Visibility  Visibility
	SyncMode    SyncMode
	CreatedBy   string
	CreatedAtMs int64
}

// RoomWithCount augments a Room with its current member count for plaza rows.
type RoomWithCount struct {
	Room
	MemberCount int
}

// RoomPatch is a partial update to a room. Nil fields are left unchanged.
type RoomPatch struct {
	Name        *string
	Description *string
	Visibility  *Visibility
	SyncMode    *SyncMode
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
}

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
	RoomTenant(ctx context.Context, roomID string) (string, bool)
	// CreateRoomWithOwnerAndChannel atomically creates a room, its owner
	// membership, and the room's default channel in a single transaction
	// (design lock: a room always exists with exactly one default channel). If
	// any insert fails the whole create is rolled back.
	CreateRoomWithOwnerAndChannel(ctx context.Context, r Room, owner Membership, defaultChannel RoomChannel) error
	AddMember(ctx context.Context, m Membership) error
	RemoveMember(ctx context.Context, roomID, userID string) error
	// MemberRole is GetMember's role (an agent host reads as a visitor).
	MemberRole(ctx context.Context, roomID, userID string) (Role, bool)
	// GetMember returns a user's membership of a room in the ctx tenant,
	// agent-host memberships included (Membership.AgentHost).
	GetMember(ctx context.Context, roomID, userID string) (Membership, bool)
	IsMember(ctx context.Context, roomID, userID string) bool
	// MembershipAnyTenant returns the membership keyed on (room_id, user_id)
	// alone, ignoring the caller's tenant — the room-scope fallback for a
	// member whose room lives in a different tenant than their token. Only a
	// membership in the room's own tenant, of a live room, counts.
	MembershipAnyTenant(ctx context.Context, roomID, userID string) (Membership, bool)
	// Members returns every membership of a room, agent hosts flagged.
	Members(ctx context.Context, roomID string) []Membership
	// RoomsForUser returns the rooms the user is a human member of (agent-host
	// memberships are not the user's rooms).
	RoomsForUser(ctx context.Context, userID string) []string
	// RoomsForUserAllTenants returns every room the user is a human member of
	// across all tenants — the bridge discovery shape. The bridge subscribes,
	// advertises, and claims in each of the owner's rooms, so a session token
	// minted in one tenant must not hide rooms in another. UI listings use the
	// tenant-scoped RoomsForUser.
	RoomsForUserAllTenants(ctx context.Context, userID string) []string
	// AgentHostRoomsAllTenants returns the live rooms, across all tenants,
	// where the user holds an agent-host membership (the bridge's
	// includeAgentHost discovery).
	AgentHostRoomsAllTenants(ctx context.Context, userID string) []string
	// AddAgentHost gives userID an agent-host membership of roomID unless they
	// already hold any membership there (a human membership is never
	// downgraded). ErrRoomNotFound when the room is absent or deleted.
	AddAgentHost(ctx context.Context, roomID, userID string, atMs int64) error
	// RemoveAgentHost deletes userID's agent-host membership of roomID; a
	// human membership is never touched. Absent is not an error.
	RemoveAgentHost(ctx context.Context, roomID, userID string) error
	CreateInvite(ctx context.Context, inv Invite) error
	// RedeemInvite atomically validates and consumes an invite for a user,
	// returning the created membership. Redeeming for an already-member user is
	// idempotent and does not increment the redeemed count.
	RedeemInvite(ctx context.Context, code, userID string, nowMs int64) (Membership, error)
	// SearchRooms returns non-deleted rooms whose name or description matches the
	// (case-insensitive) query substring, newest-first, with member counts. An
	// empty query returns all rooms. total is the match count before paging.
	SearchRooms(ctx context.Context, query string, limit, offset int) (rooms []RoomWithCount, total int, err error)
	// UpdateRoom applies a partial patch (name/description/visibility).
	UpdateRoom(ctx context.Context, roomID string, patch RoomPatch) error
	// DeleteRoom soft-deletes a room (its id is never reused) and removes every
	// row that belongs to it: memberships, invites, channels, messages, read
	// state, agent sessions/messages/jobs, grants/leases/audit, attachments
	// metadata, artifacts and their versions, and agent prefs.
	DeleteRoom(ctx context.Context, roomID string) error
	// TransferOwner makes newOwner the room owner and demotes the prior owner to
	// member. newOwner must already be a human member (not an agent host).
	TransferOwner(ctx context.Context, roomID, newOwner string) error
	// RoomsForUserDetailed returns rich rows for the user's rooms (human
	// memberships only).
	RoomsForUserDetailed(ctx context.Context, userID string) []RoomMembershipDetail
	// MemberCount returns the number of human members in a room.
	MemberCount(ctx context.Context, roomID string) int
}

// MemoryRoom is an in-memory RoomStore for M0/M1 and tests.
type MemoryRoom struct {
	mu       sync.Mutex
	rooms    map[string]Room
	members  map[string]map[string]Membership // roomID -> userID -> membership
	invites  map[string]Invite
	deleted  map[string]bool                   // roomID -> soft-deleted
	channels map[string]map[string]RoomChannel // channelKey -> channelID -> channel
}

// NewMemoryRoom constructs an empty in-memory room store.
func NewMemoryRoom() *MemoryRoom {
	return &MemoryRoom{
		rooms:    map[string]Room{},
		members:  map[string]map[string]Membership{},
		invites:  map[string]Invite{},
		deleted:  map[string]bool{},
		channels: map[string]map[string]RoomChannel{},
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
func (s *MemoryRoom) RoomTenant(_ context.Context, roomID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.liveRoomKeyLocked(roomID)
	if !ok {
		return "", false
	}
	return s.rooms[key].TenantID, true
}

// GetRoom returns a room or ErrRoomNotFound. Soft-deleted rooms are not found.
func (s *MemoryRoom) GetRoom(ctx context.Context, roomID string) (Room, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Room{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tkey(tid, roomID)
	r, ok := s.rooms[rk]
	if !ok || s.deleted[rk] {
		return Room{}, ErrRoomNotFound
	}
	return r, nil
}

// AddMember inserts or updates a membership.
func (s *MemoryRoom) AddMember(ctx context.Context, m Membership) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *MemoryRoom) GetMember(ctx context.Context, roomID, userID string) (Membership, bool) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Membership{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[tkey(tid, roomID)][userID]
	return m, ok
}

// MemberRole returns a user's role in a room.
func (s *MemoryRoom) MemberRole(ctx context.Context, roomID, userID string) (Role, bool) {
	m, ok := s.GetMember(ctx, roomID, userID)
	return m.Role, ok
}

// IsMember reports membership.
func (s *MemoryRoom) IsMember(ctx context.Context, roomID, userID string) bool {
	_, ok := s.GetMember(ctx, roomID, userID)
	return ok
}

// MembershipAnyTenant returns the membership of the given room regardless of
// the caller's tenant. Room ids are globally unique, so the room resolves to
// the one tenant that holds it, and only a membership in THAT tenant of a live
// room counts — a membership row filed under any other tenant is never honored.
func (s *MemoryRoom) MembershipAnyTenant(ctx context.Context, roomID, userID string) (Membership, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.liveRoomKeyLocked(roomID)
	if !ok {
		return Membership{}, false
	}
	m, member := s.members[key][userID]
	return m, member
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
	rk := tkey(tid, roomID)
	if _, live := s.rooms[rk]; !live || s.deleted[rk] {
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
	if set := s.members[tkey(tid, roomID)]; set != nil && set[userID].AgentHost {
		delete(set, userID)
	}
	return nil
}

// Members returns all memberships for a room.
func (s *MemoryRoom) Members(ctx context.Context, roomID string) []Membership {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.members[tkey(tid, roomID)]
	out := make([]Membership, 0, len(set))
	for _, m := range set {
		out = append(out, m)
	}
	return out
}

// RoomsForUser returns the room ids the user is a member of.
func (s *MemoryRoom) RoomsForUser(ctx context.Context, userID string) []string {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, set := range s.members {
		if m, ok := set[userID]; ok && m.TenantID == tid && !m.AgentHost {
			out = append(out, m.RoomID)
		}
	}
	return out
}

// RoomsForUserAllTenants returns every room the user is a member of across all
// tenants. This is the BRIDGE discovery shape: the bridge subscribes,
// advertises, and claims in every room its owner belongs to, so it must see
// them all regardless of which tenant its owner's session token was minted in.
// UI-facing listings stay tenant-scoped (RoomsForUser).
func (s *MemoryRoom) RoomsForUserAllTenants(ctx context.Context, userID string) []string {
	return s.roomsAllTenants(ctx, userID, false)
}

// AgentHostRoomsAllTenants returns the live rooms where userID holds an
// agent-host membership, across all tenants.
func (s *MemoryRoom) AgentHostRoomsAllTenants(ctx context.Context, userID string) []string {
	return s.roomsAllTenants(ctx, userID, true)
}

func (s *MemoryRoom) roomsAllTenants(ctx context.Context, userID string, agentHost bool) []string {
	if _, err := tenant.Require(ctx); err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for key, set := range s.members {
		// Same rule as MembershipAnyTenant: only a membership in the room's
		// own tenant, of a live room.
		if _, live := s.rooms[key]; !live || s.deleted[key] {
			continue
		}
		if m, ok := set[userID]; ok && m.AgentHost == agentHost {
			out = append(out, m.RoomID)
		}
	}
	return out
}

// CreateInvite stores an invite code.
func (s *MemoryRoom) CreateInvite(ctx context.Context, inv Invite) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inv.TenantID = tid
	s.invites[tkey(tid, inv.Code)] = inv
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
	rk := tkey(tid, inv.RoomID)
	// A soft-deleted room cannot be joined.
	if s.deleted[rk] {
		return Membership{}, ErrRoomNotFound
	}
	// Idempotent: an existing member redeeming again returns their membership
	// without consuming capacity.
	// An agent-host membership is not one: the invite makes the user a
	// human member.
	if set := s.members[rk]; set != nil {
		if existing, isMember := set[userID]; isMember && !existing.AgentHost {
			return existing, nil
		}
	}
	if nowMs > inv.ExpiresAtMs {
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
		if !m.AgentHost {
			n++
		}
	}
	return n
}

// MemberCount returns the number of members in a room.
func (s *MemoryRoom) MemberCount(ctx context.Context, roomID string) int {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memberCountLocked(tkey(tid, roomID))
}

// SearchRooms returns non-deleted rooms matching the query, newest-first.
func (s *MemoryRoom) SearchRooms(ctx context.Context, query string, limit, offset int) ([]RoomWithCount, int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var matched []Room
	for id, r := range s.rooms {
		if r.TenantID != tid || s.deleted[id] {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(r.Name), q) &&
			!strings.Contains(strings.ToLower(r.Description), q) {
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
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	page := matched[offset:end]
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
	rk := tkey(tid, roomID)
	r, ok := s.rooms[rk]
	if !ok || s.deleted[rk] {
		return ErrRoomNotFound
	}
	if patch.Name != nil {
		r.Name = *patch.Name
	}
	if patch.Description != nil {
		r.Description = *patch.Description
	}
	if patch.Visibility != nil {
		r.Visibility = *patch.Visibility
	}
	if patch.SyncMode != nil {
		r.SyncMode = *patch.SyncMode
	}
	s.rooms[rk] = r
	return nil
}

// DeleteRoom soft-deletes a room and removes every row this store owns for it:
// memberships, invites, and channels. The MySQL store clears every room-owned
// table in one transaction; the other in-memory stores (chat, artifacts, jobs,
// prefs) are per-process dev stores no other service shares, so there is no
// cross-service state for this store to reach.
func (s *MemoryRoom) DeleteRoom(ctx context.Context, roomID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tkey(tid, roomID)
	if _, ok := s.rooms[rk]; !ok || s.deleted[rk] {
		return ErrRoomNotFound
	}
	s.deleted[rk] = true
	delete(s.members, rk)
	delete(s.channels, channelKey(tid, roomID))
	for k, inv := range s.invites {
		if inv.TenantID == tid && inv.RoomID == roomID {
			delete(s.invites, k)
		}
	}
	return nil
}

// TransferOwner promotes newOwner to owner and demotes the prior owner(s).
func (s *MemoryRoom) TransferOwner(ctx context.Context, roomID, newOwner string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.members[tkey(tid, roomID)]
	if set == nil {
		return ErrRoomNotFound
	}
	target, ok := set[newOwner]
	if !ok || target.AgentHost {
		return errors.New("store: new owner is not a member")
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
func (s *MemoryRoom) RoomsForUserDetailed(ctx context.Context, userID string) []RoomMembershipDetail {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RoomMembershipDetail
	for _, set := range s.members {
		m, ok := set[userID]
		if !ok || m.TenantID != tid || m.AgentHost {
			continue
		}
		rk := tkey(tid, m.RoomID)
		if s.deleted[rk] {
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
	return out
}
