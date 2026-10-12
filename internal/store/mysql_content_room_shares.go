package store

import (
	"context"
	"strings"
)

func (s *MySQLContentComponents) PutContentRoomShare(ctx context.Context, sh ContentRoomShare) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO content_room_shares
  (room_id, component_id, tenant_id, version, shared_by, note, created_at_ms, updated_at_ms)
  SELECT ?, id, ?, ?, ?, ?, ?, ? FROM content_components WHERE id = ?
  ON DUPLICATE KEY UPDATE version = VALUES(version), shared_by = VALUES(shared_by), note = VALUES(note), updated_at_ms = VALUES(updated_at_ms)`,
		sh.RoomID, sh.TenantID, sh.Version, sh.SharedBy, sh.Note, sh.CreatedAtMs, sh.UpdatedAtMs, sh.ComponentID)
	if err != nil {
		return err
	}
	// The INSERT ... SELECT inserts nothing only when the component is gone
	// (an unchanged duplicate still counts: clientFoundRows).
	return requireMatched(res, ErrContentNotFound)
}

func (s *MySQLContentComponents) ListContentRoomShares(ctx context.Context, q ContentRoomShareQuery) ([]ContentRoomShare, error) {
	var where []string
	var args []any
	if q.RoomID != "" {
		where, args = append(where, "room_id = ?"), append(args, q.RoomID)
	}
	if q.ComponentID != "" {
		where, args = append(where, "component_id = ?"), append(args, q.ComponentID)
	}
	query := `SELECT room_id, component_id, tenant_id, version, shared_by, note, created_at_ms, updated_at_ms FROM content_room_shares`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at_ms, room_id, component_id LIMIT ?"
	rows, err := s.DB.QueryContext(ctx, query, append(args, MaxContentList)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentRoomShare{}
	for rows.Next() {
		var sh ContentRoomShare
		if err := rows.Scan(&sh.RoomID, &sh.ComponentID, &sh.TenantID, &sh.Version, &sh.SharedBy, &sh.Note, &sh.CreatedAtMs, &sh.UpdatedAtMs); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *MySQLContentComponents) DeleteContentRoomShare(ctx context.Context, roomID, componentID string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM content_room_shares WHERE room_id = ? AND component_id = ?`, roomID, componentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
