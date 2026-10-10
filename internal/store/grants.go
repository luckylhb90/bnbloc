package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Shared-agent domain errors.
var (
	ErrGrantNotFound   = errors.New("store: grant not found")
	ErrConcurrencyFull = errors.New("store: concurrency lease unavailable")
	// ErrRunQuotaExhausted: consuming a run would exceed the grant's RunQuota.
	ErrRunQuotaExhausted = errors.New("store: grant run quota exhausted")
	// ErrCostAllowanceExhausted: the run's cost would exceed the grant's
	// CostAllowance.
	ErrCostAllowanceExhausted = errors.New("store: grant cost allowance exhausted")
)

// AllowedModel is one entry in a grant's allowed (ModelPlan, model) range. A
// ModelPlanID of "" matches every otherwise-compatible plan.
type AllowedModel struct {
	ModelPlanID string `json:"modelPlanId,omitempty"`
	Model       string `json:"model"`
}

// Capabilities are the shared-agent turn capabilities a grant permits.
type Capabilities struct {
	Consult  bool `json:"consult"`
	Review   bool `json:"review"`
	Delegate bool `json:"delegate"`
	Upgrade  bool `json:"upgrade"`
}

// Grant is a durable authorization allowing a grantee to use an owner's shared
// agent within limits. It never holds a credential.
type Grant struct {
	GrantID       string
	TenantID      string
	RoomID        string
	TargetID      string // shared-agent:*
	OwnerUserID   string
	GranteeUserID string
	AllowedModels []AllowedModel
	Capabilities  Capabilities
	RunQuota      *int     // nil = unlimited
	CostAllowance *float64 // nil = unlimited
	MaxConcurrent int
	ResetAtMs     *int64
	RunsUsed      int
	CostUsed      float64
	RevokedAtMs   *int64
	// ExpiresAtMs is the grant lifetime: after it, the grant is treated as
	// invalid and borrowing must be re-approved. nil = no expiry.
	ExpiresAtMs *int64
	CreatedAtMs int64
	// CreatedBy is who issued the grant: the owner for a room authorization,
	// the adding member for a platform-agent grant.
	CreatedBy string
	// Source is who issued the grant: "" = the agent's owner (room
	// authorization), GrantSourcePlatform = a room member adding a published
	// platform agent.
	Source string
}

// GrantSourcePlatform marks a platform-agent room grant: unlimited, no
// expiry, valid only while the platform policy still publishes the agent
// (checked at reserve time), and outside the tenant's borrow ceilings.
const GrantSourcePlatform = "platform"

// AuditRecord captures one allow/deny decision for a shared-agent action.
type AuditRecord struct {
	RoomID      string
	TenantID    string
	GrantID     string
	ActorUserID string
	OwnerUserID string
	Action      string // start|resume|settings|turn|release
	Decision    string // allow|deny
	DenyReason  string
	Model       string
	// ChannelID is the room channel the audited action originated in (empty
	// for room-wide actions or pre-channel records).
	ChannelID string
	AtMs      int64
	// Cost is the run cost the reporter supplied for a recorded borrow turn;
	// nil when none was reported (distinct from a reported zero), so usage
	// reports never present an absent cost as a free run.
	Cost *float64
	// InputTokens / OutputTokens are the recorded borrow turn's token usage
	// when the reporter supplied it (the core reported metrics); nil when not
	// reported, never a fabricated zero.
	InputTokens  *int64
	OutputTokens *int64
	// ReservationID is set on a borrowed run's record written at reserve
	// time (R8); SettledAtMs once the run's usage was attached.
	ReservationID string
	SettledAtMs   *int64
}

