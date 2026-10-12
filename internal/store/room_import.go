package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ErrRoomImportExists refuses a second provenance row for a room (room ids
// are global: the key carries no tenant, so the row is written once).
var ErrRoomImportExists = errors.New("store: room import provenance already recorded")

// RoomImportStore persists the provenance of rooms created from a handoff
// package import (wire.RoomHandoffInfo): which room the package came from, who
// exported and imported it, and the source member list offered as invite
// suggestions. It is written once, when the import creates the room.
type RoomImportStore interface {
	// PutRoomImport records a room's provenance in the ctx tenant;
	// ErrRoomImportExists when the room already has one (in any tenant).
	PutRoomImport(ctx context.Context, info wire.RoomHandoffInfo) error
	// GetRoomImport returns the room's provenance; found=false for a room that
	// was not created by an import.
	GetRoomImport(ctx context.Context, roomID string) (wire.RoomHandoffInfo, bool, error)
}

// MemoryRoomImports is the in-memory RoomImportStore (dev/tests).
type MemoryRoomImports struct {
	mu   sync.Mutex
	rows map[[2]string]wire.RoomHandoffInfo // (tenant, room)
}

// NewMemoryRoomImports constructs an empty in-memory provenance store.
func NewMemoryRoomImports() *MemoryRoomImports {
	return &MemoryRoomImports{rows: map[[2]string]wire.RoomHandoffInfo{}}
}

var _ RoomImportStore = (*MemoryRoomImports)(nil)

func (m *MemoryRoomImports) PutRoomImport(ctx context.Context, info wire.RoomHandoffInfo) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.rows {
		if k[1] == info.RoomID {
			return ErrRoomImportExists
		}
	}
	info.SuggestedMembers = append([]wire.RoomExportMember{}, info.SuggestedMembers...)
	m.rows[[2]string{tid, info.RoomID}] = info
	return nil
}

func (m *MemoryRoomImports) GetRoomImport(ctx context.Context, roomID string) (wire.RoomHandoffInfo, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomHandoffInfo{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.rows[[2]string{tid, roomID}]
	info.SuggestedMembers = append([]wire.RoomExportMember{}, info.SuggestedMembers...)
	return info, ok, nil
}

// MySQLRoomImports is the MySQL-backed RoomImportStore.
type MySQLRoomImports struct{ DB *sql.DB }

// NewMySQLRoomImports builds a MySQLRoomImports over a shared pool.
func NewMySQLRoomImports(db *sql.DB) *MySQLRoomImports { return &MySQLRoomImports{DB: db} }

var _ RoomImportStore = (*MySQLRoomImports)(nil)

func (s *MySQLRoomImports) PutRoomImport(ctx context.Context, info wire.RoomHandoffInfo) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	members := info.SuggestedMembers
	if members == nil {
		members = []wire.RoomExportMember{}
	}
	mj, err := jsonArg(members)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO room_imports (tenant_id, room_id, source_room_id, source_room_name, source_exported_at_ms,
		   source_exported_by, imported_by, imported_at_ms, suggested_members_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tid, info.RoomID, info.SourceRoomID, info.SourceRoomName, info.SourceExportedAtMs,
		info.SourceExportedBy, info.ImportedBy, info.ImportedAtMs, mj)
	return dupAs(err, ErrRoomImportExists)
}

func (s *MySQLRoomImports) GetRoomImport(ctx context.Context, roomID string) (wire.RoomHandoffInfo, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomHandoffInfo{}, false, err
	}
	var info wire.RoomHandoffInfo
	var mj []byte
	err = s.DB.QueryRowContext(ctx,
		`SELECT room_id, source_room_id, source_room_name, source_exported_at_ms, source_exported_by,
		   imported_by, imported_at_ms, suggested_members_json
		 FROM room_imports WHERE tenant_id = ? AND room_id = ?`, tid, roomID).
		Scan(&info.RoomID, &info.SourceRoomID, &info.SourceRoomName, &info.SourceExportedAtMs,
			&info.SourceExportedBy, &info.ImportedBy, &info.ImportedAtMs, &mj)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.RoomHandoffInfo{}, false, nil
	}
	if err != nil {
		return wire.RoomHandoffInfo{}, false, err
	}
	if err := json.Unmarshal(mj, &info.SuggestedMembers); err != nil {
		return wire.RoomHandoffInfo{}, false, err
	}
	return info, true, nil
}
