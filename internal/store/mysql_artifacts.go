package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLArtifacts is the MySQL-backed ArtifactStore.
type MySQLArtifacts struct{ DB *sql.DB }

func NewMySQLArtifacts(db *sql.DB) *MySQLArtifacts { return &MySQLArtifacts{DB: db} }

var _ ArtifactStore = (*MySQLArtifacts)(nil)

// artifactCols selects an artifacts row aliased a, with the two app facts
// Artifact.IsApp decides on (artifact_apps.go).
const artifactCols = `a.artifact_id, a.tenant_id, a.room_id, a.channel_id, a.kind, a.title, a.language, a.mime_type, a.file_name, a.current_version, a.created_by, a.created_at_ms, a.updated_at_ms, ` +
	sqlArtifactLoadsSDK + `, ` + sqlArtifactHasAppData

// artifactVersionCols selects a version row; {content} is replaced by the
// content expression (the column, or an empty string literal for
// metadata-only listings).
const artifactVersionCols = `artifact_id, tenant_id, version, parent_version, content_hash, {content}, storage, blob_key, size_bytes, mime_type, source_job_id, source_seq, summary, author_user_id, created_at_ms, thumb_blob_key, thumb_mime_type, thumb_width, thumb_height, thumb_rank, loads_app_sdk`

func versionCols(withContent bool) string {
	if withContent {
		return strings.Replace(artifactVersionCols, "{content}", "content", 1)
	}
	// Content excluded by design: a list of 50 revisions x 2MB would move
	// 100MB where the panel needs the timeline only.
	return strings.Replace(artifactVersionCols, "{content}", "''", 1)
}

