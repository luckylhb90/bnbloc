package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Room group chats (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md
// §5a): the group chats a room's members connected to the room, by name,
// each delivered through one notifier component at that notifier's own
// opaque address of the chat; and the one-time codes that connect them (a
// member asks for a code, the notifier's bot sees it posted in the chat and
// binds it). Both belong to the room and are deleted with it.

// RoomGroup is one connected group chat of a room.
type RoomGroup struct {
	TenantID    string
	RoomID      string
	Name        string
	Notifier    string
	Address     string
	ConnectedBy string
	CreatedAtMs int64
}

// RoomGroupCode is an open connect code: the name the group will have in
// the room, who asked, until when it binds.
type RoomGroupCode struct {
	Code        string
	TenantID    string
	RoomID      string
	Name        string
	CreatedBy   string
	ExpiresAtMs int64
}

// Room group errors.
var (
	ErrRoomGroupNotFound = errors.New("room group not found")
	// ErrRoomGroupCode: the code is unknown, spent or expired.
	ErrRoomGroupCode = errors.New("connect code unknown or expired")
)

// RoomGroupStore holds rooms' group chats and connect codes. The tenant is
// the context's for everything but BindRoomGroup (a notifier's call names
// no tenant: the code carries it).
type RoomGroupStore interface {
	ListRoomGroups(ctx context.Context, roomID string) ([]RoomGroup, error)
	GetRoomGroup(ctx context.Context, roomID, name string) (RoomGroup, error)
	DeleteRoomGroup(ctx context.Context, roomID, name string) error
	// PutRoomGroupCode stores a new code; OpenRoomGroupCodes counts the
	// room's unexpired ones at nowMs.
	PutRoomGroupCode(ctx context.Context, c RoomGroupCode) error
	OpenRoomGroupCodes(ctx context.Context, roomID string, nowMs int64) (int, error)
	// BindRoomGroup spends code (unexpired at nowMs) and connects its group
	// at address through notifier, replacing a group of the same name in
	// the room; it returns the group. ErrRoomGroupCode for a code that is
	// unknown, spent or expired. cross-tenant: the code names its room.
	BindRoomGroup(ctx context.Context, code, notifier, address string, nowMs int64) (RoomGroup, error)
}

var (
	_ RoomGroupStore = (*MemoryRoomGroups)(nil)
	_ RoomGroupStore = (*MySQLRoomGroups)(nil)
)

// --- memory ---

// MemoryRoomGroups is the in-memory RoomGroupStore.
type MemoryRoomGroups struct {
	mu     sync.Mutex
	groups map[[3]string]RoomGroup // tenant, room, name
	codes  map[string]RoomGroupCode
}

// NewMemoryRoomGroups builds an empty store.
func NewMemoryRoomGroups() *MemoryRoomGroups {
	return &MemoryRoomGroups{groups: map[[3]string]RoomGroup{}, codes: map[string]RoomGroupCode{}}
}

func (m *MemoryRoomGroups) ListRoomGroups(ctx context.Context, roomID string) ([]RoomGroup, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RoomGroup{}
	for k, g := range m.groups {
		if k[0] == tid && k[1] == roomID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryRoomGroups) GetRoomGroup(ctx context.Context, roomID, name string) (RoomGroup, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomGroup{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[[3]string{tid, roomID, name}]
	if !ok {
		return RoomGroup{}, ErrRoomGroupNotFound
	}
	return g, nil
}

func (m *MemoryRoomGroups) DeleteRoomGroup(ctx context.Context, roomID, name string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, name}
	if _, ok := m.groups[k]; !ok {
		return ErrRoomGroupNotFound
	}
	delete(m.groups, k)
	return nil
}

func (m *MemoryRoomGroups) PutRoomGroupCode(ctx context.Context, c RoomGroupCode) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c.TenantID = tid
	m.codes[c.Code] = c
	return nil
}

func (m *MemoryRoomGroups) OpenRoomGroupCodes(ctx context.Context, roomID string, nowMs int64) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, c := range m.codes {
		if !liveAt(c.ExpiresAtMs, nowMs) {
			delete(m.codes, k)
			continue
		}
		if c.TenantID == tid && c.RoomID == roomID {
			n++
		}
	}
	return n, nil
}

func (m *MemoryRoomGroups) BindRoomGroup(_ context.Context, code, notifier, address string, nowMs int64) (RoomGroup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[code]
	if !ok || !liveAt(c.ExpiresAtMs, nowMs) {
		return RoomGroup{}, ErrRoomGroupCode
	}
	delete(m.codes, code)
	g := RoomGroup{TenantID: c.TenantID, RoomID: c.RoomID, Name: c.Name, Notifier: notifier, Address: address, ConnectedBy: c.CreatedBy, CreatedAtMs: nowMs}
	m.groups[[3]string{c.TenantID, c.RoomID, c.Name}] = g
	return g, nil
}

