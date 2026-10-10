package store

import (
	"context"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Tenant domain errors.
var (
	ErrTenantNotFound = errors.New("store: tenant not found")
)

// OrgRole is a member's organization-level role (distinct from room roles).
type OrgRole string

// Organization roles, ordered by privilege. super_admin is a PLATFORM role
// (cross-tenant) that sits above org_admin; it is resolved from configuration
// at login, not from tenant membership, so it never appears as a stored
// tenant_members row.
const (
	OrgRoleSuperAdmin OrgRole = "super_admin"
	OrgRoleAdmin      OrgRole = "org_admin"
	OrgRoleMember     OrgRole = "member"
	OrgRoleViewer     OrgRole = "viewer"
)

// NormalizeOrgRole maps arbitrary input to a known org role, defaulting to
// member (the safe least-privilege default for an auto-provisioned user).
func NormalizeOrgRole(v string) OrgRole {
	switch OrgRole(v) {
	case OrgRoleAdmin:
		return OrgRoleAdmin
	case OrgRoleViewer:
		return OrgRoleViewer
	default:
		return OrgRoleMember
	}
}

// TenantStatus controls whether a tenant may be used.
type TenantStatus string

// Tenant statuses.
const (
	TenantActive    TenantStatus = "active"
	TenantSuspended TenantStatus = "suspended"
)

// Tenant is one organization.
type Tenant struct {
	TenantID    string
	Name        string
	Status      TenantStatus
	SSOIssuer   string // IdP issuer / email domain this tenant maps from
	CreatedAtMs int64
	// RunQuota / CostAllowance are org-level ceilings (0 = unlimited) enforced on
	// top of per-grant quotas.
	RunQuota      int
	CostAllowance float64
	// RequireSandboxForBorrow is the org-admin sandbox policy: borrowed agent
	// drives must run sandboxed; an owner machine without a ready sandbox
	// refuses them instead of running on the host (default off).
	RequireSandboxForBorrow bool
	// PublicSharesDisabled is the org-admin switch that turns public artifact
	// share links off: creating one is refused and every existing link
	// resolves 404 while it is on (default off).
	PublicSharesDisabled bool
	// Retention is the org-admin data retention override (null = the
	// platform default, 0 = keep forever, N = delete after N days); see
	// wire.RetentionPolicy. Set only through SetTenantRetention.
	Retention wire.RetentionPolicy
}

// TenantPolicyUpdate changes the org-admin policy switches; a nil field is
// left as stored.
type TenantPolicyUpdate struct {
	RequireSandboxForBorrow *bool
	PublicSharesDisabled    *bool
}

// apply returns t with the update's set fields.
func (u TenantPolicyUpdate) apply(t Tenant) Tenant {
	if u.RequireSandboxForBorrow != nil {
		t.RequireSandboxForBorrow = *u.RequireSandboxForBorrow
	}
	if u.PublicSharesDisabled != nil {
		t.PublicSharesDisabled = *u.PublicSharesDisabled
	}
	return t
}

// TenantMember records a user's org-level role in a tenant.
type TenantMember struct {
	TenantID   string
	UserID     string
	Role       OrgRole
	JoinedAtMs int64
	// Source says who owns the membership: MemberSourceManual (an admin,
	// a join request, login provisioning) or MemberSourceSync (organization
	// sync rules). Sync only ever changes or removes sync-owned rows. Empty
	// is stored as manual.
	Source MemberSource
}

// MemberSource is the owner of a tenant membership.
type MemberSource string

// Membership sources.
const (
	MemberSourceManual MemberSource = "manual"
	MemberSourceSync   MemberSource = "sync"
)

// normalizeMemberSource maps an unset source to manual (the safe owner: sync
// never removes a manual row).
func normalizeMemberSource(v MemberSource) MemberSource {
	if v == MemberSourceSync {
		return MemberSourceSync
	}
	return MemberSourceManual
}

// JoinRequestStatus is the lifecycle state of a membership request.
type JoinRequestStatus string

// Join-request statuses.
const (
	JoinPending  JoinRequestStatus = "pending"
	JoinApproved JoinRequestStatus = "approved"
	JoinRejected JoinRequestStatus = "rejected"
)

// JoinRequest is a user's request to join a tenant, pending an admin decision.
// It is distinct from a TenantMember: a request grants no access until approved
// (which creates the membership). One (tenant,user) has at most one live row.
type JoinRequest struct {
	TenantID    string
	UserID      string
	Message     string
	Status      JoinRequestStatus
	CreatedAtMs int64
	DecidedAtMs int64
	DecidedBy   string
}

// TenantStore persists tenants and their members, and resolves the tenant a
// given IdP issuer/domain maps to. Implementations must be safe for concurrent
type TenantStore interface {
	// PutTenant creates or updates a tenant.
	PutTenant(ctx context.Context, t Tenant) error
	// GetTenant returns a tenant or ErrTenantNotFound.
	GetTenant(ctx context.Context, tenantID string) (Tenant, error)
	// ListTenants returns every tenant, for the platform (super-admin) console.
	ListTenants(ctx context.Context) []Tenant
	// TenantForIssuer returns the tenant id mapped to an SSO issuer / email
	// domain, or found=false when none is registered.
	TenantForIssuer(ctx context.Context, issuer string) (tenantID string, found bool)
	// PutMember creates or updates a tenant membership.
	PutMember(ctx context.Context, m TenantMember) error
	// GetMember returns a user's membership in a tenant, found=false when none.
	GetMember(ctx context.Context, tenantID, userID string) (TenantMember, bool)
	// MemberCount returns how many members a tenant has (to detect the first
	// user, who is promoted to org_admin).
	MemberCount(ctx context.Context, tenantID string) int
	// ListMembers returns all members of a tenant (for the org-admin console).
	ListMembers(ctx context.Context, tenantID string) []TenantMember
	// RemoveMember deletes a membership. Removing an absent member is a no-op.
	RemoveMember(ctx context.Context, tenantID, userID string) error
	// SetQuota persists a tenant's org-level run/cost ceilings (0 = unlimited).
	SetQuota(ctx context.Context, tenantID string, runQuota int, costAllowance float64) error
	// SetTenantPolicy persists the org-admin policy switches the update sets
	// (agent sandbox for borrowed drives, public share links) and returns
	// the tenant as stored; ErrTenantNotFound when it does not exist.
	SetTenantPolicy(ctx context.Context, tenantID string, u TenantPolicyUpdate) (Tenant, error)
	// SetTenantRetention replaces the tenant's whole data retention override
	// (a null kind uses the platform default) and returns the tenant as
	// stored; ErrTenantNotFound when it does not exist. The caller validates.
	SetTenantRetention(ctx context.Context, tenantID string, p wire.RetentionPolicy) (Tenant, error)
	// PutJoinRequest creates or replaces a user's join request for a tenant.
	PutJoinRequest(ctx context.Context, jr JoinRequest) error
	// GetJoinRequest returns a user's join request for a tenant, found=false when none.
	GetJoinRequest(ctx context.Context, tenantID, userID string) (JoinRequest, bool)
	// ListJoinRequests returns a tenant's join requests filtered by status
	// (empty status = all), for the admin review queue.
	ListJoinRequests(ctx context.Context, tenantID string, status JoinRequestStatus) []JoinRequest
	// ListJoinRequestsForUser returns all of a user's join requests across
	// tenants, so a user can see the status of what they applied to.
	ListJoinRequestsForUser(ctx context.Context, userID string) []JoinRequest
	// PrimaryTenantForUser returns the tenant a user should land in at login
	// when neither an explicit IdP tenant nor an issuer mapping applies: their
	// most-recently-joined membership. found=false when the user has none.
	PrimaryTenantForUser(ctx context.Context, userID string) (tenantID string, found bool)
	// ListMembershipsForUser returns every tenant membership of a user (any
	// source), for organization sync and deprovisioning.
	ListMembershipsForUser(ctx context.Context, userID string) []TenantMember
}

// MemoryTenant is an in-memory TenantStore for dev and tests.
type MemoryTenant struct {
	mu       sync.Mutex
	tenants  map[string]Tenant
	byIssue  map[string]string                  // issuer -> tenantID
	members  map[string]map[string]TenantMember // tenantID -> userID -> member
	requests map[string]map[string]JoinRequest  // tenantID -> userID -> request
}

// NewMemoryTenant constructs an empty in-memory tenant store.
func NewMemoryTenant() *MemoryTenant {
	return &MemoryTenant{
		tenants:  map[string]Tenant{},
		byIssue:  map[string]string{},
		members:  map[string]map[string]TenantMember{},
		requests: map[string]map[string]JoinRequest{},
	}
}

func (s *MemoryTenant) PutTenant(_ context.Context, t Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Status == "" {
		t.Status = TenantActive
	}
	// Match the MySQL upsert: an existing tenant only takes the new name,
	// status and SSO issuer; its quotas, policy switches and creation time
	// are owned by their own setters.
	if cur, ok := s.tenants[t.TenantID]; ok {
		cur.Name, cur.Status, cur.SSOIssuer = t.Name, t.Status, t.SSOIssuer
		t = cur
	}
	s.tenants[t.TenantID] = t
	if t.SSOIssuer != "" {
		s.byIssue[t.SSOIssuer] = t.TenantID
	}
	return nil
}

func (s *MemoryTenant) GetTenant(_ context.Context, tenantID string) (Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return Tenant{}, ErrTenantNotFound
	}
	return t, nil
}

