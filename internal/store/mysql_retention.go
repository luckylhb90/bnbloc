package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MySQLRetention implements RetentionStore over database/sql. Every delete
// walks an index on (tenant_id, <age column>) in bounded batches, so a purge
// is a series of short statements that never scan a table:
//
//   - room_messages: idx_room_messages_retention (tenant_id, kind,
//     created_at_ms), one range per kind (the fence kind is skipped by
//     equality, so redacted rows are never re-read); the room max per batch
//     and the deletes go by primary key;
//   - agent_messages: idx_agent_messages_tenant_time;
//   - attachments: idx_attachments_created;
//   - artifact_versions: idx_artifact_versions_created, walked with a
//     keyset cursor so the latest versions it skips are read once a pass;
//   - shared_agent_audit / extension_audit: idx_audit_tenant_time /
//     idx_extension_audit_tenant;
//   - agent_jobs: idx_agent_jobs_tenant_created.
type MySQLRetention struct{ DB *sql.DB }

// NewMySQLRetention constructs the MySQL retention store.
func NewMySQLRetention(db *sql.DB) *MySQLRetention { return &MySQLRetention{DB: db} }

var _ RetentionStore = (*MySQLRetention)(nil)

// retentionLockName is the advisory lock one replica holds for a retention
// pass (the work is safe concurrently; the lock only avoids doing it twice).
const retentionLockName = "bnbloc_retention"

// TryRetentionLock takes the advisory lock on a pinned connection (GET_LOCK
// is owned by the session); release frees it and returns the connection.
func (s *MySQLRetention) TryRetentionLock(ctx context.Context) (func(), bool, error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", retentionLockName).Scan(&got); err != nil {
		conn.Close()
		return nil, false, err
	}
	if !got.Valid || got.Int64 != 1 {
		conn.Close()
		return nil, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", retentionLockName)
		conn.Close()
	}, true, nil
}

// Purge deletes one kind's expired rows in batches until none is left.
func (s *MySQLRetention) Purge(ctx context.Context, req PurgeRequest) (PurgeResult, error) {
	out := PurgeResult{}
	var err error
	switch req.Kind {
	case wire.RetentionMessages:
		err = s.purgeMessages(ctx, req, out)
	case wire.RetentionArtifactVersions:
		err = s.purgeArtifactVersions(ctx, req, out)
	case wire.RetentionAudit:
		if err = s.deleteBatches(ctx, out, PurgedBorrowAudit, `DELETE FROM shared_agent_audit WHERE tenant_id = ? AND at_ms < ? LIMIT ?`, req.TenantID, req.BeforeMs, req.batch()); err == nil {
			err = s.deleteBatches(ctx, out, PurgedExtensionAudit, `DELETE FROM extension_audit WHERE tenant_id = ? AND at_ms < ? LIMIT ?`, req.TenantID, req.BeforeMs, req.batch())
		}
	case wire.RetentionAgentJobs:
		// finished_at_ms >= created_at_ms, so the created range bounds the
		// scan; a terminal job without a finish stamp counts by creation.
		err = s.deleteBatches(ctx, out, PurgedAgentJobs, `DELETE FROM agent_jobs
WHERE tenant_id = ? AND created_at_ms < ? AND finished_at_ms < ?
  AND status IN ('replied','failed','timed_out','cancelled') LIMIT ?`, req.TenantID, req.BeforeMs, req.BeforeMs, req.batch())
	default:
		err = fmt.Errorf("retention: unknown kind %q", req.Kind)
	}
	return out, err
}

// deleteBatches runs a single-table DELETE ... LIMIT ? (the batch size is
// the last argument) until it deletes fewer rows than the batch.
func (s *MySQLRetention) deleteBatches(ctx context.Context, out PurgeResult, item, q string, args ...any) error {
	batch := int64(args[len(args)-1].(int))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := s.DB.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("purge %s: %w", item, err)
		}
		n, _ := res.RowsAffected()
		out[item] += n
		if n < batch {
			return nil
		}
	}
}

type roomSeq struct {
	room string
	seq  int64
}