func (m *MySQLArtifacts) PutArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, err
	}
	a.TenantID = tid
	if a.CreatedAtMs <= 0 {
		a.CreatedAtMs = time.Now().UnixMilli()
	}
	if a.UpdatedAtMs <= 0 {
		a.UpdatedAtMs = a.CreatedAtMs
	}
	// artifact_id alone is the key, so an existing row is never written: an
	// upsert would let a caller-chosen id rewrite another room's artifact.
	if _, err := m.DB.ExecContext(ctx, `
INSERT IGNORE INTO artifacts (artifact_id, tenant_id, room_id, channel_id, kind, title, language, mime_type, file_name, current_version, created_by, created_at_ms, updated_at_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ArtifactID, tid, a.RoomID, a.ChannelID, a.Kind, a.Title, a.Language, a.MimeType, a.FileName, a.CurrentVersion, a.CreatedBy, a.CreatedAtMs, a.UpdatedAtMs); err != nil {
		return Artifact{}, err
	}
	// Return the stored row: a re-put (bridge attach) must see the existing
	// version history, not the caller's initial copy. The read is
	// tenant-scoped, so a row held by another tenant reads as missing.
	stored, err := m.GetArtifact(ctx, a.ArtifactID)
	if errors.Is(err, ErrArtifactNotFound) || (err == nil && stored.RoomID != a.RoomID) {
		return Artifact{}, ErrArtifactIDTaken
	}
	return stored, err
}

func (m *MySQLArtifacts) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, err
	}
	return scanArtifact(m.DB.QueryRowContext(ctx, `SELECT `+artifactCols+` FROM artifacts a WHERE a.tenant_id=? AND a.artifact_id=?`, tid, artifactID))
}

func (m *MySQLArtifacts) ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	sqlq := `SELECT ` + artifactCols + ` FROM artifacts a WHERE a.tenant_id=? AND a.room_id=?`
	args := []any{tid, q.RoomID}
	if q.ChannelID != "" {
		sqlq += ` AND a.channel_id=?`
		args = append(args, q.ChannelID)
	}
	if q.Kind != "" {
		sqlq += ` AND a.kind=?`
		args = append(args, q.Kind)
	}
	if q.MimeType != "" {
		if prefix, ok := strings.CutSuffix(q.MimeType, "/*"); ok {
			sqlq += ` AND a.mime_type LIKE ?`
			args = append(args, strings.TrimPrefix(likePattern(prefix+"/"), "%")) // "type/%": a prefix match
		} else {
			sqlq += ` AND a.mime_type=?`
			args = append(args, q.MimeType)
		}
	}
	if q.Author != "" {
		sqlq += ` AND a.created_by=?`
		args = append(args, q.Author)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		sqlq += ` AND (a.title LIKE ? OR a.file_name LIKE ?)`
		args = append(args, likePattern(t), likePattern(t))
	}
	if q.SourceSeq > 0 {
		sqlq += ` AND EXISTS (SELECT 1 FROM artifact_versions v WHERE v.tenant_id=a.tenant_id AND v.artifact_id=a.artifact_id AND v.source_seq=?)`
		args = append(args, q.SourceSeq)
	}
	if q.Apps {
		sqlq += ` AND ` + sqlArtifactIsApp
	}
	if q.BeforeUpdatedMs > 0 {
		sqlq += ` AND (a.updated_at_ms < ? OR (a.updated_at_ms = ? AND a.artifact_id < ?))`
		args = append(args, q.BeforeUpdatedMs, q.BeforeUpdatedMs, q.BeforeID)
	}
	sqlq += ` ORDER BY a.updated_at_ms DESC, a.artifact_id DESC LIMIT ?`
	return queryAll(ctx, m.DB, sqlq, scanArtifact, append(args, artifactListLimit(q.Limit))...)
}

// ArtifactAnchors returns the room's version links in a seq range (see
// ArtifactStore). It starts from the room's artifacts (idx_artifacts_room)
// and reads their versions by primary key, so other rooms' versions in the
// same seq range are never scanned.
func (m *MySQLArtifacts) ArtifactAnchors(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]ArtifactAnchor, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT DISTINCT v.source_seq, v.artifact_id
FROM artifacts a JOIN artifact_versions v ON v.tenant_id=a.tenant_id AND v.artifact_id=a.artifact_id
WHERE a.tenant_id=? AND a.room_id=? AND v.source_seq BETWEEN ? AND ? AND v.source_seq > 0
ORDER BY v.source_seq, v.artifact_id LIMIT ?`, tid, roomID, fromSeq, toSeq, MaxArtifactAnchors)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactAnchor{}
	for rows.Next() {
		var a ArtifactAnchor
		if err := rows.Scan(&a.Seq, &a.ArtifactID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PublishArtifactVersion appends one revision under compare-and-swap on the
// artifact's current version. The UPDATE's WHERE enforces the CAS: a racing
// publisher that already advanced the version makes this a no-row change and
// the caller is told the artifact moved on. Versions themselves are immutable
// rows keyed (tenant, artifact, version), so history cannot be rewritten.
func (m *MySQLArtifacts) PublishArtifactVersion(ctx context.Context, artifactID string, v ArtifactVersion, baseVersion int) (Artifact, ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	v, err = prepareArtifactVersion(v)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if v.CreatedAtMs <= 0 {
		v.CreatedAtMs = time.Now().UnixMilli()
	}
	v.ArtifactID = artifactID
	v.TenantID = tid
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// A typed version moves the artifact's MIME type with it; an untyped one
	// (legacy inline publish) leaves it as it was.
	res, err := tx.ExecContext(ctx, `
UPDATE artifacts SET current_version = current_version + 1, updated_at_ms = ?,
  mime_type = IF(? = '', mime_type, ?)
WHERE tenant_id=? AND artifact_id=? AND current_version = ?`,
		v.CreatedAtMs, v.MimeType, v.MimeType, tid, artifactID, baseVersion)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		a, err := m.GetArtifact(ctx, artifactID)
		if err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		return a, ArtifactVersion{}, ErrArtifactVersionConflict
	}
	v.Version = baseVersion + 1
	v.ParentVersion = baseVersion
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_versions (tenant_id, artifact_id, version, parent_version, content_hash, content, storage, blob_key, size_bytes, mime_type, source_job_id, source_seq, summary, author_user_id, created_at_ms, loads_app_sdk)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		tid, artifactID, v.Version, v.ParentVersion, v.ContentHash, v.Content, v.Storage, v.BlobKey, v.SizeBytes, v.MimeType, v.SourceJobID, v.SourceSeq, v.Summary, v.AuthorUserID, v.CreatedAtMs, v.LoadsAppSDK); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	a, err := m.GetArtifact(ctx, artifactID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	return a, v, nil
}

func (m *MySQLArtifacts) GetArtifactVersion(ctx context.Context, artifactID string, version int) (ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactVersion{}, err
	}
	return scanArtifactVersion(m.DB.QueryRowContext(ctx, `
SELECT `+versionCols(true)+`
FROM artifact_versions WHERE tenant_id=? AND artifact_id=? AND version=?`, tid, artifactID, version))
}