func (s *MemoryTenant) ListTenants(_ context.Context) []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	return out
}

func (s *MemoryTenant) TenantForIssuer(_ context.Context, issuer string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byIssue[issuer]
	return id, ok
}

func (s *MemoryTenant) PutMember(_ context.Context, m TenantMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[m.TenantID] == nil {
		s.members[m.TenantID] = map[string]TenantMember{}
	}
	if m.Role == "" {
		m.Role = OrgRoleMember
	}
	m.Source = normalizeMemberSource(m.Source)
	s.members[m.TenantID][m.UserID] = m
	return nil
}

func (s *MemoryTenant) ListMembershipsForUser(_ context.Context, userID string) []TenantMember {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TenantMember
	for _, byUser := range s.members {
		if m, ok := byUser[userID]; ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *MemoryTenant) GetMember(_ context.Context, tenantID, userID string) (TenantMember, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[tenantID][userID]
	return m, ok
}

func (s *MemoryTenant) MemberCount(_ context.Context, tenantID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members[tenantID])
}

func (s *MemoryTenant) RemoveMember(_ context.Context, tenantID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[tenantID] != nil {
		delete(s.members[tenantID], userID)
	}
	return nil
}

func (s *MemoryTenant) ListMembers(_ context.Context, tenantID string) []TenantMember {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TenantMember, 0, len(s.members[tenantID]))
	for _, m := range s.members[tenantID] {
		out = append(out, m)
	}
	return out
}