// GrantStore persists shared-agent grants, concurrency leases, and audit
// records. Implementations must be safe for concurrent use.
type GrantStore interface {
	PutGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, grantID string) (Grant, error)
	RevokeGrant(ctx context.Context, grantID string, atMs int64) error
	// AcquireLease atomically checks live concurrency against MaxConcurrent and
	// records a lease. Returns ErrConcurrencyFull when the ceiling is reached.
	AcquireLease(ctx context.Context, grantID, leaseID string, atMs int64) error
	ReleaseLease(ctx context.Context, leaseID string, atMs int64) error
	ActiveLeases(ctx context.Context, grantID string) int
	// ConsumeRun atomically enforces the grant's RunQuota and CostAllowance and,
	// only when both still admit the run, increments the run counter and cost.
	// It returns ErrRunQuotaExhausted / ErrCostAllowanceExhausted (nothing
	// consumed) when a ceiling would be exceeded, ErrGrantNotFound for an absent
	// grant, and the updated grant on success. The check and the increment are
	// one atomic step, so concurrent consumers can never overrun a ceiling.
	ConsumeRun(ctx context.Context, grantID string, cost float64) (Grant, error)
	// TenantUsage sums runs_used and cost_used across the ctx tenant's
	// owner-issued grants (platform grants are outside the org's borrow
	// ceilings), for org-level usage accounting and ceiling enforcement.
	TenantUsage(ctx context.Context) (runsUsed int, costUsed float64)
	// AppendAudit records a decision, returning any persistence error.
	AppendAudit(ctx context.Context, rec AuditRecord) error
	// AuditRecords returns audit records for a room newest-first. limit<=0
	// defaults to 100 and is capped at 500; beforeAtMs>0 returns only records
	// strictly older than that timestamp for cursor pagination.
	AuditRecords(ctx context.Context, roomID string, limit int, beforeAtMs int64) []AuditRecord
	// TenantAuditRecords returns audit records for the ctx tenant across ALL
	// rooms newest-first, for an org-wide audit trail. limit<=0 defaults to 100
	// and is capped at 500; beforeAtMs>0 returns only records strictly older
	// than that timestamp for cursor pagination.
	TenantAuditRecords(ctx context.Context, limit int, beforeAtMs int64) []AuditRecord
	// RoomGrants returns all grants in a room (callers filter revoked/expired).
	RoomGrants(ctx context.Context, roomID string) []Grant
	// BorrowUsageStore: borrow-turn reporting and quota raises (F3).
	BorrowUsageStore
	// RunReservationStore: reserve-then-settle borrowed runs (R8).
	RunReservationStore
}

// leaseTTLms bounds how long a lease counts as active. A lease older than this
// is treated as released when enforcing MaxConcurrent, so a crashed borrower
// that never released cannot permanently consume a concurrency slot.
const leaseTTLms int64 = 5 * 60 * 1000

// memLease is an in-memory active lease with its acquisition time for TTL.
type memLease struct {
	tenant       string
	grantID      string
	acquiredAtMs int64
}

// grantKey namespaces the in-memory grant/lease maps by tenant so two tenants
// that reuse the same grant or lease id can never collide or cross-read.
func grantKey(tenant, id string) string { return tenant + "\x00" + id }

// MemoryGrant is an in-memory GrantStore for M2 and tests.
type MemoryGrant struct {
	mu     sync.Mutex
	grants map[string]Grant
	leases map[string]memLease // leaseID -> lease (active only)
	audit  []AuditRecord
}

// NewMemoryGrant constructs an empty in-memory grant store.
func NewMemoryGrant() *MemoryGrant {
	return &MemoryGrant{grants: map[string]Grant{}, leases: map[string]memLease{}}
}

// PutGrant stores or replaces a grant.
func (s *MemoryGrant) PutGrant(ctx context.Context, g Grant) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	g.TenantID = tnt
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[grantKey(tnt, g.GrantID)] = g
	return nil
}

// GetGrant returns a grant or ErrGrantNotFound.
func (s *MemoryGrant) GetGrant(ctx context.Context, grantID string) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[grantKey(tnt, grantID)]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	return g, nil
}

// RevokeGrant marks a grant revoked.
func (s *MemoryGrant) RevokeGrant(ctx context.Context, grantID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, grantID)
	g, ok := s.grants[key]
	if !ok {
		return ErrGrantNotFound
	}
	g.RevokedAtMs = &atMs
	s.grants[key] = g
	return nil
}

// AcquireLease atomically enforces the concurrency ceiling. Leases older than
// leaseTTLms are treated as released when counting active leases.
func (s *MemoryGrant) AcquireLease(ctx context.Context, grantID, leaseID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[grantKey(tnt, grantID)]
	if !ok {
		return ErrGrantNotFound
	}
	if s.activeLeasesLocked(tnt, grantID, atMs) >= g.MaxConcurrent {
		return ErrConcurrencyFull
	}
	s.leases[grantKey(tnt, leaseID)] = memLease{tenant: tnt, grantID: grantID, acquiredAtMs: atMs}
	return nil
}

