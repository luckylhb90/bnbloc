package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Tenant domain errors.
var (
	ErrTenantNotFound = errors.New("store: tenant not found")
	// ErrSSOIssuerTaken: another tenant already maps the SSO issuer (an
	// issuer names at most one tenant).
	ErrSSOIssuerTaken = errors.New("store: sso issuer already maps another tenant")
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
	// AnonymousLinksDisabled is the org-admin switch that turns share links
	// anyone can open (access "anyone") off: creating or switching to one
	// is refused and every existing one resolves 404 while it is on
	// (default off). Links for the organization's members stay allowed.
	// The room service's anonymousLinkAllowed is its one reader.
	AnonymousLinksDisabled bool
	// PublishApproval is the org-admin setting whether a member's publish
	// to the organization waits for an admin (wire.PublishApprovalRequired,
	// the default a new tenant gets) or takes effect at once
	// (wire.PublishApprovalNone). contentshare.NeedsApproval is its one
	// reader.
	PublishApproval string
	// Retention is the org-admin data retention override (null = the
	// platform default, 0 = keep forever, N = delete after N days); see
	// wire.RetentionPolicy. Set only through SetTenantRetention.
	Retention wire.RetentionPolicy
}

// TenantPolicyUpdate changes the org-admin policy switches; a nil field is
// left as stored.
type TenantPolicyUpdate struct {
	RequireSandboxForBorrow *bool
	AnonymousLinksDisabled  *bool
	PublishApproval         *string
}

// apply returns t with the update's set fields.
func (u TenantPolicyUpdate) apply(t Tenant) Tenant {
	if u.RequireSandboxForBorrow != nil {
		t.RequireSandboxForBorrow = *u.RequireSandboxForBorrow
	}
	if u.AnonymousLinksDisabled != nil {
		t.AnonymousLinksDisabled = *u.AnonymousLinksDisabled
	}
	if u.PublishApproval != nil {
		t.PublishApproval = *u.PublishApproval
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
// given IdP issuer/domain maps to. Implementations must be safe for
// concurrent use. It is the registry that establishes tenancy, so it takes
// tenant ids as arguments, not from the context.
type TenantStore interface {
	// PutTenant creates or updates a tenant. An issuer another tenant
	// already maps is ErrSSOIssuerTaken; an empty issuer maps nothing.
	PutTenant(ctx context.Context, t Tenant) error
	// GetTenant returns a tenant or ErrTenantNotFound.
	GetTenant(ctx context.Context, tenantID string) (Tenant, error)
	// ListTenants returns every tenant, for the platform (super-admin)
	// console, oldest first (then tenant id).
	ListTenants(ctx context.Context) ([]Tenant, error)
	// TenantForIssuer returns the tenant id mapped to an SSO issuer / email
	// domain, or found=false when none is registered. A read failure is an
	// error, never "not registered".
	TenantForIssuer(ctx context.Context, issuer string) (tenantID string, found bool, err error)
	// PutMember creates or updates a tenant membership (an admin decision:
	// it overwrites the role and source; an existing row keeps its join
	// time).
	PutMember(ctx context.Context, m TenantMember) error
	// EnsureMember inserts m unless the user is already a member, and returns
	// the membership as stored (created=false: the existing row, its role
	// untouched). It is the login path's write: a failed or racing read can
	// never turn into a role change.
	EnsureMember(ctx context.Context, m TenantMember) (stored TenantMember, created bool, err error)
	// GetMember returns a user's membership in a tenant, found=false when
	// none. A read failure is an error, never "not a member".
	GetMember(ctx context.Context, tenantID, userID string) (TenantMember, bool, error)
	// MemberCount returns how many members a tenant has (to detect the first
	// user, who is promoted to org_admin).
	MemberCount(ctx context.Context, tenantID string) (int, error)
	// ListMembers returns all members of a tenant (for the org-admin
	// console), in join order (then user id).
	ListMembers(ctx context.Context, tenantID string) ([]TenantMember, error)
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
	GetJoinRequest(ctx context.Context, tenantID, userID string) (JoinRequest, bool, error)
	// ListJoinRequests returns a tenant's join requests filtered by status
	// (empty status = all), for the admin review queue, oldest first (then
	// user id).
	ListJoinRequests(ctx context.Context, tenantID string, status JoinRequestStatus) ([]JoinRequest, error)
	// ListJoinRequestsForUser returns all of a user's join requests across
	// tenants, so a user can see the status of what they applied to, newest
	// first (then tenant id).
	ListJoinRequestsForUser(ctx context.Context, userID string) ([]JoinRequest, error)
	// PrimaryTenantForUser returns the tenant a user should land in at login
	// when neither an explicit IdP tenant nor an issuer mapping applies
	// (primaryTenant: a real org outranks the default tenant, then the
	// most-recently-joined membership, then the smallest tenant id).
	// found=false when the user has none.
	PrimaryTenantForUser(ctx context.Context, userID string) (tenantID string, found bool, err error)
	// ListMembershipsForUser returns every tenant membership of a user (any
	// source), for organization sync and deprovisioning, in join order (then
	// tenant id).
	ListMembershipsForUser(ctx context.Context, userID string) ([]TenantMember, error)
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
	if owner, ok := s.byIssue[t.SSOIssuer]; ok && t.SSOIssuer != "" && owner != t.TenantID {
		return ErrSSOIssuerTaken
	}
	// Match the MySQL upsert: an existing tenant only takes the new name,
	// status and SSO issuer; its quotas, policy switches and creation time
	// are owned by their own setters. A changed issuer releases the old one.
	if cur, ok := s.tenants[t.TenantID]; ok {
		if cur.SSOIssuer != t.SSOIssuer {
			delete(s.byIssue, cur.SSOIssuer)
		}
		cur.Name, cur.Status, cur.SSOIssuer = t.Name, t.Status, t.SSOIssuer
		t = cur
	} else {
		// A new tenant takes the column defaults MySQL gives it.
		t.PublishApproval = wire.PublishApprovalRequired
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

func (s *MemoryTenant) ListTenants(_ context.Context) ([]Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].TenantID < out[j].TenantID
	})
	return out, nil
}

func (s *MemoryTenant) TenantForIssuer(_ context.Context, issuer string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byIssue[issuer]
	return id, ok, nil
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
	// Like the MySQL upsert: a re-put changes the role and source only.
	if cur, ok := s.members[m.TenantID][m.UserID]; ok {
		m.JoinedAtMs = cur.JoinedAtMs
	}
	s.members[m.TenantID][m.UserID] = m
	return nil
}

func (s *MemoryTenant) EnsureMember(_ context.Context, m TenantMember) (TenantMember, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.members[m.TenantID][m.UserID]; ok {
		return cur, false, nil
	}
	if s.members[m.TenantID] == nil {
		s.members[m.TenantID] = map[string]TenantMember{}
	}
	if m.Role == "" {
		m.Role = OrgRoleMember
	}
	m.Source = normalizeMemberSource(m.Source)
	s.members[m.TenantID][m.UserID] = m
	return m, true, nil
}

func (s *MemoryTenant) ListMembershipsForUser(_ context.Context, userID string) ([]TenantMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TenantMember{}
	for _, byUser := range s.members {
		if m, ok := byUser[userID]; ok {
			out = append(out, m)
		}
	}
	sortMembers(out)
	return out, nil
}

// sortMembers is the MySQL membership list order: joined_at_ms, then
// tenant id, then user id.
func sortMembers(out []TenantMember) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.JoinedAtMs != b.JoinedAtMs {
			return a.JoinedAtMs < b.JoinedAtMs
		}
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		return a.UserID < b.UserID
	})
}

