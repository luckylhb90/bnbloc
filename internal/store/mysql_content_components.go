package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// MySQLContentComponents implements ContentComponentStore over database/sql.
type MySQLContentComponents struct{ DB *sql.DB }

// NewMySQLContentComponents constructs a MySQL-backed content store.
func NewMySQLContentComponents(db *sql.DB) *MySQLContentComponents {
	return &MySQLContentComponents{DB: db}
}

var _ ContentComponentStore = (*MySQLContentComponents)(nil)

const contentComponentCols = `id, kind, name, tenant_id, owner, scope, status, latest_version, display_name, description, category, created_at_ms, updated_at_ms`

func scanContentComponent(row interface{ Scan(...any) error }) (ContentComponent, error) {
	var c ContentComponent
	var scope, status string
	if err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.TenantID, &c.Owner, &scope, &status, &c.LatestVersion, &c.DisplayName, &c.Description, &c.Category, &c.CreatedAtMs, &c.UpdatedAtMs); err != nil {
		return ContentComponent{}, err
	}
	c.Scope, c.Status = ContentScope(scope), ContentStatus(status)
	return c, nil
}

const contentVersionCols = `component_id, version, manifest_json, bundle_blob_key, sha256, size_bytes, signature, created_by, created_at_ms`

func scanContentVersion(row interface{ Scan(...any) error }) (ContentComponentVersion, error) {
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

func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDupEntry
}

func (s *MySQLContentComponents) CreateContentComponent(ctx context.Context, c ContentComponent, v ContentComponentVersion) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO content_components (`+contentComponentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.Name, c.TenantID, c.Owner, string(c.Scope), string(c.Status), v.Version, c.DisplayName, c.Description, c.Category, c.CreatedAtMs, c.UpdatedAtMs)
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
	query := `SELECT ` + contentComponentCols + ` FROM content_components`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY updated_at_ms DESC, id LIMIT %d", contentLimit(q.Limit))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentComponent{}
	for rows.Next() {
		c, err := scanContentComponent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *MySQLContentComponents) queryVersions(ctx context.Context, query string, args ...any) ([]ContentComponentVersion, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentComponentVersion{}
	for rows.Next() {
		v, err := scanContentVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *MySQLContentComponents) ListContentComponentVersions(ctx context.Context, componentID string) ([]ContentComponentVersion, error) {
	return s.queryVersions(ctx, `SELECT `+contentVersionCols+` FROM content_component_versions WHERE component_id = ? ORDER BY created_at_ms DESC, version DESC LIMIT ?`, componentID, MaxContentList)
}

func (s *MySQLContentComponents) GetContentComponentVersion(ctx context.Context, componentID, version string) (ContentComponentVersion, error) {
	v, err := scanContentVersion(s.DB.QueryRowContext(ctx, `SELECT `+contentVersionCols+` FROM content_component_versions WHERE component_id = ? AND version = ?`, componentID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return ContentComponentVersion{}, ErrContentNotFound
	}
	return v, err
}

func (s *MySQLContentComponents) ContentVersionsByBlob(ctx context.Context, key string) ([]ContentComponentVersion, error) {
	return s.queryVersions(ctx, `SELECT `+contentVersionCols+` FROM content_component_versions WHERE bundle_blob_key = ? LIMIT ?`, key, MaxContentList)
}

func (s *MySQLContentComponents) DeleteContentComponent(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
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

func (s *MySQLContentComponents) PutContentUse(ctx context.Context, u ContentUse) error {
	if strings.HasPrefix(u.ComponentID, BuiltinContentIDPrefix) {
		// A built-in component has no row to select from (the overlay checked
		// that it exists).
		_, err := s.DB.ExecContext(ctx, `INSERT INTO content_component_uses
  (scope_kind, scope_id, component_id, tenant_id, kind, pinned_version, created_by, created_at_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE pinned_version = VALUES(pinned_version)`,
			string(u.ScopeKind), u.ScopeID, u.ComponentID, u.TenantID, u.Kind, u.PinnedVersion, u.CreatedBy, u.CreatedAtMs)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO content_component_uses
  (scope_kind, scope_id, component_id, tenant_id, kind, pinned_version, created_by, created_at_ms)
  SELECT ?, ?, id, ?, ?, ?, ?, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE pinned_version = VALUES(pinned_version)`,
		string(u.ScopeKind), u.ScopeID, u.TenantID, u.Kind, u.PinnedVersion, u.CreatedBy, u.CreatedAtMs, u.ComponentID)
	if err != nil {
		return err
	}
	var one int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM content_component_uses WHERE scope_kind = ? AND scope_id = ? AND component_id = ?`,
		string(u.ScopeKind), u.ScopeID, u.ComponentID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrContentNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (s *MySQLContentComponents) DeleteContentUse(ctx context.Context, scopeKind ContentUseScope, scopeID, componentID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM content_component_uses WHERE scope_kind = ? AND scope_id = ? AND component_id = ?`, string(scopeKind), scopeID, componentID)
	return err
}

func (s *MySQLContentComponents) ListContentUses(ctx context.Context, scopeKind ContentUseScope, scopeID string) ([]ContentUse, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT scope_kind, scope_id, component_id, tenant_id, kind, pinned_version, created_by, created_at_ms
  FROM content_component_uses WHERE scope_kind = ? AND scope_id = ? ORDER BY created_at_ms, component_id LIMIT ?`, string(scopeKind), scopeID, MaxContentList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentUse{}
	for rows.Next() {
		var u ContentUse
		var sk string
		if err := rows.Scan(&sk, &u.ScopeID, &u.ComponentID, &u.TenantID, &u.Kind, &u.PinnedVersion, &u.CreatedBy, &u.CreatedAtMs); err != nil {
			return nil, err
		}
		u.ScopeKind = ContentUseScope(sk)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *MySQLContentComponents) CountContentUses(ctx context.Context, componentIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(componentIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(componentIDs))
	for i, id := range componentIDs {
		args[i] = id
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT component_id, COUNT(*) FROM content_component_uses WHERE component_id IN (`+inPlaceholders(len(args))+`) AND scope_kind <> '`+string(ContentUseRoomExpert)+`' GROUP BY component_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT subject, component_id, version, acked_at_ms FROM content_permission_acks
  WHERE subject = ? AND component_id IN (`+inPlaceholders(len(componentIDs))+`) ORDER BY component_id, version`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a ContentAck
		if err := rows.Scan(&a.Subject, &a.ComponentID, &a.Version, &a.AckedAtMs); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
