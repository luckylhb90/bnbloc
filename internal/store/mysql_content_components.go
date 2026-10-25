package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLContentComponents implements ContentComponentStore over database/sql.
type MySQLContentComponents struct{ DB *DB }

// NewMySQLContentComponents constructs a MySQL-backed content store.
func NewMySQLContentComponents(db *DB) *MySQLContentComponents {
	return &MySQLContentComponents{DB: db}
}

var _ ContentComponentStore = (*MySQLContentComponents)(nil)

const contentComponentCols = `id, kind, name, tenant_id, owner, scope, status, latest_version, display_name, description, category, source_id, shared_by, enabled_tenants, created_at_ms, updated_at_ms`

func scanContentComponent(row rowScanner) (ContentComponent, error) {
	var c ContentComponent
	var scope, status string
	var tenants sql.NullString
	if err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.TenantID, &c.Owner, &scope, &status, &c.LatestVersion, &c.DisplayName, &c.Description, &c.Category, &c.SourceID, &c.SharedBy, &tenants, &c.CreatedAtMs, &c.UpdatedAtMs); err != nil {
		return ContentComponent{}, err
	}
	c.Scope, c.Status = ContentScope(scope), ContentStatus(status)
	if tenants.Valid && tenants.String != "" && tenants.String != "null" {
		if err := json.Unmarshal([]byte(tenants.String), &c.EnabledTenants); err != nil {
			return ContentComponent{}, fmt.Errorf("content component %s enabled_tenants: %w", c.ID, err)
		}
		if len(c.EnabledTenants) == 0 {
			c.EnabledTenants = nil
		}
	}
	return c, nil
}

// contentTenantsArg is the enabled_tenants argument (NULL = every tenant).
func contentTenantsArg(tenants []string) (any, error) {
	if len(tenants) == 0 {
		return nil, nil
	}
	return jsonArg(tenants)
}

// contentOrgName is the org_name of a row: its name at organization scope
// (the column holding UNIQUE (tenant_id, kind, org_name)), NULL otherwise.
func contentOrgName(c ContentComponent) any {
	if c.Scope == ContentScopeOrg {
		return c.Name
	}
	return nil
}

// isOrgNameClash reports a duplicate on the organization name key.
func isOrgNameClash(err error) bool {
	return isDupKey(err, "uq_content_components_org_name")
}

const contentVersionCols = `component_id, version, manifest_json, bundle_blob_key, sha256, size_bytes, signature, created_by, created_at_ms`

func scanContentVersion(row rowScanner) (ContentComponentVersion, error) {
	var v ContentComponentVersion
	var manifest []byte
	var sig sql.NullString
	if err := row.Scan(&v.ComponentID, &v.Version, &manifest, &v.BundleBlobKey, &v.SHA256, &v.SizeBytes, &sig, &v.CreatedBy, &v.CreatedAtMs); err != nil {
		return ContentComponentVersion{}, err
	}
	v.ManifestJSON = json.RawMessage(manifest)
	v.Signature = sig.String
	return v, nil
}

func insertContentVersion(ctx context.Context, tx *sql.Tx, v ContentComponentVersion) error {
	manifest := v.ManifestJSON
	if len(manifest) == 0 {
		manifest = json.RawMessage("{}")
	}
	var sig any
	if v.Signature != "" {
		sig = v.Signature
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO content_component_versions (`+contentVersionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ComponentID, v.Version, manifest, v.BundleBlobKey, v.SHA256, v.SizeBytes, sig, v.CreatedBy, v.CreatedAtMs)
	if isDupEntry(err) {
		return ErrContentVersionConflict
	}
	return err
}

func (s *MySQLContentComponents) CreateContentComponent(ctx context.Context, c ContentComponent, v ContentComponentVersion) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tenants, err := contentTenantsArg(c.EnabledTenants)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO content_components (`+contentComponentCols+`, org_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.Name, c.TenantID, c.Owner, string(c.Scope), string(c.Status), v.Version, c.DisplayName, c.Description, c.Category, c.SourceID, c.SharedBy, tenants, c.CreatedAtMs, c.UpdatedAtMs, contentOrgName(c))
	if isOrgNameClash(err) {
		return ErrContentOrgNameTaken
	}
	if isDupEntry(err) {
		return ErrContentExists
	}
	if err != nil {
		return err
	}
	v.ComponentID = c.ID
	if err := insertContentVersion(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLContentComponents) AddContentComponentVersion(ctx context.Context, c ContentComponent, prevLatest string, v ContentComponentVersion) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE content_components SET latest_version = ?, display_name = ?, description = ?, category = ?, updated_at_ms = ?
  WHERE id = ? AND latest_version = ?`, v.Version, c.DisplayName, c.Description, c.Category, c.UpdatedAtMs, c.ID, prevLatest)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM content_components WHERE id = ?`, c.ID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrContentNotFound
		}
		return ErrContentVersionConflict
	}
	v.ComponentID = c.ID
	if err := insertContentVersion(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLContentComponents) GetContentComponent(ctx context.Context, id string) (ContentComponent, error) {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	c, err := scanContentComponent(s.DB.QueryRowContext(ctx, `SELECT `+contentComponentCols+` FROM content_components WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ContentComponent{}, ErrContentNotFound
	}
	return c, err
}

