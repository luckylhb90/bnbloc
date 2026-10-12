package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLRoom implements RoomStore over database/sql. Schema matches deploy/schema.sql.
type MySQLRoom struct{ DB *sql.DB }

// NewMySQLRoom constructs a MySQL-backed room store.
func NewMySQLRoom(db *sql.DB) *MySQLRoom { return &MySQLRoom{DB: db} }

var _ RoomStore = (*MySQLRoom)(nil)

// CreateRoom inserts a new room; ErrRoomExists when the id is taken in any
// tenant. Visibility defaults to private when unset.
func (s *MySQLRoom) CreateRoom(ctx context.Context, r Room) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if r.Visibility == "" {
		r.Visibility = VisibilityPrivate
	}
	if r.SyncMode == "" {
		r.SyncMode = SyncModeGit
	}
	return insertRoom(ctx, s.DB, tid, r)
}

// insertRoom inserts a room row. It is a plain INSERT, never an upsert: rooms'
// primary key is room_id alone (globally unique, never reused after delete),
// so a duplicate means another room — possibly another tenant's — already owns
// the id, and the create must fail instead of adopting it (BE-1: the former
// ON DUPLICATE KEY UPDATE let any caller re-"create" an existing room and be
// stamped its owner, including across tenants).
func insertRoom(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, tid string, r Room) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO rooms (tenant_id, room_id, name, description, visibility, sync_mode, created_by, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tid, r.RoomID, r.Name, r.Description, string(r.Visibility), string(r.SyncMode), r.CreatedBy, r.CreatedAtMs)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlErrDupEntry {
		return ErrRoomExists
	}
	return err
}

// CreateRoomWithOwner atomically inserts a room and its owner membership in a
// single transaction. If the owner insert fails the room insert is rolled back.
func (s *MySQLRoom) CreateRoomWithOwner(ctx context.Context, r Room, owner Membership) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.createRoomTx(ctx, tx, r, owner, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateRoomWithOwnerAndChannel atomically inserts a room, its owner
// membership, and the room's default channel in a single transaction. If any
// insert fails the whole create is rolled back — a room must never exist
// without its default channel.
func (s *MySQLRoom) CreateRoomWithOwnerAndChannel(ctx context.Context, r Room, owner Membership, def RoomChannel) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.createRoomTx(ctx, tx, r, owner, &def); err != nil {
		return err
	}
	return tx.Commit()
}

// createRoomTx inserts a room, its owner membership, and (when def is
// non-nil) the room's default channel on one transaction. It never commits or
// rolls back; the caller owns the transaction.
func (s *MySQLRoom) createRoomTx(ctx context.Context, tx *sql.Tx, r Room, owner Membership, def *RoomChannel) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if r.Visibility == "" {
		r.Visibility = VisibilityPrivate
	}
	if r.SyncMode == "" {
		r.SyncMode = SyncModeGit
	}
	if err := insertRoom(ctx, tx, tid, r); err != nil {
		return err
	}
	// The room row is new (insertRoom refuses an existing id), so its owner
	// membership and default channel are plain inserts too.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms) VALUES (?, ?, ?, ?, ?)`,
		tid, owner.RoomID, owner.UserID, string(owner.Role), owner.JoinedAtMs); err != nil {
		return err
	}
	if def != nil {
		// The context tenant is authoritative; def.TenantID from the caller is
		// not trusted for row ownership.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO room_channels (channel_id, tenant_id, room_id, name, is_default, created_by, created_at_ms, deleted_at_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			def.ChannelID, tid, def.RoomID, def.Name, def.IsDefault, def.CreatedBy, def.CreatedAtMs, def.DeletedAtMs); err != nil {
			return err
		}
	}
	return nil
}

// GetRoom returns a room or ErrRoomNotFound.
// RoomTenant returns a room's authoritative tenant, keyed on room_id (the PK,
// globally unique) with no tenant filter. It is how a room-scoped request
// resolves which tenant to execute in, independent of the caller's session
// tenant. Returns false when the room does not exist or is deleted.
func (s *MySQLRoom) RoomTenant(ctx context.Context, roomID string) (string, bool) {
	var tid string
	err := s.DB.QueryRowContext(ctx,
		`SELECT tenant_id FROM rooms WHERE room_id = ? AND deleted_at_ms IS NULL`, roomID).Scan(&tid)
	if err != nil {
		return "", false
	}
	return tid, true
}

