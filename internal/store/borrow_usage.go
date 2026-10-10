package store

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ErrRunQuotaUnlimited reports a quota raise on a grant with no run quota
// (nil = unlimited): there is no ceiling to raise.
var ErrRunQuotaUnlimited = errors.New("store: grant has no run quota to raise")

// MaxBorrowTurnRows caps one BorrowTurns scan so a report over a huge range
// stays bounded; callers surface a truncated result when it is hit.
const MaxBorrowTurnRows = 50_000

// BorrowTurnQuery selects recorded borrow turns: allowed "turn" audit records
// whose actor (the borrower) is not the owner, at FromMs <= at_ms < ToMs.
// OwnerUserID / BorrowerUserID, when set, restrict to that owner / borrower.
// PlatformOnly keeps the turns of platform grants (GrantSourcePlatform) only.
// AllTenants drops the ctx tenant scope (the super admin's platform-agent
// usage: platform agents serve every organization); each record keeps its
// TenantID.
type BorrowTurnQuery struct {
	FromMs         int64
	ToMs           int64
	OwnerUserID    string
	BorrowerUserID string
	PlatformOnly   bool
	AllTenants     bool
}

// BorrowUsageStore is the borrow-accounting read side and the quota raise an
// approved quota request applies. It is part of GrantStore.
type BorrowUsageStore interface {
	// BorrowTurns returns the ctx tenant's (every tenant's with
	// q.AllTenants) recorded borrow turns matching q, oldest-first, at most
	// MaxBorrowTurnRows.
	BorrowTurns(ctx context.Context, q BorrowTurnQuery) []AuditRecord
	// RaiseRunQuota atomically adds `add` runs to a live (not revoked) grant's
	// RunQuota without touching its usage counters, so an approved request
	// extends the window instead of resetting history. ErrGrantNotFound for an
	// absent/revoked grant, ErrRunQuotaUnlimited when RunQuota is nil.
	RaiseRunQuota(ctx context.Context, grantID string, add int) (Grant, error)
}

// isBorrowTurn reports whether an audit record is an allowed borrow turn: the
// schema's "turn" action, allowed, by an actor other than the owner (an
// owner's own turn shares the action; actor != owner is what marks a borrow).
func isBorrowTurn(rec AuditRecord) bool {
	return rec.Action == "turn" && rec.Decision == "allow" && rec.ActorUserID != "" &&
		!strings.EqualFold(rec.ActorUserID, rec.OwnerUserID)
}

// BorrowTurns scans the in-memory audit trail (see BorrowUsageStore).
func (s *MemoryGrant) BorrowTurns(ctx context.Context, q BorrowTurnQuery) []AuditRecord {
	tnt := tenant.FromContext(ctx)
	if !q.AllTenants {
		var err error
		if tnt, err = tenant.Require(ctx); err != nil {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AuditRecord
	for _, rec := range s.audit {
		if (!q.AllTenants && rec.TenantID != tnt) || !isBorrowTurn(rec) || rec.AtMs < q.FromMs || (q.ToMs > 0 && rec.AtMs >= q.ToMs) {
			continue
		}
		if q.PlatformOnly && s.grants[grantKey(rec.TenantID, rec.GrantID)].Source != GrantSourcePlatform {
			continue
		}
		if q.OwnerUserID != "" && !strings.EqualFold(rec.OwnerUserID, q.OwnerUserID) {
			continue
		}
		if q.BorrowerUserID != "" && !strings.EqualFold(rec.ActorUserID, q.BorrowerUserID) {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtMs < out[j].AtMs })
	if len(out) > MaxBorrowTurnRows {
		out = out[:MaxBorrowTurnRows]
	}
	return out
}

// RaiseRunQuota raises a live grant's run quota (see BorrowUsageStore).
func (s *MemoryGrant) RaiseRunQuota(ctx context.Context, grantID string, add int) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, grantID)
	g, ok := s.grants[key]
	if !ok || g.RevokedAtMs != nil {
		return Grant{}, ErrGrantNotFound
	}
	if g.RunQuota == nil {
		return Grant{}, ErrRunQuotaUnlimited
	}
	next := *g.RunQuota + add
	g.RunQuota = &next
	s.grants[key] = g
	return g, nil
}