// purgeMessages deletes expired room messages (fencing each room's max-seq
// row), their tasks and artifact source links, then fences the rooms moved
// past, the activity copy of agent replies and the attachments.
func (s *MySQLRetention) purgeMessages(ctx context.Context, req PurgeRequest, out PurgeResult) error {
	// The tenant's kinds, one index dive each (a loose scan of
	// idx_room_messages_retention; SELECT DISTINCT would read every entry).
	var kinds []string
	for last := ""; ; {
		var k string
		err := s.DB.QueryRowContext(ctx, `SELECT kind FROM room_messages
WHERE tenant_id = ? AND kind > ? ORDER BY kind LIMIT 1`, req.TenantID, last).Scan(&k)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("purge room_messages: kinds: %w", err)
		}
		if k != RetentionFenceKind {
			kinds = append(kinds, k)
		}
		last = k
	}
	for _, kind := range kinds {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := s.purgeMessageBatch(ctx, req, kind, out)
			if err != nil {
				return err
			}
			if n < req.batch() {
				break
			}
		}
	}
	// Fences the room moved past are plain garbage now.
	if err := s.deleteBatches(ctx, out, PurgedRoomMessages, `DELETE f FROM room_messages f
JOIN (SELECT c.room_id, c.seq FROM room_messages c
      WHERE c.tenant_id = ? AND c.kind = ?
        AND EXISTS (SELECT 1 FROM room_messages n FORCE INDEX (PRIMARY) WHERE n.room_id = c.room_id AND n.seq > c.seq)
      LIMIT ?) d ON d.room_id = f.room_id AND d.seq = f.seq`, req.TenantID, RetentionFenceKind, req.batch()); err != nil {
		return err
	}
	// The activity copy of agent replies. Submit-provenance rows carry no
	// creation time (0) and no content; they are kept.
	if err := s.deleteBatches(ctx, out, PurgedAgentMessages, `DELETE FROM agent_messages
WHERE tenant_id = ? AND created_at_ms > 0 AND created_at_ms < ? LIMIT ?`, req.TenantID, req.BeforeMs, req.batch()); err != nil {
		return err
	}
	// Attachments by upload age: the message that carried one is created
	// right after the upload, so both leave together.
	return s.deleteBatches(ctx, out, PurgedAttachments, `DELETE FROM attachments
WHERE tenant_id = ? AND created_at_ms < ? LIMIT ?`, req.TenantID, req.BeforeMs, req.batch())
}

// purgeMessageBatch handles one batch of expired rows of one kind and
// returns how many candidates it read.
func (s *MySQLRetention) purgeMessageBatch(ctx context.Context, req PurgeRequest, kind string, out PurgeResult) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT room_id, seq FROM room_messages
WHERE tenant_id = ? AND kind = ? AND created_at_ms < ?
ORDER BY created_at_ms LIMIT ?`, req.TenantID, kind, req.BeforeMs, req.batch())
	if err != nil {
		return 0, fmt.Errorf("purge room_messages: select: %w", err)
	}
	var cands []roomSeq
	rooms := map[string]bool{}
	for rows.Next() {
		var c roomSeq
		if err := rows.Scan(&c.room, &c.seq); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
		rooms[c.room] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(cands) == 0 {
		return 0, err
	}
	// The max only grows, so a row below it now stays below it; a row that
	// is the max now is fenced (if the room moves on meanwhile, a later pass
	// deletes the fence).
	roomList := sortedSet(rooms)
	maxSeq := make(map[string]int64, len(roomList))
	args := make([]any, 0, len(roomList)+1)
	args = append(args, req.TenantID)
	for _, r := range roomList {
		args = append(args, r)
	}
	mrows, err := s.DB.QueryContext(ctx, `SELECT room_id, MAX(seq) FROM room_messages
WHERE tenant_id = ? AND room_id IN (`+inPlaceholders(len(roomList))+`) GROUP BY room_id`, args...)
	if err != nil {
		return 0, fmt.Errorf("purge room_messages: max seq: %w", err)
	}
	for mrows.Next() {
		var r string
		var m int64
		if err := mrows.Scan(&r, &m); err != nil {
			mrows.Close()
			return 0, err
		}
		maxSeq[r] = m
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return 0, err
	}
	var del, fence []roomSeq
	for _, c := range cands {
		if c.seq >= maxSeq[c.room] {
			fence = append(fence, c)
		} else {
			del = append(del, c)
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	gone := append(append([]roomSeq{}, del...), fence...)
	n, err := execRoomSeqs(ctx, tx, `DELETE FROM message_tasks WHERE tenant_id = ? AND (room_id, seq) IN (%s)`, req.TenantID, gone)
	if err != nil {
		return 0, fmt.Errorf("purge message_tasks: %w", err)
	}
	out[PurgedMessageTasks] += n
	// Artifact versions anchored to a gone message lose the anchor.
	byRoom := map[string][]any{}
	for _, g := range gone {
		byRoom[g.room] = append(byRoom[g.room], g.seq)
	}
	for _, room := range sortedRoomKeys(byRoom) {
		seqs := byRoom[room]
		args := append([]any{req.TenantID, room}, seqs...)
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions v
JOIN artifacts a ON a.tenant_id = v.tenant_id AND a.artifact_id = v.artifact_id
SET v.source_seq = 0
WHERE v.tenant_id = ? AND a.room_id = ? AND v.source_seq IN (`+inPlaceholders(len(seqs))+`)`, args...); err != nil {
			return 0, fmt.Errorf("purge artifact source links: %w", err)
		}
	}
	if n, err = execRoomSeqs(ctx, tx, `DELETE FROM room_messages WHERE tenant_id = ? AND (room_id, seq) IN (%s)`, req.TenantID, del); err != nil {
		return 0, fmt.Errorf("purge room_messages: %w", err)
	}
	out[PurgedRoomMessages] += n
	for _, f := range fence {
		res, err := tx.ExecContext(ctx, `UPDATE room_messages
SET kind = ?, author = NULL, client_msg_id = NULL, payload_json = '{}', search_text = NULL,
    edited_at_ms = NULL, deleted_at_ms = COALESCE(deleted_at_ms, ?)
WHERE tenant_id = ? AND room_id = ? AND seq = ? AND kind <> ?`, RetentionFenceKind, req.NowMs, req.TenantID, f.room, f.seq, RetentionFenceKind)
		if err != nil {
			return 0, fmt.Errorf("purge room_messages: fence: %w", err)
		}
		fenced, _ := res.RowsAffected()
		out[PurgedRoomMessagesKept] += fenced
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(cands), nil
}

