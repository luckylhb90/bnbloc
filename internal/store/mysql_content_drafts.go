package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *MySQLContentComponents) PutContentDraft(ctx context.Context, d ContentDraft) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO content_drafts
  (id, tenant_id, room_id, job_id, requester, author, kind, name, display_name, version, manifest_json, bundle_blob_key, sha256, size_bytes, created_at_ms, expires_at_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.TenantID, d.RoomID, d.JobID, d.Requester, d.Author, d.Kind, d.Name, d.DisplayName, d.Version, d.ManifestJSON,
		d.BundleBlobKey, d.SHA256, d.SizeBytes, d.CreatedAtMs, d.ExpiresAtMs)
	return err
}

func (s *MySQLContentComponents) GetContentDraft(ctx context.Context, id string) (ContentDraft, error) {
	var d ContentDraft
	var manifest []byte
	err := s.DB.QueryRowContext(ctx, `SELECT id, tenant_id, room_id, job_id, requester, author, kind, name, display_name, version, manifest_json,
  bundle_blob_key, sha256, size_bytes, created_at_ms, expires_at_ms FROM content_drafts WHERE id = ?`, id).Scan(
		&d.ID, &d.TenantID, &d.RoomID, &d.JobID, &d.Requester, &d.Author, &d.Kind, &d.Name, &d.DisplayName, &d.Version, &manifest,
		&d.BundleBlobKey, &d.SHA256, &d.SizeBytes, &d.CreatedAtMs, &d.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentDraft{}, ErrContentNotFound
	}
	if err != nil {
		return ContentDraft{}, err
	}
	d.ManifestJSON = manifest
	return d, nil
}