// room_groups, room_group_codes: deleted with the room.
func (m *MemoryRoomGroups) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.groups {
		if k[0] == tenantID && k[1] == roomID {
			delete(m.groups, k)
		}
	}
	for k, c := range m.codes {
		if c.TenantID == tenantID && c.RoomID == roomID {
			delete(m.codes, k)
		}
	}
}

// --- MySQL ---

// MySQLRoomGroups is the MySQL RoomGroupStore over room_groups and
// room_group_codes.
type MySQLRoomGroups struct{ DB *DB }

// NewMySQLRoomGroups builds the MySQL store.
func NewMySQLRoomGroups(db *DB) *MySQLRoomGroups { return &MySQLRoomGroups{DB: db} }

const roomGroupCols = `tenant_id, room_id, name, notifier, address, connected_by, created_at_ms`

func scanRoomGroup(r rowScanner) (RoomGroup, error) {
	var g RoomGroup
	err := r.Scan(&g.TenantID, &g.RoomID, &g.Name, &g.Notifier, &g.Address, &g.ConnectedBy, &g.CreatedAtMs)
	return g, err
}

func (s *MySQLRoomGroups) ListRoomGroups(ctx context.Context, roomID string) ([]RoomGroup, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, s.DB, `SELECT `+roomGroupCols+` FROM room_groups WHERE tenant_id = ? AND room_id = ? ORDER BY name`, scanRoomGroup, tid, roomID)
	if out == nil && err == nil {
		out = []RoomGroup{}
	}
	return out, err
}

func (s *MySQLRoomGroups) GetRoomGroup(ctx context.Context, roomID, name string) (RoomGroup, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomGroup{}, err
	}
	g, err := scanRoomGroup(s.DB.QueryRowContext(ctx, `SELECT `+roomGroupCols+` FROM room_groups WHERE tenant_id = ? AND room_id = ? AND name = ?`, tid, roomID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomGroup{}, ErrRoomGroupNotFound
	}
	return g, err
}

func (s *MySQLRoomGroups) DeleteRoomGroup(ctx context.Context, roomID, name string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM room_groups WHERE tenant_id = ? AND room_id = ? AND name = ?`, tid, roomID, name)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrRoomGroupNotFound)
}

func (s *MySQLRoomGroups) PutRoomGroupCode(ctx context.Context, c RoomGroupCode) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO room_group_codes (code, tenant_id, room_id, name, created_by, expires_at_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		c.Code, tid, c.RoomID, c.Name, c.CreatedBy, c.ExpiresAtMs)
	return err
}

func (s *MySQLRoomGroups) OpenRoomGroupCodes(ctx context.Context, roomID string, nowMs int64) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_group_codes WHERE tenant_id = ? AND room_id = ? AND expires_at_ms > ?`, tid, roomID, nowMs).Scan(&n)
	return n, err
}

func (s *MySQLRoomGroups) BindRoomGroup(ctx context.Context, code, notifier, address string, nowMs int64) (RoomGroup, error) {
	// cross-tenant: a notifier's bind names no tenant; the code carries it.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoomGroup{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var c RoomGroupCode
	err = tx.QueryRowContext(ctx, `SELECT code, tenant_id, room_id, name, created_by, expires_at_ms FROM room_group_codes WHERE code = ? FOR UPDATE`, code).
		Scan(&c.Code, &c.TenantID, &c.RoomID, &c.Name, &c.CreatedBy, &c.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !liveAt(c.ExpiresAtMs, nowMs)) {
		return RoomGroup{}, ErrRoomGroupCode
	}
	if err != nil {
		return RoomGroup{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM room_group_codes WHERE code = ?`, code); err != nil {
		return RoomGroup{}, err
	}
	g := RoomGroup{TenantID: c.TenantID, RoomID: c.RoomID, Name: c.Name, Notifier: notifier, Address: address, ConnectedBy: c.CreatedBy, CreatedAtMs: nowMs}
	if _, err := tx.ExecContext(ctx, `INSERT INTO room_groups (`+roomGroupCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE notifier = VALUES(notifier), address = VALUES(address), connected_by = VALUES(connected_by), created_at_ms = VALUES(created_at_ms)`,
		g.TenantID, g.RoomID, g.Name, g.Notifier, g.Address, g.ConnectedBy, g.CreatedAtMs); err != nil {
		return RoomGroup{}, err
	}
	return g, tx.Commit()
}
