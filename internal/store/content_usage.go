package store

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Content usage counts (docs/prd/2026-10-12-experts-and-teams.md, step 6):
// the Library shows, per component, how many runs wore an expert and how
// many members used it. An expert's runs and members come from the jobs
// that pinned it (agent_jobs.expert_id, on idx_agent_jobs_expert); a skill
// or prompt pack's members are the members who turned it on for
// themselves (content uses of member scope).

// MaxUsageCount caps each usage count: a count shown on a card is a badge,
// so its scan is bounded like the unread counts (MaxUnreadCount).
const MaxUsageCount = 9999

// ContentUsage is one component's usage counts, each capped at
// MaxUsageCount.
type ContentUsage struct {
	// Runs are jobs that wore the expert (experts only).
	Runs int
	// Members are distinct requesters of those jobs (experts), or members
	// who turned the pack on for themselves (packs).
	Members int
}

// ExpertUsageReader counts the jobs that wore experts.
type ExpertUsageReader interface {
	// ExpertUsage returns, per expert id, the ctx tenant's jobs that wore
	// it and their distinct requesters (ids without jobs are absent).
	ExpertUsage(ctx context.Context, expertIDs []string) (map[string]ContentUsage, error)
}

var (
	_ ExpertUsageReader = (*MemoryBridgeJobs)(nil)
	_ ExpertUsageReader = (*MySQLBridgeJobs)(nil)
)

// ExpertUsage implements ExpertUsageReader for the memory store.
func (s *MemoryBridgeJobs) ExpertUsage(ctx context.Context, expertIDs []string) (map[string]ContentUsage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range expertIDs {
		if id != "" {
			want[id] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := map[string]int{}
	members := map[string]map[string]bool{}
	for _, j := range s.jobs {
		if j.TenantID != tid || !want[j.ExpertID] {
			continue
		}
		runs[j.ExpertID]++
		if members[j.ExpertID] == nil {
			members[j.ExpertID] = map[string]bool{}
		}
		members[j.ExpertID][j.RequesterUserID] = true
	}
	// Each count is capped on its own, as MySQL's two bounded reads are.
	out := map[string]ContentUsage{}
	for id, n := range runs {
		out[id] = ContentUsage{Runs: min(n, MaxUsageCount), Members: min(len(members[id]), MaxUsageCount)}
	}
	return out, nil
}

// ExpertUsage implements ExpertUsageReader for MySQL: per expert, two
// bounded reads on idx_agent_jobs_expert (each stops at MaxUsageCount+1
// rows, so a popular expert never scans its whole history).
func (s *MySQLBridgeJobs) ExpertUsage(ctx context.Context, expertIDs []string) (map[string]ContentUsage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ContentUsage{}
	seen := map[string]bool{}
	for _, id := range expertIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var u ContentUsage
		err := s.DB.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM (SELECT 1 FROM agent_jobs WHERE tenant_id = ? AND expert_id = ? LIMIT ?) r),
  (SELECT COUNT(*) FROM (SELECT DISTINCT requester_user_id FROM agent_jobs WHERE tenant_id = ? AND expert_id = ? LIMIT ?) m)`,
			tid, id, MaxUsageCount, tid, id, MaxUsageCount).Scan(&u.Runs, &u.Members)
		if err != nil {
			return nil, err
		}
		if u.Runs > 0 {
			out[id] = u
		}
	}
	return out, nil
}

// CountMemberUses implements ContentComponentStore for the memory store.
func (s *MemoryContentComponents) CountMemberUses(_ context.Context, tenantID string, componentIDs []string) (map[string]int, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	want := map[string]bool{}
	for _, id := range componentIDs {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, u := range s.uses {
		if u.ScopeKind == ContentUseMember && u.TenantID == tenantID && want[u.ComponentID] && out[u.ComponentID] < MaxUsageCount {
			out[u.ComponentID]++
		}
	}
	return out, nil
}

// CountMemberUses implements ContentComponentStore for MySQL (tenant's
// member uses on idx_content_uses_component, bounded per component).
func (s *MySQLContentComponents) CountMemberUses(ctx context.Context, tenantID string, componentIDs []string) (map[string]int, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	out := map[string]int{}
	seen := map[string]bool{}
	for _, id := range componentIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM content_component_uses WHERE component_id = ? AND tenant_id = ? AND scope_kind = ? LIMIT ?) u`,
			id, tenantID, string(ContentUseMember), MaxUsageCount).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out, nil
}