func (s *MySQLContentComponents) ListContentComponents(ctx context.Context, q ContentComponentQuery) ([]ContentComponent, error) {
	var where []string
	var args []any
	if q.TenantID != "" {
		where, args = append(where, "tenant_id = ?"), append(args, q.TenantID)
	}
	if q.Owner != "" {
		where, args = append(where, "owner = ?"), append(args, q.Owner)
	}
	if q.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, q.Kind)
	}
	if len(q.Scopes) > 0 {
		where = append(where, "scope IN ("+inPlaceholders(len(q.Scopes))+")")
		for _, sc := range q.Scopes {
			args = append(args, string(sc))
		}
	}
	if len(q.IDs) > 0 {
		where = append(where, "id IN ("+inPlaceholders(len(q.IDs))+")")
		for _, id := range q.IDs {
			args = append(args, id)
		}
	}
	if q.SourceID != "" {
		where, args = append(where, "source_id = ?"), append(args, q.SourceID)
	}
	if q.Name != "" {
		where, args = append(where, "name = ?"), append(args, q.Name)
	}
	query := `SELECT ` + contentComponentCols + ` FROM content_components`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY updated_at_ms DESC, id LIMIT ?"
	return queryAll(ctx, s.DB, query, scanContentComponent, append(args, contentLimit(q.Limit))...)
}

func (s *MySQLContentComponents) ListContentComponentVersions(ctx context.Context, componentID string) ([]ContentComponentVersion, error) {
	return queryAll(ctx, s.DB, `SELECT `+contentVersionCols+` FROM content_component_versions WHERE component_id = ? ORDER BY created_at_ms DESC, version DESC LIMIT ?`, scanContentVersion, componentID, MaxContentList)
}

func (s *MySQLContentComponents) GetContentComponentVersion(ctx context.Context, componentID, version string) (ContentComponentVersion, error) {
	v, err := scanContentVersion(s.DB.QueryRowContext(ctx, `SELECT `+contentVersionCols+` FROM content_component_versions WHERE component_id = ? AND version = ?`, componentID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return ContentComponentVersion{}, ErrContentNotFound
	}
	return v, err
}

// ContentComponentsByBlob is one query: the versions bundling the key
// (idx_content_versions_blob) semi-joined to the components in reach (see
// ContentBlobQuery.reaches, its memory form).
func (s *MySQLContentComponents) ContentComponentsByBlob(ctx context.Context, q ContentBlobQuery) ([]ContentComponent, error) {
	// cross-tenant: platform content spans tenants; the reach names the tenant.
	return queryAll(ctx, s.DB, `SELECT `+contentComponentCols+` FROM content_components
  WHERE id IN (SELECT component_id FROM content_component_versions WHERE bundle_blob_key = ?)
    AND (scope = ?
      OR (tenant_id = ? AND (scope = ? OR (? <> '' AND owner = ?)))
      OR (? <> '' AND id IN (SELECT component_id FROM content_component_uses WHERE scope_kind = ? AND scope_id = ?)))
  ORDER BY id LIMIT ?`, scanContentComponent,
		q.Key, string(ContentScopePlatform), q.Tenant, string(ContentScopeOrg), q.Subject, q.Subject,
		q.RoomID, string(ContentUseRoom), q.RoomID, MaxContentList)
}

func (s *MySQLContentComponents) DeleteContentComponent(ctx context.Context, id string) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM content_publish_requests WHERE component_id = ?`,
		`DELETE FROM content_room_shares WHERE component_id = ?`,
		`DELETE FROM content_prompt_uses WHERE component_id = ?`,
		`DELETE FROM content_favorites WHERE component_id = ?`,
		`DELETE FROM content_version_covers WHERE component_id = ?`,
		`DELETE FROM content_permission_acks WHERE component_id = ?`,
		`DELETE FROM content_component_uses WHERE component_id = ?`,
		`DELETE FROM content_component_versions WHERE component_id = ?`,
		`DELETE FROM content_components WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *MySQLContentComponents) SetContentComponentStatus(ctx context.Context, id string, status ContentStatus, atMs int64) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	// Activating an organization row claims its name: a row that predates
	// the name key may hold none (2026_10_content_org_names kept the
	// oldest of any duplicates).
	res, err := s.DB.ExecContext(ctx, `UPDATE content_components SET status = ?, updated_at_ms = ?,
  org_name = IF(scope = 'org' AND ? = 'active', name, org_name) WHERE id = ?`, string(status), atMs, string(status), id)
	if isOrgNameClash(err) {
		return ErrContentOrgNameTaken
	}
	if err != nil {
		return err
	}
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) SetContentComponentTenants(ctx context.Context, id string, tenants []string, atMs int64) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	arg, err := contentTenantsArg(tenants)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE content_components SET enabled_tenants = ?, updated_at_ms = ? WHERE id = ?`, arg, atMs, id)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) PutContentUse(ctx context.Context, u ContentUse) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO content_component_uses
  (scope_kind, scope_id, component_id, tenant_id, kind, pinned_version, created_by, created_at_ms)
  SELECT ?, ?, id, ?, ?, ?, ?, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE pinned_version = VALUES(pinned_version)`,
		string(u.ScopeKind), u.ScopeID, u.TenantID, u.Kind, u.PinnedVersion, u.CreatedBy, u.CreatedAtMs, u.ComponentID)
	if err != nil {
		return err
	}
	// The INSERT ... SELECT matches nothing only when the component is gone
	// (an unchanged duplicate still counts: clientFoundRows).
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) DeleteContentUse(ctx context.Context, scopeKind ContentUseScope, scopeID, componentID string) error {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	_, err := s.DB.ExecContext(ctx, `DELETE FROM content_component_uses WHERE scope_kind = ? AND scope_id = ? AND component_id = ?`, string(scopeKind), scopeID, componentID)
	return err
}