func (s *MemoryTenant) SetQuota(_ context.Context, tenantID string, runQuota int, costAllowance float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return ErrTenantNotFound
	}
	t.RunQuota = runQuota
	t.CostAllowance = costAllowance
	s.tenants[tenantID] = t
	return nil
}

func (s *MemoryTenant) SetTenantPolicy(_ context.Context, tenantID string, u TenantPolicyUpdate) (Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return Tenant{}, ErrTenantNotFound
	}
	t = u.apply(t)
	s.tenants[tenantID] = t
	return t, nil
}

func (s *MemoryTenant) SetTenantRetention(_ context.Context, tenantID string, p wire.RetentionPolicy) (Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return Tenant{}, ErrTenantNotFound
	}
	t.Retention = copyRetention(p)
	s.tenants[tenantID] = t
	return t, nil
}

// copyRetention detaches a policy from the caller's pointers.
func copyRetention(p wire.RetentionPolicy) wire.RetentionPolicy {
	out := wire.RetentionPolicy{}
	for _, k := range wire.RetentionKinds {
		if d := p.Days(k); d != nil {
			v := *d
			out = out.With(k, &v)
		}
	}
	return out
}

func (s *MemoryTenant) PutJoinRequest(_ context.Context, jr JoinRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests[jr.TenantID] == nil {
		s.requests[jr.TenantID] = map[string]JoinRequest{}
	}
	s.requests[jr.TenantID][jr.UserID] = jr
	return nil
}

func (s *MemoryTenant) GetJoinRequest(_ context.Context, tenantID, userID string) (JoinRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jr, ok := s.requests[tenantID][userID]
	return jr, ok
}

func (s *MemoryTenant) ListJoinRequests(_ context.Context, tenantID string, status JoinRequestStatus) []JoinRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JoinRequest, 0, len(s.requests[tenantID]))
	for _, jr := range s.requests[tenantID] {
		if status == "" || jr.Status == status {
			out = append(out, jr)
		}
	}
	return out
}

func (s *MemoryTenant) ListJoinRequestsForUser(_ context.Context, userID string) []JoinRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []JoinRequest
	for _, byUser := range s.requests {
		if jr, ok := byUser[userID]; ok {
			out = append(out, jr)
		}
	}
	return out
}

func (s *MemoryTenant) PrimaryTenantForUser(_ context.Context, userID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A real org membership always outranks the "default" catch-all tenant,
	// which is auto-provisioned on first login and would otherwise shadow the
	// org a user was actually approved into. Within the same class, the
	// most-recently-joined wins. ("default" is the literal fallback tenant id;
	// the store layer does not import the tenant package.)
	best, bestAt, bestDefault := "", int64(-1), true
	for tid, byUser := range s.members {
		m, ok := byUser[userID]
		if !ok {
			continue
		}
		isDefault := tid == "default"
		better := best == "" ||
			(bestDefault && !isDefault) ||
			(bestDefault == isDefault && m.JoinedAtMs >= bestAt)
		if better {
			best, bestAt, bestDefault = tid, m.JoinedAtMs, isDefault
		}
	}
	return best, best != ""
}
