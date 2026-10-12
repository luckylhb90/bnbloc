package store

import (
	"context"
	"errors"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Exact borrow quota (R8): reserve-then-settle.
//
// A borrowed run reserves its quota BEFORE the owner's bridge drives it:
// ReserveRun admits the run against the grant's ceilings and increments
// runs_used in one atomic step, and writes the run's audit record (an allowed
// "turn" by the borrower) keyed by a reservation id. Reserved runs therefore
// count against the quota immediately, so concurrent borrows can never exceed
// it. Once the drive ends, SettleRun attaches the usage the turn reported
// (model, tokens, cost) to that audit record and adds the cost to the grant.
//
// Cost semantics: a run cannot be undone once it has run, so the cost
// allowance is checked at reserve time against the cost settled so far
// (cost_used < cost_allowance admits one more run) and the actual cost is
// added at settle, even when it carries the grant past its allowance — the
// next reservation is then refused.
//
// A reservation that is never settled (the bridge crashed mid-drive) expires
// after RunReservationTTLms: it stays counted as a run (the agent most likely
// ran), keeps its audit record without usage, and can no longer be settled.

// RunReservationTTLms is how long a reservation may be settled.
const RunReservationTTLms int64 = 30 * 60 * 1000

// Run reservation errors.
var (
	ErrReservationNotFound = errors.New("store: run reservation not found")
	ErrReservationSettled  = errors.New("store: run reservation already settled")
	ErrReservationExpired  = errors.New("store: run reservation expired")
	// ErrReservationExists: the reservation id is taken in the tenant
	// (nothing is consumed or recorded).
	ErrReservationExists = errors.New("store: run reservation exists")
)

// RunReservation is one borrowed run admitted against a grant.
type RunReservation struct {
	ReservationID  string
	GrantID        string
	RoomID         string
	OwnerUserID    string
	BorrowerUserID string
	// Model is the model the job requested ("" = none); settle replaces it
	// with the model the core reported.
	Model     string
	ChannelID string
	AtMs      int64
}

// RunUsage is what a settled run reported. Nil fields were not reported.
type RunUsage struct {
	Model        string
	Cost         *float64
	InputTokens  *int64
	OutputTokens *int64
}

// RunReservationStore is the reserve/settle side of GrantStore.
type RunReservationStore interface {
	// ReserveRun atomically admits one run on a live grant (present, not
	// revoked, not expired at r.AtMs) whose run quota and cost allowance
	// still admit it, increments runs_used, and records the run's audit
	// record under r.ReservationID. ErrGrantNotFound for an absent or
	// non-live grant, ErrRunQuotaExhausted / ErrCostAllowanceExhausted when a
	// ceiling refuses it, ErrReservationExists for a reservation id the
	// tenant already used (nothing is consumed or recorded then).
	ReserveRun(ctx context.Context, r RunReservation) (Grant, error)
	// SettleRun attaches u to the reservation of grantID and adds u.Cost to
	// the grant's cost_used. ErrReservationNotFound / ErrReservationSettled /
	// ErrReservationExpired (at nowMs) refuse it without changing anything.
	SettleRun(ctx context.Context, grantID, reservationID string, u RunUsage, nowMs int64) error
}

// admitReservation is the one admission rule of a borrowed run: the grant is
// live at atMs (Grant.Live), and each ceiling admits used + cost <= allowance
// (nil = unlimited) where a reservation costs one run and the cost settled
// so far — the run's own cost is unknown until settle, so some allowance must
// be left (cost_used < allowance). The MySQL ReserveRun's conditional UPDATE
// is this rule in SQL; a miss is classified by it.
func (g Grant) admitReservation(atMs int64) error {
	if !g.Live(atMs) {
		return ErrGrantNotFound
	}
	if g.RunQuota != nil && g.RunsUsed+1 > *g.RunQuota {
		return ErrRunQuotaExhausted
	}
	if g.CostAllowance != nil && g.CostUsed >= *g.CostAllowance {
		return ErrCostAllowanceExhausted
	}
	return nil
}

// reservationAudit is the audit record a reservation writes.
func reservationAudit(tnt string, r RunReservation) AuditRecord {
	return AuditRecord{
		RoomID: r.RoomID, TenantID: tnt, GrantID: r.GrantID,
		ActorUserID: r.BorrowerUserID, OwnerUserID: r.OwnerUserID,
		// "turn" is the schema's enum value for a run; actor != owner marks
		// it a borrow.
		Action: "turn", Decision: "allow", Model: r.Model, ChannelID: r.ChannelID, AtMs: r.AtMs,
		ReservationID: r.ReservationID,
	}
}

// ReserveRun implements RunReservationStore for the memory store.
func (s *MemoryGrant) ReserveRun(ctx context.Context, r RunReservation) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, r.GrantID)
	g, ok := s.grants[key]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	if err := g.admitReservation(r.AtMs); err != nil {
		return Grant{}, err
	}
	for _, rec := range s.audit {
		if rec.TenantID == tnt && rec.ReservationID == r.ReservationID {
			return Grant{}, ErrReservationExists
		}
	}
	g.RunsUsed++
	s.grants[key] = g
	s.appendAuditLocked(reservationAudit(tnt, r))
	return g, nil
}

// SettleRun implements RunReservationStore for the memory store.
func (s *MemoryGrant) SettleRun(ctx context.Context, grantID, reservationID string, u RunUsage, nowMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.audit {
		rec := &s.audit[i]
		if rec.TenantID != tnt || rec.ReservationID == "" || rec.ReservationID != reservationID || rec.GrantID != grantID {
			continue
		}
		switch {
		case rec.SettledAtMs != nil:
			return ErrReservationSettled
		case nowMs >= rec.AtMs+RunReservationTTLms:
			return ErrReservationExpired
		}
		settled := nowMs
		rec.SettledAtMs = &settled
		if m := strings.TrimSpace(u.Model); m != "" {
			rec.Model = m
		}
		rec.Cost, rec.InputTokens, rec.OutputTokens = u.Cost, u.InputTokens, u.OutputTokens
		if u.Cost != nil {
			key := grantKey(tnt, grantID)
			if g, ok := s.grants[key]; ok {
				g.CostUsed += *u.Cost
				s.grants[key] = g
			}
		}
		return nil
	}
	return ErrReservationNotFound
}
