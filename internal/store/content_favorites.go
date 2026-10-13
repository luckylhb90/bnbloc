package store

import (
	"context"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ContentFavorite is a member's star on a content component
// (docs/prd/2026-10-13-app-gallery.md §7: the gallery's favorites), in one
// organization: built-in and platform content is starred in every
// organization, and one's stars never show in another's counts.
type ContentFavorite struct {
	TenantID    string
	Member      string
	ComponentID string
	CreatedAtMs int64
}

// ContentFavoriteStore keeps the members' favorites. Every read and write
// names the tenant (tenant.ErrNoTenant without one). A component's
// favorites are deleted with it.
type ContentFavoriteStore interface {
	// PutContentFavorite stars a component for a member (a repeat keeps
	// the first star). A component that does not exist is
	// ErrContentNotFound; a built-in one has no row (the overlay vouches
	// for it).
	PutContentFavorite(ctx context.Context, f ContentFavorite) error
	// DeleteContentFavorite removes a member's star (none is no error).
	DeleteContentFavorite(ctx context.Context, tenantID, member, componentID string) error
	// ListContentFavorites returns a member's stars in tenant, newest
	// first, at most MaxContentList; componentIDs narrows them to those
	// components (nil: every star).
	ListContentFavorites(ctx context.Context, tenantID, member string, componentIDs []string) ([]ContentFavorite, error)
	// CountContentFavorites returns, per component, how many of tenant's
	// members starred it (capped at MaxUsageCount, a badge count; ids
	// without a star are absent).
	CountContentFavorites(ctx context.Context, tenantID string, componentIDs []string) (map[string]int, error)
}

var (
	_ ContentFavoriteStore = (*MemoryContentComponents)(nil)
	_ ContentFavoriteStore = (*MySQLContentComponents)(nil)
)

func favoriteKey(tenantID, member, componentID string) string {
	return tenantID + "\x00" + member + "\x00" + componentID
}

func (s *MemoryContentComponents) PutContentFavorite(_ context.Context, f ContentFavorite) error {
	if f.TenantID == "" {
		return tenant.ErrNoTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[f.ComponentID]; !ok && !strings.HasPrefix(f.ComponentID, BuiltinContentIDPrefix) {
		return ErrContentNotFound
	}
	k := favoriteKey(f.TenantID, f.Member, f.ComponentID)
	if _, ok := s.favorites[k]; !ok {
		s.favorites[k] = f
	}
	return nil
}

func (s *MemoryContentComponents) DeleteContentFavorite(_ context.Context, tenantID, member, componentID string) error {
	if tenantID == "" {
		return tenant.ErrNoTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.favorites, favoriteKey(tenantID, member, componentID))
	return nil
}

func (s *MemoryContentComponents) ListContentFavorites(_ context.Context, tenantID, member string, componentIDs []string) ([]ContentFavorite, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	want := map[string]bool{}
	for _, id := range componentIDs {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentFavorite{}
	for _, f := range s.favorites {
		if f.TenantID == tenantID && f.Member == member && (componentIDs == nil || want[f.ComponentID]) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return out[:min(len(out), MaxContentList)], nil
}

func (s *MemoryContentComponents) CountContentFavorites(_ context.Context, tenantID string, componentIDs []string) (map[string]int, error) {
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
	for _, f := range s.favorites {
		if f.TenantID == tenantID && want[f.ComponentID] && out[f.ComponentID] < MaxUsageCount {
			out[f.ComponentID]++
		}
	}
	return out, nil
}

// PutContentFavorite inserts the star when the component exists (a
// built-in one has no row to check); a repeat matches the existing row.
func (s *MySQLContentComponents) PutContentFavorite(ctx context.Context, f ContentFavorite) error {
	if f.TenantID == "" {
		return tenant.ErrNoTenant
	}
	if strings.HasPrefix(f.ComponentID, BuiltinContentIDPrefix) {
		_, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO content_favorites (tenant_id, member_id, component_id, created_at_ms) VALUES (?, ?, ?, ?)`,
			f.TenantID, f.Member, f.ComponentID, f.CreatedAtMs)
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO content_favorites (tenant_id, member_id, component_id, created_at_ms)
  SELECT ?, ?, id, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE content_favorites.created_at_ms = content_favorites.created_at_ms`, f.TenantID, f.Member, f.CreatedAtMs, f.ComponentID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) DeleteContentFavorite(ctx context.Context, tenantID, member, componentID string) error {
	if tenantID == "" {
		return tenant.ErrNoTenant
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM content_favorites WHERE tenant_id = ? AND member_id = ? AND component_id = ?`, tenantID, member, componentID)
	return err
}

func (s *MySQLContentComponents) ListContentFavorites(ctx context.Context, tenantID, member string, componentIDs []string) ([]ContentFavorite, error) {
	if tenantID == "" {
		return nil, tenant.ErrNoTenant
	}
	if componentIDs != nil && len(componentIDs) == 0 {
		return []ContentFavorite{}, nil
	}
	q := `SELECT tenant_id, member_id, component_id, created_at_ms FROM content_favorites WHERE tenant_id = ? AND member_id = ?`
	args := []any{tenantID, member}
	if componentIDs != nil {
		q += ` AND component_id IN (` + inPlaceholders(len(componentIDs)) + `)`
		for _, id := range componentIDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY created_at_ms DESC, component_id ASC LIMIT ?`
	args = append(args, MaxContentList)
	return queryAll(ctx, s.DB, q, func(r rowScanner) (ContentFavorite, error) {
		var f ContentFavorite
		err := r.Scan(&f.TenantID, &f.Member, &f.ComponentID, &f.CreatedAtMs)
		return f, err
	}, args...)
}

// CountContentFavorites reads idx_content_favorites_component, bounded per
// component.
func (s *MySQLContentComponents) CountContentFavorites(ctx context.Context, tenantID string, componentIDs []string) (map[string]int, error) {
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
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM content_favorites WHERE component_id = ? AND tenant_id = ? LIMIT ?) f`,
			id, tenantID, MaxUsageCount).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out, nil
}
