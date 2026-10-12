package store

import (
	"context"
	"strings"
)

// ContentPromptUseStore counts how often each prompt of a prompt pack was
// inserted (docs/prd/2026-10-12-experts-and-teams.md §4 "Usage counts"):
// one write per insertion in a composer, never per keystroke.
type ContentPromptUseStore interface {
	// RecordContentPromptUse counts one insertion of a pack's prompt.
	RecordContentPromptUse(ctx context.Context, componentID, promptID string, atMs int64) error
	// ContentPromptUses returns the use counts of the given components'
	// prompts (component -> prompt -> uses; unused prompts are absent).
	ContentPromptUses(ctx context.Context, componentIDs []string) (map[string]map[string]int64, error)
}

func (s *MemoryContentComponents) RecordContentPromptUse(_ context.Context, componentID, promptID string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[componentID]; !ok {
		return ErrContentNotFound
	}
	if s.promptUses[componentID] == nil {
		s.promptUses[componentID] = map[string]int64{}
	}
	s.promptUses[componentID][promptID]++
	return nil
}

func (s *MemoryContentComponents) ContentPromptUses(_ context.Context, componentIDs []string) (map[string]map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]int64{}
	for _, id := range componentIDs {
		if counts := s.promptUses[id]; len(counts) > 0 {
			out[id] = map[string]int64{}
			for p, n := range counts {
				out[id][p] = n
			}
		}
	}
	return out, nil
}

// RecordContentPromptUse upserts the prompt's counter (one row per prompt,
// written once per insertion). A component that does not exist is
// ErrContentNotFound.
func (s *MySQLContentComponents) RecordContentPromptUse(ctx context.Context, componentID, promptID string, atMs int64) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	res, err := s.DB.ExecContext(ctx, `INSERT INTO content_prompt_uses (component_id, prompt_id, use_count, last_used_at_ms)
  SELECT id, ?, 1, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE use_count = use_count + 1, last_used_at_ms = GREATEST(last_used_at_ms, VALUES(last_used_at_ms))`,
		promptID, atMs, componentID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) ContentPromptUses(ctx context.Context, componentIDs []string) (map[string]map[string]int64, error) {
	out := map[string]map[string]int64{}
	if len(componentIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(componentIDs))
	for i, id := range componentIDs {
		args[i] = id
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT component_id, prompt_id, use_count FROM content_prompt_uses
  WHERE component_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(componentIDs)), ",")+`)`, args...)
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
