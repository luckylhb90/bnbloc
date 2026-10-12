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
	ErrGrantNotFound = errors.New("store: grant not found")
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

// Live reports whether the grant is in force at atMs: not revoked and not
// expired. It is the one liveness rule for a grant (TestGrantLivenessOwned):
// every reader — the reservation, a quota raise, the room listings, the
// policy-aware liveAuthorization — asks it, never the fields.
func (g Grant) Live(atMs int64) bool {
	return g.RevokedAtMs == nil && (g.ExpiresAtMs == nil || atMs < *g.ExpiresAtMs)
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
	// ID is the record's row id (MySQL audit_id; the memory store numbers
	// appends the same way): the tie-break of the audit order.
	ID int64
}

// Cursor is the record's position in the audit merge order.
func (r AuditRecord) Cursor() AuditCursor {
	return AuditCursor{AtMs: r.AtMs, Source: AuditSourceBorrow, ID: r.ID}
}

// AuditRecordQuery selects one page of the borrow audit trail.
type AuditRecordQuery struct {
	// RoomID keeps one room's records ("" = every room of the ctx tenant).
	RoomID string
	// Limit is the page size: <= 0 means DefaultAuditPage, at most
	// MaxAuditPage.
	Limit int
	// After continues after a record a previous page returned (its
	// Cursor()); nil is the first page. A cursor with ID 0 keeps the records
	// strictly older than its AtMs.
	After *AuditCursor
}

// Audit page sizes (AuditRecords, MergeAuditLog).
const (
	DefaultAuditPage = 100
	MaxAuditPage     = 500
)

// auditPage is the page size of an audit read.
func auditPage(n int) int { return clampLimit(n, DefaultAuditPage, MaxAuditPage) }

// GrantStore persists shared-agent grants and audit records.
// Implementations must be safe for concurrent use.
type GrantStore interface {
	PutGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, grantID string) (Grant, error)
	RevokeGrant(ctx context.Context, grantID string, atMs int64) error
	// TenantUsage sums runs_used and cost_used across the ctx tenant's
	// owner-issued grants (platform grants are outside the org's borrow
	// ceilings), for org-level usage accounting and ceiling enforcement.
	TenantUsage(ctx context.Context) (runsUsed int, costUsed float64, err error)
	// AppendAudit records a decision, returning any persistence error.
	AppendAudit(ctx context.Context, rec AuditRecord) error
	// AuditRecords returns one page of the ctx tenant's borrow audit trail
	// (one room's, or every room's for the org-wide trail), newest first in
	// the audit merge order: at_ms DESC, then row id DESC (auditBefore), so a
	// page boundary never drops rows sharing a millisecond. Every column is
	// returned (cost, tokens, reservation). Empty is [] (never nil).
	AuditRecords(ctx context.Context, q AuditRecordQuery) ([]AuditRecord, error)
	// RoomGrants returns all grants in a room, newest first (callers filter
	// revoked/expired).
	RoomGrants(ctx context.Context, roomID string) ([]Grant, error)
	// BorrowUsageStore: borrow-turn reporting and quota raises (F3).
	BorrowUsageStore
	// RunReservationStore: reserve-then-settle borrowed runs (R8).
	RunReservationStore
}

// grantKey namespaces the in-memory grant map by tenant so two tenants that
// reuse the same grant id can never collide or cross-read.
func grantKey(tenant, id string) string { return tenant + "\x00" + id }

// MemoryGrant is an in-memory GrantStore for M2 and tests.
type MemoryGrant struct {
	mu     sync.Mutex
	grants map[string]Grant
	audit  []AuditRecord
	// auditSeq numbers appended audit records (AuditRecord.ID), like the
	// MySQL audit_id.
	auditSeq int64
}

// NewMemoryGrant constructs an empty in-memory grant store.
func NewMemoryGrant() *MemoryGrant {
	return &MemoryGrant{grants: map[string]Grant{}}
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
	key := grantKey(tnt, g.GrantID)
	if prev, ok := s.grants[key]; ok {
		// A re-put changes what the SQL upsert changes: the grant's room,
		// target, owner and grantee stay (the id is derived from them).
		g.RoomID, g.TargetID, g.OwnerUserID, g.GranteeUserID = prev.RoomID, prev.TargetID, prev.OwnerUserID, prev.GranteeUserID
	}
	s.grants[key] = g
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

// TenantUsage sums runs_used and cost_used across the ctx tenant's
// owner-issued grants.
func (s *MemoryGrant) TenantUsage(ctx context.Context) (int, float64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, 0, err
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
	return runs, cost, nil
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
	s.appendAuditLocked(rec)
	return nil
}

// appendAuditLocked appends rec under the next row id. Caller holds s.mu.
func (s *MemoryGrant) appendAuditLocked(rec AuditRecord) {
	s.auditSeq++
	rec.ID = s.auditSeq
	s.audit = append(s.audit, rec)
}

// AuditRecords returns one page of the ctx tenant's borrow audit trail (see
// GrantStore).
func (s *MemoryGrant) AuditRecords(ctx context.Context, q AuditRecordQuery) ([]AuditRecord, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auditPageLocked(AuditLogQuery{TenantID: tnt, After: q.After, Limit: auditPage(q.Limit)}, q.RoomID), nil
}

// auditPageLocked is the one memory read of the borrow trail (AuditRecords
// and the AuditLog reader): the records q and roomID ("" = any) admit, in
// merge order, at most q.Limit. Caller holds s.mu.
func (s *MemoryGrant) auditPageLocked(q AuditLogQuery, roomID string) []AuditRecord {
	out := []AuditRecord{}
	for _, rec := range s.audit {
		if (roomID == "" || rec.RoomID == roomID) && q.matches(borrowAuditRow(rec)) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return auditBefore(out[i].Cursor(), out[j].Cursor()) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

// RoomGrants returns all grants in a room newest first (callers filter
// revoked/expired).
func (s *MemoryGrant) RoomGrants(ctx context.Context, roomID string) ([]Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].GrantID > out[j].GrantID
	})
	return out, nil
}
