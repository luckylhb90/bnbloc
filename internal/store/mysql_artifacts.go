package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLArtifacts is the MySQL-backed ArtifactStore.
type MySQLArtifacts struct{ DB *sql.DB }

func NewMySQLArtifacts(db *sql.DB) *MySQLArtifacts { return &MySQLArtifacts{DB: db} }

var _ ArtifactStore = (*MySQLArtifacts)(nil)

const artifactCols = `artifact_id, tenant_id, room_id, channel_id, kind, title, language, mime_type, file_name, current_version, created_by, created_at_ms, updated_at_ms`

// artifactVersionCols selects a version row; {content} is replaced by the
// content expression (the column, or an empty string literal for
// metadata-only listings).
const artifactVersionCols = `artifact_id, tenant_id, version, parent_version, content_hash, {content}, storage, blob_key, size_bytes, mime_type, source_job_id, source_seq, summary, author_user_id, created_at_ms, thumb_blob_key, thumb_mime_type, thumb_width, thumb_height, thumb_rank`

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
	if _, err := m.DB.ExecContext(ctx, `
INSERT INTO artifacts (artifact_id, tenant_id, room_id, channel_id, kind, title, language, mime_type, file_name, current_version, created_by, created_at_ms, updated_at_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE title=VALUES(title), updated_at_ms=VALUES(updated_at_ms)`,
		a.ArtifactID, tid, a.RoomID, a.ChannelID, a.Kind, a.Title, a.Language, a.MimeType, a.FileName, a.CurrentVersion, a.CreatedBy, a.CreatedAtMs, a.UpdatedAtMs); err != nil {
		return Artifact{}, err
	}
	// Return the stored row: a re-put (bridge attach) must see the existing
	// version history, not the caller's initial copy.
	return m.GetArtifact(ctx, a.ArtifactID)
}

func (m *MySQLArtifacts) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, err
	}
	return scanArtifact(m.DB.QueryRowContext(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE tenant_id=? AND artifact_id=?`, tid, artifactID))
}

func (m *MySQLArtifacts) ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	sqlq := `SELECT ` + artifactCols + ` FROM artifacts a WHERE tenant_id=? AND room_id=?`
	args := []any{tid, q.RoomID}
	if q.ChannelID != "" {
		sqlq += ` AND channel_id=?`
		args = append(args, q.ChannelID)
	}
	if q.Kind != "" {
		sqlq += ` AND kind=?`
		args = append(args, q.Kind)
	}
	if q.MimeType != "" {
		if prefix, ok := strings.CutSuffix(q.MimeType, "/*"); ok {
			sqlq += ` AND mime_type LIKE ?`
			args = append(args, strings.TrimPrefix(likePattern(prefix+"/"), "%")) // "type/%": a prefix match
		} else {
			sqlq += ` AND mime_type=?`
			args = append(args, q.MimeType)
		}
	}
	if q.Author != "" {
		sqlq += ` AND created_by=?`
		args = append(args, q.Author)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		sqlq += ` AND (title LIKE ? OR file_name LIKE ?)`
		args = append(args, likePattern(t), likePattern(t))
	}
	if q.SourceSeq > 0 {
		sqlq += ` AND EXISTS (SELECT 1 FROM artifact_versions v WHERE v.tenant_id=a.tenant_id AND v.artifact_id=a.artifact_id AND v.source_seq=?)`
		args = append(args, q.SourceSeq)
	}
	if q.BeforeUpdatedMs > 0 {
		sqlq += ` AND (updated_at_ms < ? OR (updated_at_ms = ? AND artifact_id < ?))`
		args = append(args, q.BeforeUpdatedMs, q.BeforeUpdatedMs, q.BeforeID)
	}
	sqlq += ` ORDER BY updated_at_ms DESC, artifact_id DESC LIMIT ?`
	args = append(args, artifactListLimit(q.Limit))
	rows, err := m.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
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
INSERT INTO artifact_versions (tenant_id, artifact_id, version, parent_version, content_hash, content, storage, blob_key, size_bytes, mime_type, source_job_id, source_seq, summary, author_user_id, created_at_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		tid, artifactID, v.Version, v.ParentVersion, v.ContentHash, v.Content, v.Storage, v.BlobKey, v.SizeBytes, v.MimeType, v.SourceJobID, v.SourceSeq, v.Summary, v.AuthorUserID, v.CreatedAtMs); err != nil {
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

func (m *MySQLArtifacts) ListArtifactVersions(ctx context.Context, artifactID string, limit int) []ArtifactVersion {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := m.DB.QueryContext(ctx, `
SELECT `+versionCols(false)+`
FROM artifact_versions WHERE tenant_id=? AND artifact_id=?
ORDER BY version DESC LIMIT ?`, tid, artifactID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ArtifactVersion
	for rows.Next() {
		if v, err := scanArtifactVersion(rows); err == nil {
			out = append(out, v)
		}
	}
	return out
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

type artifactScanner interface{ Scan(dest ...any) error }

func scanArtifact(row artifactScanner) (Artifact, error) {
	var a Artifact
	err := row.Scan(&a.ArtifactID, &a.TenantID, &a.RoomID, &a.ChannelID, &a.Kind, &a.Title, &a.Language, &a.MimeType, &a.FileName, &a.CurrentVersion, &a.CreatedBy, &a.CreatedAtMs, &a.UpdatedAtMs)
	if err == sql.ErrNoRows {
		return Artifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func scanArtifactVersion(row artifactScanner) (ArtifactVersion, error) {
	var v ArtifactVersion
	var thumbMime string
	var thumbW, thumbH int
	err := row.Scan(&v.ArtifactID, &v.TenantID, &v.Version, &v.ParentVersion, &v.ContentHash, &v.Content,
		&v.Storage, &v.BlobKey, &v.SizeBytes, &v.MimeType, &v.SourceJobID, &v.SourceSeq,
		&v.Summary, &v.AuthorUserID, &v.CreatedAtMs,
		&v.ThumbnailBlobKey, &thumbMime, &thumbW, &thumbH, &v.ThumbnailRank)
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
