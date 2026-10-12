// Package membership owns how the backends (realtime, activity, shareagent,
// notify) read room membership: one Member value with the abilities every
// service asks about (IsHuman, MayChat), one Directory interface with the
// room-service client (RoomClient) and an in-memory set (Static), and one
// request check (RoomScope) that also refuses a request signed for another
// tenant than the room's own.
package membership

import (
	"context"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Member is one user's membership of a room as authorization reads it.
// AgentHost marks a platform agent host's hidden membership (store.Membership):
// it reads the room for the host's bridge but is not a human membership.
// Tenant is the room's own tenant (rooms.tenant_id); "" only in a Static set
// that was given none (single-tenant tests and dev).
type Member struct {
	Role      store.Role
	AgentHost bool
	Tenant    string
}

// IsHuman reports a person's membership (not an agent host's). Every
// human-only surface (presence, roster advertisement, the people chat,
// plugin events, mentions, authorizing a borrow) asks this.
func (m Member) IsHuman() bool { return !m.AgentHost }

// MayChat is the ONE rule for "may this member act in the room's
// conversation": post chat, type, drive or borrow an agent, create or run a
// schedule, add a platform agent. A human member who is not a visitor
// (visitors are read-only, docs/prd/2026-10-12-code-health-remediation.md
// D1). The web mirrors it in lib/roomRoles.ts.
func (m Member) MayChat() bool { return m.IsHuman() && m.Role != store.RoleVisitor }

// Reader is the read side a background consumer needs (notify, plugin
// fan-out). Lookup may serve a short-lived cached positive answer. Every
// implementation fails closed: no answer is "not a member".
type Reader interface {
	Lookup(roomID, userID string) (Member, bool)
}

// Directory answers room membership for request handlers. Fresh always asks
// the authority (a stream open); Forget drops the cached answers for (room,
// user), or for the whole room when userID is "" (a member removed, a room
// deleted).
type Directory interface {
	Reader
	Fresh(roomID, userID string) (Member, bool)
	Forget(roomID, userID string)
}

// The refusals of RoomScope; every one is a 403 to a client (IsRefusal).
var (
	// ErrNotMember refuses a caller who is not a member of the room.
	ErrNotMember = errors.New("caller is not a member of room")
	// ErrOtherTenant refuses a request signed for a tenant other than the
	// room's own: the gateway acts in the room's tenant for every path that
	// names a room (services/gateway/tenant_scope.go), so a mismatch is a
	// scoping bug that must fail closed instead of writing the room's rows
	// elsewhere.
	ErrOtherTenant = errors.New("request tenant is not the room's tenant")
	// ErrNotHuman refuses an agent host's membership on a human-only surface.
	ErrNotHuman = errors.New("only a person in the room may do this")
	// ErrMayNotChat refuses a member who may not act in the conversation
	// (a visitor, or an agent host).
	ErrMayNotChat = errors.New("visitors are read-only: only a member who may chat in the room may do this")
)

// IsRefusal reports whether err is one of RoomScope's refusals.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrNotMember) || errors.Is(err, ErrOtherTenant) || errors.Is(err, ErrNotHuman) || errors.Is(err, ErrMayNotChat)
}

// Need is the ability a route requires of the caller's membership.
type Need int

const (
	// AnyMember admits every membership, an agent host's included (reads).
	AnyMember Need = iota
	// Human admits a person's membership (Member.IsHuman).
	Human
	// Chat admits a member who may act in the conversation (Member.MayChat).
	Chat
)

func (n Need) check(m Member) error {
	switch {
	case n == Human && !m.IsHuman():
		return ErrNotHuman
	case n == Chat && !m.MayChat():
		return ErrMayNotChat
	}
	return nil
}

// RoomScope is the ONE membership check of a room-scoped request: userID's
// membership of roomID (fail closed on a nil directory), in the tenant the
// request is signed for (tenant.FromContext), with the ability need.
func RoomScope(ctx context.Context, d Directory, roomID, userID string, need Need) (Member, error) {
	return scope(ctx, d, roomID, userID, need, false)
}

// FreshRoomScope is RoomScope without the cache: a stream open, which then
// lives on its own re-checks.
func FreshRoomScope(ctx context.Context, d Directory, roomID, userID string, need Need) (Member, error) {
	return scope(ctx, d, roomID, userID, need, true)
}

func scope(ctx context.Context, d Directory, roomID, userID string, need Need, fresh bool) (Member, error) {
	if d == nil || roomID == "" || userID == "" {
		return Member{}, ErrNotMember
	}
	lookup := d.Lookup
	if fresh {
		lookup = d.Fresh
	}
	m, ok := lookup(roomID, userID)
	if !ok {
		return Member{}, ErrNotMember
	}
	if m.Tenant != "" && m.Tenant != tenant.FromContext(ctx) {
		return Member{}, ErrOtherTenant
	}
	if err := need.check(m); err != nil {
		return Member{}, err
	}
	return m, nil
}

// HumanMember is the background form of RoomScope(..., Human): no request,
// so no tenant to compare (a fan-out, a notification, a share hand-out reads
// the room as it is). Fails closed on a nil directory.
func HumanMember(d Reader, roomID, userID string) bool {
	if d == nil {
		return false
	}
	m, ok := d.Lookup(roomID, userID)
	return ok && m.IsHuman()
}

// Static is an in-memory membership set for dev and tests.
type Static struct {
	mu      sync.RWMutex
	members map[string]map[string]Member // roomID -> userID -> membership
	tenants map[string]string            // roomID -> the room's tenant
}

// NewStatic constructs an empty static membership set.
func NewStatic() *Static {
	return &Static{members: map[string]map[string]Member{}, tenants: map[string]string{}}
}

// Add records a (human) member in a room.
func (s *Static) Add(roomID, userID string) { s.AddRole(roomID, userID, store.RoleMember) }

// AddRole records a human member with a role.
func (s *Static) AddRole(roomID, userID string, role store.Role) {
	s.put(roomID, userID, Member{Role: role})
}

// SetRoomTenant files a room under a tenant: its memberships then carry it,
// and RoomScope refuses a request signed for any other tenant.
func (s *Static) SetRoomTenant(roomID, tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[roomID] = tenantID
}

// Remove deletes a membership (any kind).
func (s *Static) Remove(roomID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.members[roomID], userID)
}

func (s *Static) put(roomID, userID string, m Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[roomID] == nil {
		s.members[roomID] = map[string]Member{}
	}
	s.members[roomID][userID] = m
}

// Lookup returns a user's membership.
func (s *Static) Lookup(roomID, userID string) (Member, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.members[roomID][userID]
	m.Tenant = s.tenants[roomID]
	return m, ok
}

// Fresh is Lookup: the static set has no cache.
func (s *Static) Fresh(roomID, userID string) (Member, bool) { return s.Lookup(roomID, userID) }

// Forget is a no-op: the static set has no cache.
func (s *Static) Forget(string, string) {}

// PutAgentHost mirrors the room service: an agent-host membership is added
// only for a user who is not already a member.
func (s *Static) PutAgentHost(_ context.Context, _, roomID, userID string) error {
	if _, ok := s.Lookup(roomID, userID); ok {
		return nil
	}
	s.put(roomID, userID, Member{Role: store.RoleVisitor, AgentHost: true})
	return nil
}

// DeleteAgentHost removes an agent-host membership (never a human one).
func (s *Static) DeleteAgentHost(_ context.Context, _, roomID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[roomID][userID].AgentHost {
		delete(s.members[roomID], userID)
	}
	return nil
}