// execRoomSeqs runs q with its %s replaced by one (?, ?) per row, after the
// tenant argument.
func execRoomSeqs(ctx context.Context, tx *sql.Tx, q, tenantID string, rows []roomSeq) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	args := make([]any, 0, 1+2*len(rows))
	args = append(args, tenantID)
	for _, r := range rows {
		args = append(args, r.room, r.seq)
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(q, tuplePlaceholders(len(rows), 2)), args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// tuplePlaceholders renders n row constructors of width placeholders.
func tuplePlaceholders(n, width int) string {
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", width), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(one+",", n), ",")
}

func sortedRoomKeys(m map[string][]any) []string {
	set := make(map[string]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return sortedSet(set)
}

// purgeArtifactVersions deletes non-latest versions older than the cutoff
// and the share links pinned to them, walking the age index with a keyset
// cursor (latest versions it skips are read once per pass).
func (s *MySQLRetention) purgeArtifactVersions(ctx context.Context, req PurgeRequest, out PurgeResult) error {
	type cand struct {
		artifact, room string
		version        int
		created        int64
	}
	var after *cand
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := `SELECT v.artifact_id, a.room_id, v.version, v.created_at_ms
FROM artifact_versions v
JOIN artifacts a ON a.tenant_id = v.tenant_id AND a.artifact_id = v.artifact_id
WHERE v.tenant_id = ? AND v.created_at_ms < ? AND v.version < a.current_version`
		args := []any{req.TenantID, req.BeforeMs}
		if after != nil {
			// Spelled out (not a row comparison) so MySQL range-scans it.
			q += ` AND (v.created_at_ms > ? OR (v.created_at_ms = ? AND (v.artifact_id > ? OR (v.artifact_id = ? AND v.version > ?))))`
			args = append(args, after.created, after.created, after.artifact, after.artifact, after.version)
		}
		q += ` ORDER BY v.created_at_ms, v.artifact_id, v.version LIMIT ?`
		args = append(args, req.batch())
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("purge artifact_versions: select: %w", err)
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.artifact, &c.room, &c.version, &c.created); err != nil {
				rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(cands) == 0 {
			return nil
		}
		shareArgs := []any{req.TenantID}
		verArgs := []any{req.TenantID}
		for _, c := range cands {
			shareArgs = append(shareArgs, c.room, c.artifact, c.version)
			verArgs = append(verArgs, c.artifact, c.version)
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM artifact_shares WHERE tenant_id = ? AND (room_id, artifact_id, version) IN (`+tuplePlaceholders(len(cands), 3)+`)`, shareArgs...)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("purge artifact_shares: %w", err)
		}
		shares, _ := res.RowsAffected()
		res, err = tx.ExecContext(ctx, `DELETE FROM artifact_versions WHERE tenant_id = ? AND (artifact_id, version) IN (`+tuplePlaceholders(len(cands), 2)+`)`, verArgs...)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("purge artifact_versions: %w", err)
		}
		versions, _ := res.RowsAffected()
		if err := tx.Commit(); err != nil {
			return err
		}
		out[PurgedArtifactShares] += shares
		out[PurgedArtifactVersions] += versions
		if len(cands) < req.batch() {
			return nil
		}
		last := cands[len(cands)-1]
		after = &last
	}
}

// ReferencedBlobKeys checks keys against every blob-key column (indexed:
// idx_attachments_blob, idx_artifact_versions_blob,
// idx_artifact_versions_thumb) and the platform settings.
func (s *MySQLRetention) ReferencedBlobKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	platform, err := platformBlobKeys(ctx, NewMySQLPlatformSettings(s.DB))
	if err != nil {
		return nil, err
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
		if platform[strings.ToLower(k)] {
			out[k] = true
		}
	}
	in := inPlaceholders(len(keys))
	for _, q := range []string{
		`SELECT DISTINCT blob_key FROM attachments WHERE blob_key IN (` + in + `)`,
		`SELECT DISTINCT blob_key FROM artifact_versions WHERE blob_key IN (` + in + `)`,
		`SELECT DISTINCT thumb_blob_key FROM artifact_versions WHERE thumb_blob_key IN (` + in + `)`,
	} {
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("blob references: %w", err)
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