func (s *MemoryTenant) GetMember(_ context.Context, tenantID, userID string) (TenantMember, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[tenantID][userID]
	return m, ok, nil
}

func (s *MemoryTenant) MemberCount(_ context.Context, tenantID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members[tenantID]), nil
}

func (s *MemoryTenant) RemoveMember(_ context.Context, tenantID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[tenantID] != nil {
		delete(s.members[tenantID], userID)
	}
	return nil
}

func (s *MemoryTenant) ListMembers(_ context.Context, tenantID string) ([]TenantMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TenantMember, 0, len(s.members[tenantID]))
	for _, m := range s.members[tenantID] {
		out = append(out, m)
	}
	sortMembers(out)
	return out, nil
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

func (s *MemoryTenant) GetJoinRequest(_ context.Context, tenantID, userID string) (JoinRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jr, ok := s.requests[tenantID][userID]
	return jr, ok, nil
}

func (s *MemoryTenant) ListJoinRequests(_ context.Context, tenantID string, status JoinRequestStatus) ([]JoinRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JoinRequest, 0, len(s.requests[tenantID]))
	for _, jr := range s.requests[tenantID] {
		if status == "" || jr.Status == status {
			out = append(out, jr)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (s *MemoryTenant) ListJoinRequestsForUser(_ context.Context, userID string) ([]JoinRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []JoinRequest{}
	for _, byUser := range s.requests {
		if jr, ok := byUser[userID]; ok {
			out = append(out, jr)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].TenantID < out[j].TenantID
	})
	return out, nil
}

func (s *MemoryTenant) PrimaryTenantForUser(ctx context.Context, userID string) (string, bool, error) {
	ms, err := s.ListMembershipsForUser(ctx, userID)
	if err != nil {
		return "", false, err
	}
	return primaryTenant(ms)
}

// primaryTenant is the one login-landing rule over a user's memberships: a
// real org membership outranks the default tenant (tenant.Default, which a
// first sign-in without an org provisions and which would otherwise shadow
// the org the user was later approved into), then the most-recently-joined
// wins, then the smallest tenant id. found=false for no membership.
func primaryTenant(ms []TenantMember) (string, bool, error) {
	var best *TenantMember
	for i := range ms {
		m := &ms[i]
		if best == nil {
			best = m
			continue
		}
		mDefault, bestDefault := m.TenantID == tenant.Default, best.TenantID == tenant.Default
		switch {
		case mDefault != bestDefault:
			if bestDefault {
				best = m
			}
		case m.JoinedAtMs != best.JoinedAtMs:
			if m.JoinedAtMs > best.JoinedAtMs {
				best = m
			}
		case m.TenantID < best.TenantID:
			best = m
		}
	}
	if best == nil {
		return "", false, nil
	}
	return best.TenantID, true, nil
}