// GetRoom returns a room scoped to the caller's tenant. Room ids are NOT
// globally unique (two tenants may each have their own room with the same id),
// so a caller reaching a room outside their session tenant resolves it through
// the room-anchored membership probes (MembershipAnyTenant / TenantOfRoom), not
// by un-scoping this read.
func (s *MySQLRoom) GetRoom(ctx context.Context, roomID string) (Room, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Room{}, err
	}
	var r Room
	var visibility, syncMode string
	err = s.DB.QueryRowContext(ctx,
		`SELECT tenant_id, room_id, name, description, visibility, sync_mode, created_by, created_at_ms
		 FROM rooms WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL`,
		tid, roomID).Scan(&r.TenantID, &r.RoomID, &r.Name, &r.Description, &visibility, &syncMode, &r.CreatedBy, &r.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrRoomNotFound
	}
	r.Visibility = NormalizeVisibility(visibility)
	r.SyncMode = NormalizeSyncMode(syncMode)
	return r, err
}

// AddMember inserts or updates a membership. It is always a human
// membership: an agent-host row of the same user becomes a human one. The row
// is selected from the ctx tenant's live room, so the key (room_id, user_id),
// which carries no tenant, can never be written for another tenant's room.
func (s *MySQLRoom) AddMember(ctx context.Context, m Membership) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms, agent_host)
		 SELECT tenant_id, room_id, ?, ?, ?, 0 FROM rooms
		 WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL
		 ON DUPLICATE KEY UPDATE role = VALUES(role), agent_host = 0`,
		m.UserID, string(m.Role), m.JoinedAtMs, tid, m.RoomID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrRoomNotFound)
}

// RemoveMember deletes a membership.
func (s *MySQLRoom) RemoveMember(ctx context.Context, roomID, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM room_members WHERE tenant_id = ? AND room_id = ? AND user_id = ?`, tid, roomID, userID)
	return err
}

// AddAgentHost inserts an agent-host membership (visitor role) unless the
// user already holds a membership of the room: INSERT IGNORE never
// downgrades a human member.
func (s *MySQLRoom) AddAgentHost(ctx context.Context, roomID, userID string, atMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT IGNORE INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms, agent_host)
		 SELECT tenant_id, room_id, ?, 'visitor', ?, 1 FROM rooms
		 WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL`,
		userID, atMs, tid, roomID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing inserted: either the user is already a member (fine) or
		// the room does not exist here.
		if _, err := s.GetRoom(ctx, roomID); err != nil {
			return err
		}
	}
	return nil
}

// RemoveAgentHost deletes the user's agent-host membership only.
func (s *MySQLRoom) RemoveAgentHost(ctx context.Context, roomID, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM room_members WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND agent_host = 1`, tid, roomID, userID)
	return err
}

// GetMember returns a user's membership of a room within the caller's
// tenant scope. Room ids are not globally unique; the room-anchored
// cross-tenant probe (MembershipAnyTenant) exists for the multi-tenant
// member path.
func (s *MySQLRoom) GetMember(ctx context.Context, roomID, userID string) (Membership, bool) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Membership{}, false
	}
	return scanMembership(s.DB.QueryRowContext(ctx,
		`SELECT `+membershipColumns+` FROM room_members WHERE tenant_id = ? AND room_id = ? AND user_id = ?`, tid, roomID, userID))
}

// MemberRole returns a user's role in a room within the caller's tenant.
func (s *MySQLRoom) MemberRole(ctx context.Context, roomID, userID string) (Role, bool) {
	m, ok := s.GetMember(ctx, roomID, userID)
	return m.Role, ok
}

// IsMember reports membership.
func (s *MySQLRoom) IsMember(ctx context.Context, roomID, userID string) bool {
	_, ok := s.GetMember(ctx, roomID, userID)
	return ok
}

// MembershipAnyTenant answers "is this user in this room" without pinning
// the caller's tenant. Used by the gateway room-scope fallback for a user
// whose room lives in a tenant other than the one their token was minted in
// (a multi-tenant member). The membership must be filed under the room's own
// tenant and the room must be live: a membership row under any other tenant
// (e.g. one left by the pre-BE-1 create upsert) never grants access.
func (s *MySQLRoom) MembershipAnyTenant(ctx context.Context, roomID, userID string) (Membership, bool) {
	return scanMembership(s.DB.QueryRowContext(ctx,
		`SELECT m.tenant_id, m.room_id, m.user_id, m.role, m.joined_at_ms, m.agent_host FROM room_members m
		 JOIN rooms r ON r.room_id = m.room_id AND r.tenant_id = m.tenant_id AND r.deleted_at_ms IS NULL
		 WHERE m.room_id = ? AND m.user_id = ? LIMIT 1`, roomID, userID))
}

