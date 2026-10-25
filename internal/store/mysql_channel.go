package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLChannel is the MySQL-backed ChannelStore. Like the other room tables,
// every query is scoped by the tenant bound to the context and fails closed
// when the context carries none.
type MySQLChannel struct {
	DB *DB
}

// NewMySQLChannel constructs a MySQL-backed channel store.
func NewMySQLChannel(db *DB) *MySQLChannel { return &MySQLChannel{DB: db} }

var _ ChannelStore = (*MySQLChannel)(nil)

func (m *MySQLChannel) PutChannel(ctx context.Context, c RoomChannel) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// The context tenant is authoritative; c.TenantID from the caller is not
	// trusted for row ownership.
	c.TenantID = tnt
	// A plain insert: channel_id is the whole key (no tenant), so an upsert
	// could rewrite another tenant's channel.
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO room_channels (channel_id, tenant_id, room_id, name, is_default, kind, created_by, created_at_ms, deleted_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ChannelID, c.TenantID, c.RoomID, c.Name, c.IsDefault, c.Kind, c.CreatedBy, c.CreatedAtMs, c.DeletedAtMs)
	return dupAs(err, ErrChannelExists)
}

func (m *MySQLChannel) GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, false, err
	}
	return m.scanChannel(ctx, `
SELECT `+channelColumns+` FROM room_channels
WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND `+sqlNotDeleted,
		tnt, roomID, channelID)
}

// ListChannels returns a room's live channels, default first, then oldest
// first, then by id.
func (m *MySQLChannel) ListChannels(ctx context.Context, roomID string) ([]RoomChannel, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, m.DB, `
SELECT `+channelColumns+` FROM room_channels
WHERE tenant_id = ? AND room_id = ? AND `+sqlNotDeleted+`
ORDER BY is_default DESC, created_at_ms ASC, channel_id ASC`, scanChannelRow, tnt, roomID)
}

// DefaultChannel returns a room's default channel, found=false when none.
func (m *MySQLChannel) DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, false, err
	}
	return m.scanChannel(ctx, `
SELECT `+channelColumns+` FROM room_channels
WHERE tenant_id = ? AND room_id = ? AND is_default = 1 AND `+sqlNotDeleted,
		tnt, roomID)
}

func (m *MySQLChannel) DeleteChannel(ctx context.Context, roomID, channelID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// Soft delete only; deleting an absent or already-deleted channel is a
	// no-op, matching the memory store.
	_, err = m.DB.ExecContext(ctx, `
UPDATE room_channels SET deleted_at_ms = ?
WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND `+sqlNotDeleted,
		atMs, tnt, roomID, channelID)
	return err
}

// RenameChannel renames a live channel; an absent or soft-deleted channel maps
// to ErrChannelNotFound, so a rename can never resurrect a deleted row.
func (m *MySQLChannel) RenameChannel(ctx context.Context, roomID, channelID, name string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `
UPDATE room_channels SET name = ?
WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND `+sqlNotDeleted,
		name, tnt, roomID, channelID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrChannelNotFound)
}

// scanChannel runs a single-row channel query: a missing row is found=false,
// any other failure an error.
func (m *MySQLChannel) scanChannel(ctx context.Context, query string, args ...any) (RoomChannel, bool, error) {
	c, err := scanChannelRow(m.DB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomChannel{}, false, nil
	}
	if err != nil {
		return RoomChannel{}, false, err
	}
	return c, true, nil
}

// channelColumns is the column list scanChannelRow reads, in order.
const channelColumns = "channel_id, tenant_id, room_id, name, is_default, kind, created_by, created_at_ms, deleted_at_ms"

// EnsurePeopleChannel inserts the room's people chat under its derived id
// unless that row exists (INSERT IGNORE on the primary key), then reads it
// back, so concurrent first opens converge on one channel.
func (m *MySQLChannel) EnsurePeopleChannel(ctx context.Context, roomID, creator string, atMs int64) (RoomChannel, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomChannel{}, err
	}
	id := peopleChannelID(tnt, roomID)
	if _, err := m.DB.ExecContext(ctx, `
INSERT IGNORE INTO room_channels (channel_id, tenant_id, room_id, name, is_default, kind, created_by, created_at_ms)
VALUES (?, ?, ?, ?, 0, ?, ?, ?)`, id, tnt, roomID, PeopleChannelName, ChannelKindPeople, creator, atMs); err != nil {
		return RoomChannel{}, err
	}
	c, err := scanChannelRow(m.DB.QueryRowContext(ctx, `
SELECT `+channelColumns+` FROM room_channels WHERE tenant_id = ? AND room_id = ? AND channel_id = ?`, tnt, roomID, id))
	if err != nil {
		return RoomChannel{}, err
	}
	return c, nil
}

// ChannelKind reads a channel's kind (soft-deleted rows included); a read
// failure is returned, never mapped to "not found".
func (m *MySQLChannel) ChannelKind(ctx context.Context, roomID, channelID string) (string, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return "", false, err
	}
	var kind string
	err = m.DB.QueryRowContext(ctx, `
SELECT kind FROM room_channels WHERE tenant_id = ? AND room_id = ? AND channel_id = ?`, tnt, roomID, channelID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return kind, true, nil
}

func scanChannelRow(sc rowScanner) (RoomChannel, error) {
	var c RoomChannel
	err := sc.Scan(&c.ChannelID, &c.TenantID, &c.RoomID, &c.Name, &c.IsDefault, &c.Kind, &c.CreatedBy, &c.CreatedAtMs, &c.DeletedAtMs)
	return c, err
}
