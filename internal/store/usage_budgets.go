package store

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Usage budgets (R12): monthly token/cost budgets per member and per tenant,
// set by org admins. The burn they are measured against is the reported reply
// usage (AgentUsageReader), attributed to the agent's OWNER — a borrowed run
// consumes the owner's agent and counts against the owner's budget.

// UsageBudget is one budget of the ctx tenant. UserID "" is the tenant-wide
// budget. A nil Tokens / Cost is no limit on that dimension; at least one is
// set.
type UsageBudget struct {
	UserID string
	Tokens *int64
	Cost   *float64
	// WarnAtPercent is the burn percentage (1..99) that sends the first
	// warning; reaching 100% sends the second.
	WarnAtPercent int
	// HardStop refuses the member's own agent drives (for the tenant budget:
	// every drive in the tenant) once the budget is reached.
	HardStop    bool
	UpdatedBy   string
	UpdatedAtMs int64
}

// sameLimits reports whether two budgets warn at the same points (a change
// re-arms the month's warnings).
func (b UsageBudget) sameLimits(o UsageBudget) bool {
	eqI := func(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	eqF := func(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	return eqI(b.Tokens, o.Tokens) && eqF(b.Cost, o.Cost) && b.WarnAtPercent == o.WarnAtPercent
}

// UsageBudgetStore persists the ctx tenant's usage budgets and which warning
// was already sent this period.
type UsageBudgetStore interface {
	// ListUsageBudgets returns the ctx tenant's budgets, the tenant budget
	// (UserID "") first, then members by user id.
	ListUsageBudgets(ctx context.Context) ([]UsageBudget, error)
	// GetUsageBudget returns one budget (userID "" = the tenant budget).
	GetUsageBudget(ctx context.Context, userID string) (UsageBudget, bool, error)
	// ReplaceUsageBudgets makes budgets the ctx tenant's whole budget set:
	// absent budgets are deleted, present ones upserted. A budget whose
	// limits changed re-arms its warnings for the period.
	ReplaceUsageBudgets(ctx context.Context, budgets []UsageBudget) error
	// ClaimBudgetWarning records that the warning at level (a percentage) of
	// userID's budget is sent for period ("YYYY-MM"). It returns true for
	// exactly one caller per (budget, period, level), and false when that or
	// a higher level was already sent this period (or the budget is gone).
	ClaimBudgetWarning(ctx context.Context, userID, period string, level int) (bool, error)
}

type memBudget struct {
	b            UsageBudget
	warnedPeriod string
	warnedLevel  int
}

// MemoryUsageBudgets is the in-memory UsageBudgetStore.
type MemoryUsageBudgets struct {
	mu   sync.Mutex
	rows map[string]map[string]*memBudget // tenant -> lower(user) -> row
}

// NewMemoryUsageBudgets returns an empty in-memory budget store.
func NewMemoryUsageBudgets() *MemoryUsageBudgets {
	return &MemoryUsageBudgets{rows: map[string]map[string]*memBudget{}}
}

var _ UsageBudgetStore = (*MemoryUsageBudgets)(nil)

// ListUsageBudgets implements UsageBudgetStore.
func (s *MemoryUsageBudgets) ListUsageBudgets(ctx context.Context) ([]UsageBudget, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UsageBudget, 0, len(s.rows[tnt]))
	for _, r := range s.rows[tnt] {
		out = append(out, r.b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// GetUsageBudget implements UsageBudgetStore.
func (s *MemoryUsageBudgets) GetUsageBudget(ctx context.Context, userID string) (UsageBudget, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return UsageBudget{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[tnt][strings.ToLower(userID)]
	if !ok {
		return UsageBudget{}, false, nil
	}
	return r.b, true, nil
}

// ReplaceUsageBudgets implements UsageBudgetStore.
func (s *MemoryUsageBudgets) ReplaceUsageBudgets(ctx context.Context, budgets []UsageBudget) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.rows[tnt]
	next := make(map[string]*memBudget, len(budgets))
	for _, b := range budgets {
		key := strings.ToLower(b.UserID)
		row := &memBudget{b: b}
		if prev, ok := old[key]; ok && prev.b.sameLimits(b) {
			row.warnedPeriod, row.warnedLevel = prev.warnedPeriod, prev.warnedLevel
		}
		next[key] = row
	}
	s.rows[tnt] = next
	return nil
}

// ClaimBudgetWarning implements UsageBudgetStore.
func (s *MemoryUsageBudgets) ClaimBudgetWarning(ctx context.Context, userID, period string, level int) (bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[tnt][strings.ToLower(userID)]
	if !ok || (r.warnedPeriod == period && r.warnedLevel >= level) {
		return false, nil
	}
	r.warnedPeriod, r.warnedLevel = period, level
	return true, nil
}