// membershipColumns is the column list scanMembership reads, in its order.
const membershipColumns = `tenant_id, room_id, user_id, role, joined_at_ms, agent_host`

type membershipScanner interface{ Scan(dest ...any) error }

// scanMembership reads one membershipColumns row; false on no row or error.
func scanMembership(row membershipScanner) (Membership, bool) {
	var m Membership
	var role string
	if err := row.Scan(&m.TenantID, &m.RoomID, &m.UserID, &role, &m.JoinedAtMs, &m.AgentHost); err != nil {
		return Membership{}, false
	}
	m.Role = Role(role)
	return m, true
}

// Members returns all memberships for a room, agent hosts flagged.
func (s *MySQLRoom) Members(ctx context.Context, roomID string) []Membership {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+membershipColumns+` FROM room_members WHERE tenant_id = ? AND room_id = ?`, tid, roomID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		if m, ok := scanMembership(rows); ok {
			out = append(out, m)
		}
	}
	return out
}

// RoomsForUser returns the room ids the user is a member of in the caller's
// tenant scope (UI listings, login room-stamping). The bridge discovery
// shape is RoomsForUserAllTenants.
func (s *MySQLRoom) RoomsForUser(ctx context.Context, userID string) []string {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT room_id FROM room_members WHERE tenant_id = ? AND user_id = ? AND agent_host = 0`, tid, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// RoomsForUserAllTenants returns every room the user is a member of across
// all tenants — the BRIDGE discovery shape: the bridge subscribes, advertises,
// and claims in every room its owner belongs to, regardless of which tenant
// its owner's session token was minted in. UI listings stay tenant-scoped.
func (s *MySQLRoom) RoomsForUserAllTenants(ctx context.Context, userID string) []string {
	return s.roomsAllTenants(ctx, userID, false)
}

// AgentHostRoomsAllTenants returns the live rooms, across all tenants, where
// the user holds an agent-host membership.
func (s *MySQLRoom) AgentHostRoomsAllTenants(ctx context.Context, userID string) []string {
	return s.roomsAllTenants(ctx, userID, true)
}

func (s *MySQLRoom) roomsAllTenants(ctx context.Context, userID string, agentHost bool) []string {
	if _, err := tenant.Require(ctx); err != nil {
		return nil
	}
	// Same rule as MembershipAnyTenant: only memberships filed under the
	// room's own tenant, of live rooms.
	rows, err := s.DB.QueryContext(ctx,
		`SELECT m.room_id FROM room_members m
		 JOIN rooms r ON r.room_id = m.room_id AND r.tenant_id = m.tenant_id AND r.deleted_at_ms IS NULL
		 WHERE m.user_id = ? AND m.agent_host = ?`, userID, agentHost)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// CreateInvite stores an invite code.
func (s *MySQLRoom) CreateInvite(ctx context.Context, inv Invite) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO room_invites (code, tenant_id, room_id, role, expires_at_ms, capacity, redeemed_count, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Code, tid, inv.RoomID, string(inv.Role), inv.ExpiresAtMs, inv.Capacity, inv.RedeemedCount, inv.CreatedBy)
	return err
}

// RedeemInvite atomically validates and consumes an invite for a user.
func (s *MySQLRoom) RedeemInvite(ctx context.Context, code, userID string, nowMs int64) (Membership, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Membership{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Membership{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var inv Invite
	var role string
	err = tx.QueryRowContext(ctx,
		`SELECT code, room_id, role, expires_at_ms, capacity, redeemed_count FROM room_invites WHERE code = ? AND tenant_id = ? FOR UPDATE`,
		code, tid).Scan(&inv.Code, &inv.RoomID, &role, &inv.ExpiresAtMs, &inv.Capacity, &inv.RedeemedCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, ErrInviteNotFound
	}
	if err != nil {
		return Membership{}, err
	}
	inv.Role = Role(role)

	// The target room must be active; a soft-deleted room cannot be joined.
	var roomID string
	roomErr := tx.QueryRowContext(ctx,
		`SELECT room_id FROM rooms WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL FOR UPDATE`, tid, inv.RoomID).Scan(&roomID)
	if errors.Is(roomErr, sql.ErrNoRows) {
		return Membership{}, ErrRoomNotFound
	}
	if roomErr != nil {
		return Membership{}, roomErr
	}

	// Idempotent: an existing member redeeming again returns their membership.
	// An agent-host membership is not one: the invite makes the user a human
	// member (the insert below converts the row).
	var existingRole string
	memErr := tx.QueryRowContext(ctx,
		`SELECT role FROM room_members WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND agent_host = 0`, tid, inv.RoomID, userID).Scan(&existingRole)
	if memErr == nil {
		if err := tx.Commit(); err != nil {
			return Membership{}, err
		}
		return Membership{TenantID: tid, RoomID: inv.RoomID, UserID: userID, Role: Role(existingRole)}, nil
	}

	if nowMs > inv.ExpiresAtMs {
		return Membership{}, ErrInviteExpired
	}
	if inv.RedeemedCount >= inv.Capacity {
		return Membership{}, ErrInviteFull
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_invites SET redeemed_count = redeemed_count + 1 WHERE code = ? AND tenant_id = ?`, code, tid); err != nil {
		return Membership{}, err
	}
	m := Membership{TenantID: tid, RoomID: inv.RoomID, UserID: userID, Role: inv.Role, JoinedAtMs: nowMs}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms, agent_host) VALUES (?, ?, ?, ?, ?, 0)
		 ON DUPLICATE KEY UPDATE role = VALUES(role), agent_host = 0`,
		tid, m.RoomID, m.UserID, string(m.Role), m.JoinedAtMs); err != nil {
		return Membership{}, err
	}
	if err := tx.Commit(); err != nil {
		return Membership{}, err
	}
	return m, nil
}

// MemberCount returns the number of members in a room.
func (s *MySQLRoom) MemberCount(ctx context.Context, roomID string) int {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_members WHERE tenant_id = ? AND room_id = ? AND agent_host = 0`, tid, roomID).Scan(&n); err != nil {
		return 0
	}
	return n
}