func (s *MySQLContentComponents) ListContentUses(ctx context.Context, scopeKind ContentUseScope, scopeID string) ([]ContentUse, error) {
	return queryAll(ctx, s.DB, `SELECT scope_kind, scope_id, component_id, tenant_id, kind, pinned_version, created_by, created_at_ms
  FROM content_component_uses WHERE scope_kind = ? AND scope_id = ? ORDER BY created_at_ms, component_id LIMIT ?`, func(r rowScanner) (ContentUse, error) {
		var u ContentUse
		var sk string
		err := r.Scan(&sk, &u.ScopeID, &u.ComponentID, &u.TenantID, &u.Kind, &u.PinnedVersion, &u.CreatedBy, &u.CreatedAtMs)
		u.ScopeKind = ContentUseScope(sk)
		return u, err
	}, string(scopeKind), scopeID, MaxContentList)
}

// CountContentUses counts tenant's uses on idx_content_uses_component,
// bounded per component at MaxUsageCount (a badge count, like
// CountMemberUses).
func (s *MySQLContentComponents) CountContentUses(ctx context.Context, tenantID string, componentIDs []string) (map[string]int, error) {
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
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM content_component_uses WHERE component_id = ? AND tenant_id = ? AND scope_kind <> ? LIMIT ?) u`,
			id, tenantID, string(ContentUseRoomExpert), MaxUsageCount).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

func (s *MySQLContentComponents) PutContentAck(ctx context.Context, a ContentAck) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO content_permission_acks (subject, component_id, version, acked_at_ms) VALUES (?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE acked_at_ms = acked_at_ms`, a.Subject, a.ComponentID, a.Version, a.AckedAtMs)
	return err
}

func (s *MySQLContentComponents) ListContentAcks(ctx context.Context, subject string, componentIDs []string) ([]ContentAck, error) {
	out := []ContentAck{}
	if len(componentIDs) == 0 {
		return out, nil
	}
	args := []any{subject}
	for _, id := range componentIDs {
		args = append(args, id)
	}
	return queryAll(ctx, s.DB, `SELECT subject, component_id, version, acked_at_ms FROM content_permission_acks
  WHERE subject = ? AND component_id IN (`+inPlaceholders(len(componentIDs))+`) ORDER BY component_id, version`, func(r rowScanner) (ContentAck, error) {
		var a ContentAck
		err := r.Scan(&a.Subject, &a.ComponentID, &a.Version, &a.AckedAtMs)
		return a, err
	}, args...)
}
