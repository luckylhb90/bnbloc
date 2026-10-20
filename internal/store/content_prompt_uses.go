package store

import (
	"context"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ContentPromptUseStore counts how often each prompt of a prompt pack was
// inserted (docs/prd/2026-10-12-experts-and-teams.md §4 "Usage counts"):
// one write per insertion in a composer, never per keystroke. Counts are
// per organization: platform packs are used by every
// organization, and one's insertions never show in another's counts
// (docs/bugs/2026-10-13-starters-and-shared-use-counts.md).
type ContentPromptUseStore interface {
	// RecordContentPromptUse counts one insertion of a pack's prompt in
	// tenant; a pack that does not exist is ErrContentNotFound.
	RecordContentPromptUse(ctx context.Context, tenantID, componentID, promptID string, atMs int64) error
	// ContentPromptUses returns tenant's use counts of the given
	// components' prompts (component -> prompt -> uses; unused prompts are
	// absent).
	ContentPromptUses(ctx context.Context, tenantID string, componentIDs []string) (map[string]map[string]int64, error)
}

func (s *MemoryContentComponents) RecordContentPromptUse(_ context.Context, tenantID, componentID, promptID string, _ int64) error {
	if tenantID == "" {
		return tenant.ErrNoTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[componentID]; !ok {
		return ErrContentNotFound
	}
	if s.promptUses[componentID] == nil {
		s.promptUses[componentID] = map[string]int64{}
	}
	s.promptUses[componentID][tenantID+"\x00"+promptID]++
	return nil
}

func (s *MemoryContentComponents) ContentPromptUses(_ context.Context, tenantID string, componentIDs []string) (map[string]map[string]int64, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]int64{}
	for _, id := range componentIDs {
		for k, n := range s.promptUses[id] {
			prompt, ok := strings.CutPrefix(k, tenantID+"\x00")
			if !ok {
				continue
			}
			if out[id] == nil {
				out[id] = map[string]int64{}
			}
			out[id][prompt] = n
		}
	}
	return out, nil
}

// RecordContentPromptUse upserts the prompt's counter in tenant (one row
// per tenant and prompt, written once per insertion). A component that
// does not exist is ErrContentNotFound.
func (s *MySQLContentComponents) RecordContentPromptUse(ctx context.Context, tenantID, componentID, promptID string, atMs int64) error {
	if tenantID == "" {
		return tenant.ErrNoTenant
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO content_prompt_uses (component_id, tenant_id, prompt_id, use_count, last_used_at_ms)
  SELECT id, ?, ?, 1, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE use_count = use_count + 1, last_used_at_ms = GREATEST(last_used_at_ms, VALUES(last_used_at_ms))`,
		tenantID, promptID, atMs, componentID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) ContentPromptUses(ctx context.Context, tenantID string, componentIDs []string) (map[string]map[string]int64, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	out := map[string]map[string]int64{}
	if len(componentIDs) == 0 {
		return out, nil
	}
	args := []any{tenantID}
	for _, id := range componentIDs {
		args = append(args, id)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT component_id, prompt_id, use_count FROM content_prompt_uses
  WHERE tenant_id = ? AND component_id IN (`+inPlaceholders(len(componentIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var comp, prompt string
		var n int64
		if err := rows.Scan(&comp, &prompt, &n); err != nil {
			return nil, err
		}
		if out[comp] == nil {
			out[comp] = map[string]int64{}
		}
		out[comp][prompt] = n
	}
	return out, rows.Err()
}