// ReleaseLease drops an active lease.
func (s *MemoryGrant) ReleaseLease(ctx context.Context, leaseID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.leases, grantKey(tnt, leaseID))
	return nil
}

// activeLeasesLocked counts non-expired active leases for a grant relative to
// nowMs. Caller holds s.mu.
func (s *MemoryGrant) activeLeasesLocked(tnt, grantID string, nowMs int64) int {
	active := 0
	for _, l := range s.leases {
		if l.tenant == tnt && l.grantID == grantID && l.acquiredAtMs > nowMs-leaseTTLms {
			active++
		}
	}
	return active
}

// ActiveLeases counts held (non-released) leases for a grant. Like the MySQL
// impl it does not apply the TTL window; the TTL only governs admission in
// AcquireLease, whose caller supplies the authoritative clock. Applying a
// wall-clock TTL here would diverge from the injected acquisition clock.
func (s *MemoryGrant) ActiveLeases(ctx context.Context, grantID string) int {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, l := range s.leases {
		if l.tenant == tnt && l.grantID == grantID {
			active++
		}
	}
	return active
}

// ConsumeRun increments the run and cost counters.
func (s *MemoryGrant) ConsumeRun(ctx context.Context, grantID string, cost float64) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, grantID)
	g, ok := s.grants[key]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	if err := g.admitRun(cost); err != nil {
		return Grant{}, err
	}
	g.RunsUsed++
	g.CostUsed += cost
	s.grants[key] = g
	return g, nil
}

// admitRun reports whether one more run of the given cost fits the grant's
// ceilings (nil ceiling = unlimited). Shared by the memory store and the MySQL
// store's post-miss classification so both report the same error.
func (g Grant) admitRun(cost float64) error {
	if g.RunQuota != nil && g.RunsUsed >= *g.RunQuota {
		return ErrRunQuotaExhausted
	}
	if g.CostAllowance != nil && g.CostUsed+cost > *g.CostAllowance {
		return ErrCostAllowanceExhausted
	}
	return nil
}

// TenantUsage sums runs_used and cost_used across the ctx tenant's
// owner-issued grants. A missing tenant yields zero usage.
func (s *MemoryGrant) TenantUsage(ctx context.Context) (int, float64) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var runs int
	var cost float64
	for _, g := range s.grants {
		if g.TenantID != tnt || g.Source == GrantSourcePlatform {
			continue
		}
		runs += g.RunsUsed
		cost += g.CostUsed
	}
	return runs, cost
}

// AppendAudit records a decision. The in-memory store never fails.
func (s *MemoryGrant) AppendAudit(ctx context.Context, rec AuditRecord) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	rec.TenantID = tnt
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, rec)
	return nil
}

// AuditRecords returns a room's audit records newest-first. limit<=0 defaults
// to 100 (capped at 500); beforeAtMs>0 keeps only records strictly older than
// that timestamp for cursor pagination.
func (s *MemoryGrant) AuditRecords(ctx context.Context, roomID string, limit int, beforeAtMs int64) []AuditRecord {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := make([]AuditRecord, 0, len(s.audit))
	for _, rec := range s.audit {
		if rec.TenantID != tnt || rec.RoomID != roomID {
			continue
		}
		if beforeAtMs > 0 && rec.AtMs >= beforeAtMs {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtMs > out[j].AtMs })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// TenantAuditRecords returns the ctx tenant's audit records across ALL rooms
// newest-first for an org-wide audit trail. limit<=0 defaults to 100 (capped at
// 500); beforeAtMs>0 keeps only records strictly older than that timestamp.
func (s *MemoryGrant) TenantAuditRecords(ctx context.Context, limit int, beforeAtMs int64) []AuditRecord {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := make([]AuditRecord, 0, len(s.audit))
	for _, rec := range s.audit {
		if rec.TenantID != tnt {
			continue
		}
		if beforeAtMs > 0 && rec.AtMs >= beforeAtMs {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtMs > out[j].AtMs })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RoomGrants returns all grants in a room (callers filter revoked/expired).
func (s *MemoryGrant) RoomGrants(ctx context.Context, roomID string) []Grant {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Grant, 0)
	for _, g := range s.grants {
		if g.TenantID != tnt || g.RoomID != roomID {
			continue
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAtMs > out[j].CreatedAtMs })
	return out
}
