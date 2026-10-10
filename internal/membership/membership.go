// Package membership provides membership authorization implementations. M0 uses
// a static in-memory set; M1 replaces it with a bnbloc-room HTTP client behind
// the same interface.
package membership

import (
	"context"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Member is one user's membership of a room as authorization reads it.
// AgentHost marks a platform agent host's hidden membership (store.Membership):
// it reads the room for the host's bridge but is not a human membership.
type Member struct {
	Role      store.Role
	AgentHost bool
}

// Static is an in-memory membership set for M0 and tests.
type Static struct {
	mu      sync.RWMutex
	members map[string]map[string]Member // roomID -> userID -> membership
}

// NewStatic constructs an empty static membership set.
func NewStatic() *Static {
	return &Static{members: map[string]map[string]Member{}}
}

// Add records a (human) member in a room.
func (s *Static) Add(roomID, userID string) { s.AddRole(roomID, userID, store.RoleMember) }

// AddRole records a human member with a role.
func (s *Static) AddRole(roomID, userID string, role store.Role) {
	s.put(roomID, userID, Member{Role: role})
}

func (s *Static) put(roomID, userID string, m Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[roomID] == nil {
		s.members[roomID] = map[string]Member{}
	}
	s.members[roomID][userID] = m
}

// Lookup returns a user's membership. The tenant id is accepted for interface
// parity with the HTTP client; the static set is single-tenant and ignores it.
func (s *Static) Lookup(roomID, userID, _ string) (Member, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.members[roomID][userID]
	return m, ok
}

// IsMember reports whether a user belongs to a room in any way.
func (s *Static) IsMember(roomID, userID, tenantID string) bool {
	_, ok := s.Lookup(roomID, userID, tenantID)
	return ok
}

// IsHumanMember reports whether a user is a non-agent-host member.
func (s *Static) IsHumanMember(roomID, userID, tenantID string) bool {
	m, ok := s.Lookup(roomID, userID, tenantID)
	return ok && !m.AgentHost
}

// PutAgentHost mirrors the room service: an agent-host membership is added
// only for a user who is not already a member.
func (s *Static) PutAgentHost(_ context.Context, _, roomID, userID string) error {
	if _, ok := s.Lookup(roomID, userID, ""); ok {
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