// SearchRooms returns non-deleted rooms matching the query, newest-first, with
// member counts. An empty query returns all rooms. total is the pre-page count.
func (s *MySQLRoom) SearchRooms(ctx context.Context, query string, limit, offset int) ([]RoomWithCount, int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := strings.TrimSpace(query)
	like := "%" + q + "%"
	where := "tenant_id = ? AND deleted_at_ms IS NULL"
	args := []any{tid}
	if q != "" {
		where += " AND (name LIKE ? OR description LIKE ?)"
		args = append(args, like, like)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT r.tenant_id, r.room_id, r.name, r.description, r.visibility, r.sync_mode, r.created_by, r.created_at_ms,
		        COUNT(m.user_id) AS member_count
		 FROM rooms r
		 LEFT JOIN room_members m ON m.tenant_id = r.tenant_id AND m.room_id = r.room_id AND m.agent_host = 0
		 WHERE `+strings.ReplaceAll(where, "tenant_id", "r.tenant_id")+`
		 GROUP BY r.tenant_id, r.room_id, r.name, r.description, r.visibility, r.sync_mode, r.created_by, r.created_at_ms
		 ORDER BY r.created_at_ms DESC, r.room_id ASC
		 LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []RoomWithCount
	for rows.Next() {
		var rc RoomWithCount
		var visibility, syncMode string
		if err := rows.Scan(&rc.TenantID, &rc.RoomID, &rc.Name, &rc.Description, &visibility, &syncMode, &rc.CreatedBy, &rc.CreatedAtMs, &rc.MemberCount); err != nil {
			continue
		}
		rc.Visibility = NormalizeVisibility(visibility)
		rc.SyncMode = NormalizeSyncMode(syncMode)
		out = append(out, rc)
	}
	return out, total, nil
}

// UpdateRoom applies a partial patch (name/description/visibility).
func (s *MySQLRoom) UpdateRoom(ctx context.Context, roomID string, patch RoomPatch) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	sets := []string{}
	args := []any{}
	if patch.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *patch.Name)
	}
	if patch.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *patch.Description)
	}
	if patch.Visibility != nil {
		sets = append(sets, "visibility = ?")
		args = append(args, string(*patch.Visibility))
	}
	if patch.SyncMode != nil {
		sets = append(sets, "sync_mode = ?")
		args = append(args, string(*patch.SyncMode))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, tid, roomID)
	res, err := s.DB.ExecContext(ctx, `UPDATE rooms SET `+strings.Join(sets, ", ")+` WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoomNotFound
	}
	return nil
}

// DeleteRoom soft-deletes a room and hard-deletes every room-owned row in one
// tx. The room row itself is kept (soft-deleted) so its id is never reused.
func (s *MySQLRoom) DeleteRoom(ctx context.Context, roomID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE rooms SET deleted_at_ms = ? WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL`, time.Now().UnixMilli(), tid, roomID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoomNotFound
	}
	// Tables keyed by a parent id rather than room_id are cleared through their
	// parent before the parent rows go: leases via the room's grants, artifact
	// versions via the room's artifacts.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM shared_agent_leases
		 WHERE tenant_id = ? AND grant_id IN (SELECT grant_id FROM shared_agent_grants WHERE tenant_id = ? AND room_id = ?)`, tid, tid, roomID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM artifact_versions
		 WHERE tenant_id = ? AND artifact_id IN (SELECT artifact_id FROM artifacts WHERE tenant_id = ? AND room_id = ?)`, tid, tid, roomID); err != nil {
		return err
	}
	// Every remaining room-owned table keys on (tenant_id, room_id). Attachment
	// rows are metadata; their bytes live in the content-addressed blob store,
	// which is shared (artifact distribution) and not room-owned.
	childTables := []string{
		"room_members",
		"room_messages",
		"room_read_state",
		"room_invites",
		"room_channels",
		"agent_sessions",
		"agent_messages",
		"agent_jobs",
		"sync_states",
		"shared_agent_grants",
		"shared_agent_audit",
		"attachments",
		"artifacts",
		"artifact_shares",
		"user_agent_prefs",
		"room_imports",
		"room_schedules",
	}
	for _, t := range childTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE tenant_id = ? AND room_id = ?`, tid, roomID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TransferOwner makes newOwner the owner and demotes existing owners to member.
func (s *MySQLRoom) TransferOwner(ctx context.Context, roomID, newOwner string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists string
	roomErr := tx.QueryRowContext(ctx, `SELECT room_id FROM rooms WHERE tenant_id = ? AND room_id = ? AND deleted_at_ms IS NULL`, tid, roomID).Scan(&exists)
	if errors.Is(roomErr, sql.ErrNoRows) {
		return ErrRoomNotFound
	}
	if roomErr != nil {
		return roomErr
	}
	var role string
	err = tx.QueryRowContext(ctx, `SELECT role FROM room_members WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND agent_host = 0`, tid, roomID, newOwner).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("store: new owner is not a member")
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_members SET role = 'member' WHERE tenant_id = ? AND room_id = ? AND role = 'owner'`, tid, roomID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_members SET role = 'owner' WHERE tenant_id = ? AND room_id = ? AND user_id = ?`, tid, roomID, newOwner); err != nil {
		return err
	}
	return tx.Commit()
}

// RoomsForUserDetailed returns rich rows for the user's non-deleted rooms
// in the caller's tenant scope — this is the UI "my rooms" listing and MUST
// stay tenant-scoped: switching tenants switches the visible workspace.
// (The bridge discovery shape is the separate RoomsForUserAllTenants.)
func (s *MySQLRoom) RoomsForUserDetailed(ctx context.Context, userID string) []RoomMembershipDetail {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT r.tenant_id, r.room_id, r.name, r.description, r.visibility, r.sync_mode, r.created_by, r.created_at_ms, m.role,
		        (SELECT COUNT(*) FROM room_members mm WHERE mm.tenant_id = r.tenant_id AND mm.room_id = r.room_id AND mm.agent_host = 0) AS member_count
		 FROM room_members m
		 JOIN rooms r ON r.tenant_id = m.tenant_id AND r.room_id = m.room_id
		 WHERE m.tenant_id = ? AND m.user_id = ? AND m.agent_host = 0 AND r.deleted_at_ms IS NULL
		 ORDER BY r.created_at_ms DESC, r.room_id ASC`, tid, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RoomMembershipDetail
	for rows.Next() {
		var d RoomMembershipDetail
		var visibility, syncMode, role string
		if err := rows.Scan(&d.TenantID, &d.RoomID, &d.Name, &d.Description, &visibility, &syncMode, &d.CreatedBy, &d.CreatedAtMs, &role, &d.MemberCount); err != nil {
			continue
		}
		d.Visibility = NormalizeVisibility(visibility)
		d.SyncMode = NormalizeSyncMode(syncMode)
		d.Role = Role(role)
		d.IsOwner = d.Role == RoleOwner
		out = append(out, d)
	}
	return out
}
