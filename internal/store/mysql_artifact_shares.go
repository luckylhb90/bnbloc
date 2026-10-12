package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var _ ArtifactShareStore = (*MySQLArtifacts)(nil)

const artifactShareCols = `s.share_id, s.token_hash, s.tenant_id, s.room_id, s.artifact_id, s.version, s.created_by, s.created_at_ms, s.expires_at_ms, s.revoked_at_ms, s.revoked_by, s.view_count, s.last_viewed_at_ms`

// shareEndedAt is when a link stopped working: its revocation or expiry,
// whichever came first.
const shareEndedAt = `LEAST(s.expires_at_ms, COALESCE(s.revoked_at_ms, s.expires_at_ms))`

func (m *MySQLArtifacts) CreateArtifactShare(ctx context.Context, sh ArtifactShare) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	sh.TenantID = tid
	sh.RevokedAtMs, sh.RevokedBy, sh.ViewCount, sh.LastViewedAtMs = 0, "", 0, 0
	if _, err := m.DB.ExecContext(ctx, `
INSERT INTO artifact_shares (share_id, token_hash, tenant_id, room_id, artifact_id, version, created_by, created_at_ms, expires_at_ms)
VALUES (?,?,?,?,?,?,?,?,?)`,
		sh.ShareID, sh.TokenHash, tid, sh.RoomID, sh.ArtifactID, sh.Version, sh.CreatedBy, sh.CreatedAtMs, sh.ExpiresAtMs); err != nil {
		return ArtifactShare{}, err
	}
	return sh, nil
}

func (m *MySQLArtifacts) GetArtifactShare(ctx context.Context, shareID string) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	return scanArtifactShare(m.DB.QueryRowContext(ctx, `
SELECT `+artifactShareCols+` FROM artifact_shares s WHERE s.tenant_id=? AND s.share_id=?`, tid, shareID))
}

func (m *MySQLArtifacts) ListArtifactShares(ctx context.Context, roomID, artifactID string, endedAfterMs int64) ([]ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `
SELECT `+artifactShareCols+` FROM artifact_shares s
WHERE s.tenant_id=? AND s.room_id=? AND s.artifact_id=? AND `+shareEndedAt+` > ?
ORDER BY s.created_at_ms DESC, s.share_id DESC LIMIT ?`, tid, roomID, artifactID, endedAfterMs, maxArtifactShareList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactShare{}
	for rows.Next() {
		sh, err := scanArtifactShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (m *MySQLArtifacts) ListTenantArtifactShares(ctx context.Context, q TenantShareQuery) ([]TenantArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	where := `s.tenant_id=? AND ` + shareEndedAt + ` > ?`
	args := []any{tid, q.EndedAfterMs}
	if q.ActiveAtMs > 0 {
		where += ` AND s.revoked_at_ms IS NULL AND s.expires_at_ms > ?`
		args = append(args, q.ActiveAtMs)
	}
	args = append(args, tenantShareLimit(q.Limit))
	// The join drops links of artifacts that no longer exist (a deleted
	// room's artifacts are gone), which resolve 404 anyway.
	rows, err := m.DB.QueryContext(ctx, `
SELECT `+artifactShareCols+`, a.title FROM artifact_shares s
JOIN artifacts a ON a.tenant_id = s.tenant_id AND a.artifact_id = s.artifact_id
WHERE `+where+`
ORDER BY s.created_at_ms DESC, s.share_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TenantArtifactShare{}
	for rows.Next() {
		var t TenantArtifactShare
		if t.ArtifactShare, err = scanArtifactShare(rows, &t.ArtifactTitle); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (m *MySQLArtifacts) RevokeArtifactShare(ctx context.Context, shareID, by string, atMs int64) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	if _, err := m.DB.ExecContext(ctx, `
UPDATE artifact_shares SET revoked_at_ms=?, revoked_by=?
WHERE tenant_id=? AND share_id=? AND revoked_at_ms IS NULL`, atMs, by, tid, shareID); err != nil {
		return ArtifactShare{}, err
	}
	return m.GetArtifactShare(ctx, shareID)
}

func (m *MySQLArtifacts) ResolveArtifactShare(ctx context.Context, shareID string) (ArtifactShare, error) {
	return scanArtifactShare(m.DB.QueryRowContext(ctx, `
SELECT `+artifactShareCols+` FROM artifact_shares s WHERE s.share_id=?`, shareID))
}

func (m *MySQLArtifacts) CountArtifactShareView(ctx context.Context, shareID string, atMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `
UPDATE artifact_shares SET view_count = view_count + 1, last_viewed_at_ms = ?
WHERE tenant_id=? AND share_id=?`, atMs, tid, shareID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrArtifactShareNotFound
	}
	return nil
}

// scanArtifactShare reads artifactShareCols, then any extra columns.
func scanArtifactShare(row rowScanner, extra ...any) (ArtifactShare, error) {
	var sh ArtifactShare
	var revokedAt, lastViewed sql.NullInt64
	var revokedBy sql.NullString
	dest := append([]any{&sh.ShareID, &sh.TokenHash, &sh.TenantID, &sh.RoomID, &sh.ArtifactID, &sh.Version,
		&sh.CreatedBy, &sh.CreatedAtMs, &sh.ExpiresAtMs, &revokedAt, &revokedBy, &sh.ViewCount, &lastViewed}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactShare{}, ErrArtifactShareNotFound
		}
		return ArtifactShare{}, err
	}
	sh.RevokedAtMs, sh.RevokedBy, sh.LastViewedAtMs = revokedAt.Int64, revokedBy.String, lastViewed.Int64
	return sh, nil
}