func (m *MySQLArtifacts) ListArtifactVersions(ctx context.Context, artifactID string, limit int) ([]ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, m.DB, `
SELECT `+versionCols(false)+`
FROM artifact_versions WHERE tenant_id=? AND artifact_id=?
ORDER BY version DESC LIMIT ?`, scanArtifactVersion, tid, artifactID, artifactVersionLimit(limit))
}

// SetArtifactThumbnail is one conditional UPDATE: it lands only when no
// thumbnail is stored or the stored one ranks lower, so concurrent writers
// settle on the highest-priority (then first) thumbnail without a read race.
func (m *MySQLArtifacts) SetArtifactThumbnail(ctx context.Context, artifactID string, version int, t ArtifactThumbnailWrite) (ArtifactVersion, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactVersion{}, false, err
	}
	res, err := m.DB.ExecContext(ctx, `
UPDATE artifact_versions SET thumb_blob_key=?, thumb_mime_type=?, thumb_width=?, thumb_height=?, thumb_rank=?
WHERE tenant_id=? AND artifact_id=? AND version=? AND (thumb_blob_key='' OR thumb_rank < ?)`,
		t.BlobKey, t.MimeType, t.Width, t.Height, t.Rank, tid, artifactID, version, t.Rank)
	if err != nil {
		return ArtifactVersion{}, false, err
	}
	n, _ := res.RowsAffected()
	v, err := m.GetArtifactVersion(ctx, artifactID, version)
	if err != nil {
		return ArtifactVersion{}, false, err
	}
	return v, n > 0, nil
}

func scanArtifact(row rowScanner) (Artifact, error) {
	var a Artifact
	err := row.Scan(&a.ArtifactID, &a.TenantID, &a.RoomID, &a.ChannelID, &a.Kind, &a.Title, &a.Language, &a.MimeType, &a.FileName, &a.CurrentVersion, &a.CreatedBy, &a.CreatedAtMs, &a.UpdatedAtMs,
		&a.LoadsAppSDK, &a.HasAppData)
	if err == sql.ErrNoRows {
		return Artifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func scanArtifactVersion(row rowScanner) (ArtifactVersion, error) {
	var v ArtifactVersion
	var thumbMime string
	var thumbW, thumbH int
	err := row.Scan(&v.ArtifactID, &v.TenantID, &v.Version, &v.ParentVersion, &v.ContentHash, &v.Content,
		&v.Storage, &v.BlobKey, &v.SizeBytes, &v.MimeType, &v.SourceJobID, &v.SourceSeq,
		&v.Summary, &v.AuthorUserID, &v.CreatedAtMs,
		&v.ThumbnailBlobKey, &thumbMime, &thumbW, &thumbH, &v.ThumbnailRank, &v.LoadsAppSDK)
	if err == sql.ErrNoRows {
		return ArtifactVersion{}, ErrArtifactNotFound
	}
	if err != nil {
		return ArtifactVersion{}, err
	}
	if v.ThumbnailBlobKey != "" {
		v.Thumbnail = &ArtifactThumbnail{MimeType: thumbMime, Width: thumbW, Height: thumbH}
	}
	return v, nil
}
