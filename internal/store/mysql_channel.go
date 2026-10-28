package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
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

// channelSettingsJSON is a channel's settings as room_channels.settings_json
// holds them: NULL for the zero document.
func channelSettingsJSON(s wire.ChannelSettings) (any, error) {
	if s == (wire.ChannelSettings{}) {
		return nil, nil
	}
	return jsonArg(s)
}

// decodeChannelSettings reads settings_json (NULL is the zero document).
func decodeChannelSettings(raw sql.NullString) (wire.ChannelSettings, error) {
	var s wire.ChannelSettings
	if !raw.Valid || raw.String == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw.String), &s); err != nil {
		return s, fmt.Errorf("decode channel settings: %w", err)
	}
	return s, nil
}

func (m *MySQLChannel) PutChannel(ctx context.Context, c RoomChannel) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// The context tenant is authoritative; c.TenantID from the caller is not
	// trusted for row ownership.
	c.TenantID = tnt
	settings, err := channelSettingsJSON(c.Settings)
	if err != nil {
		return err
	}
	// A plain insert: channel_id is the whole key (no tenant), so an upsert
	// could rewrite another tenant's channel.
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO room_channels (channel_id, tenant_id, room_id, name, is_default, kind, created_by, created_at_ms, deleted_at_ms,
  purpose, rules, section_id, position, owner_id, archived_at_ms, settings_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ChannelID, c.TenantID, c.RoomID, c.Name, c.IsDefault, c.Kind, c.CreatedBy, c.CreatedAtMs, c.DeletedAtMs,
		c.Purpose, nullIfZero(c.Rules), c.SectionID, c.Position, c.OwnerID, c.ArchivedAtMs, settings)
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

// ListChannels returns a room's live channels, default first, then by
// position, then oldest first, then by id (sortChannels).
func (m *MySQLChannel) ListChannels(ctx context.Context, roomID string) ([]RoomChannel, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, m.DB, `
SELECT `+channelColumns+` FROM room_channels
WHERE tenant_id = ? AND room_id = ? AND `+sqlNotDeleted+`
ORDER BY is_default DESC, position ASC, created_at_ms ASC, channel_id ASC`, scanChannelRow, tnt, roomID)
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

// UpdateChannel applies a patch to a live channel; an absent or soft-deleted
// channel maps to ErrChannelNotFound, so a change can never resurrect a
// deleted row.
func (m *MySQLChannel) UpdateChannel(ctx context.Context, roomID, channelID string, patch ChannelPatch) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	sets, args := []string{}, []any{}
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if patch.Name != nil {
		add("name", *patch.Name)
	}
	if patch.Purpose != nil {
		add("purpose", *patch.Purpose)
	}
	if patch.Rules != nil {
		add("rules", nullIfZero(*patch.Rules))
	}
	if patch.SectionID != nil {
		add("section_id", *patch.SectionID)
	}
	if patch.Position != nil {
		add("position", *patch.Position)
	}
	if patch.OwnerID != nil {
		add("owner_id", *patch.OwnerID)
	}
	if patch.Settings != nil {
		settings, err := channelSettingsJSON(*patch.Settings)
		if err != nil {
			return err
		}
		add("settings_json", settings)
	}
	if len(sets) == 0 {
		if _, ok, err := m.GetChannel(ctx, roomID, channelID); err != nil || !ok {
			if err == nil {
				err = ErrChannelNotFound
			}
			return err
		}
		return nil
	}
	args = append(args, tnt, roomID, channelID)
	res, err := m.DB.ExecContext(ctx, `UPDATE room_channels SET `+strings.Join(sets, ", ")+`
WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND `+sqlNotDeleted, args...)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrChannelNotFound)
}

// SetChannelArchived archives (keeping the first time) or restores a live
// channel.
func (m *MySQLChannel) SetChannelArchived(ctx context.Context, roomID, channelID string, atMs *int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	set, args := "archived_at_ms = NULL", []any{}
	if atMs != nil {
		set, args = "archived_at_ms = COALESCE(archived_at_ms, ?)", []any{*atMs}
	}
	args = append(args, tnt, roomID, channelID)
	res, err := m.DB.ExecContext(ctx, `UPDATE room_channels SET `+set+`
WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND `+sqlNotDeleted, args...)
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
const channelColumns = "channel_id, tenant_id, room_id, name, is_default, kind, created_by, created_at_ms, deleted_at_ms, " +
	"purpose, rules, section_id, position, owner_id, archived_at_ms, settings_json"

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
INSERT IGNORE INTO room_channels (channel_id, tenant_id, room_id, name, is_default, kind, created_by, owner_id, created_at_ms)
VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)`, id, tnt, roomID, PeopleChannelName, ChannelKindPeople, creator, creator, atMs); err != nil {
		return RoomChannel{}, err
	}
	c, err := scanChannelRow(m.DB.QueryRowContext(ctx, `
SELECT `+channelColumns+` FROM room_channels WHERE tenant_id = ? AND room_id = ? AND channel_id = ?`, tnt, roomID, id))
	if err != nil {
		return RoomChannel{}, err
	}
	return c, nil
}

// ChannelAdmission reads what decides agents' admission (soft-deleted rows
// included); a read failure is returned, never mapped to "not found".
func (m *MySQLChannel) ChannelAdmission(ctx context.Context, roomID, channelID string) (ChannelAdmission, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return ChannelAdmission{}, false, err
	}
	var kind string
	var raw sql.NullString
	err = m.DB.QueryRowContext(ctx, `
SELECT kind, settings_json FROM room_channels WHERE tenant_id = ? AND room_id = ? AND channel_id = ?`, tnt, roomID, channelID).Scan(&kind, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelAdmission{}, false, nil
	}
	if err != nil {
		return ChannelAdmission{}, false, err
	}
	settings, err := decodeChannelSettings(raw)
	if err != nil {
		return ChannelAdmission{}, false, err
	}
	return ChannelAdmission{Kind: kind, NoAgents: settings.NoAgents}, true, nil
}

func scanChannelRow(sc rowScanner) (RoomChannel, error) {
	var c RoomChannel
	var rules, settings sql.NullString
	err := sc.Scan(&c.ChannelID, &c.TenantID, &c.RoomID, &c.Name, &c.IsDefault, &c.Kind, &c.CreatedBy, &c.CreatedAtMs, &c.DeletedAtMs,
		&c.Purpose, &rules, &c.SectionID, &c.Position, &c.OwnerID, &c.ArchivedAtMs, &settings)
	if err != nil {
		return c, err
	}
	c.Rules = rules.String
	c.Settings, err = decodeChannelSettings(settings)
	return c, err
}

// sectionColumns is the column list scanSectionRow reads, in order.
const sectionColumns = "section_id, tenant_id, room_id, name, position, visibility, rules, owners_json, join_policy, created_by, created_at_ms, deleted_at_ms"

func scanSectionRow(sc rowScanner) (RoomSection, error) {
	var s RoomSection
	var rules, owners sql.NullString
	err := sc.Scan(&s.SectionID, &s.TenantID, &s.RoomID, &s.Name, &s.Position, &s.Visibility, &rules, &owners, &s.JoinPolicy, &s.CreatedBy, &s.CreatedAtMs, &s.DeletedAtMs)
	if err != nil {
		return s, err
	}
	s.Rules = rules.String
	s.Owners = []string{}
	if owners.Valid && owners.String != "" {
		if err := json.Unmarshal([]byte(owners.String), &s.Owners); err != nil {
			return s, fmt.Errorf("decode section owners: %w", err)
		}
	}
	return s, nil
}

// sectionOwnersJSON is a section's owners as owners_json holds them.
func sectionOwnersJSON(owners []string) (json.RawMessage, error) {
	if owners == nil {
		owners = []string{}
	}
	return jsonArg(owners)
}

func (m *MySQLChannel) PutSection(ctx context.Context, s RoomSection) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	owners, err := sectionOwnersJSON(s.Owners)
	if err != nil {
		return err
	}
	// A plain insert: section_id is the whole key, like a channel's.
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO room_sections (section_id, tenant_id, room_id, name, position, visibility, rules, owners_json, join_policy, created_by, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SectionID, tnt, s.RoomID, s.Name, s.Position, s.Visibility, nullIfZero(s.Rules), owners, s.JoinPolicy, s.CreatedBy, s.CreatedAtMs)
	return dupAs(err, ErrSectionExists)
}

func (m *MySQLChannel) GetSection(ctx context.Context, roomID, sectionID string) (RoomSection, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return RoomSection{}, false, err
	}
	s, err := scanSectionRow(m.DB.QueryRowContext(ctx, `
SELECT `+sectionColumns+` FROM room_sections WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND `+sqlNotDeleted,
		tnt, roomID, sectionID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomSection{}, false, nil
	}
	if err != nil {
		return RoomSection{}, false, err
	}
	return s, true, nil
}

func (m *MySQLChannel) ListSections(ctx context.Context, roomID string) ([]RoomSection, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT `+sectionColumns+` FROM room_sections WHERE tenant_id = ? AND room_id = ? AND `+sqlNotDeleted+`
ORDER BY position ASC, created_at_ms ASC, section_id ASC`, scanSectionRow, tnt, roomID)
	if out == nil && err == nil {
		out = []RoomSection{}
	}
	return out, err
}

func (m *MySQLChannel) UpdateSection(ctx context.Context, roomID, sectionID string, patch SectionPatch) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	sets, args := []string{}, []any{}
	if patch.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *patch.Name)
	}
	if patch.Rules != nil {
		sets, args = append(sets, "rules = ?"), append(args, nullIfZero(*patch.Rules))
	}
	if patch.Position != nil {
		sets, args = append(sets, "position = ?"), append(args, *patch.Position)
	}
	if patch.Owners != nil {
		owners, err := sectionOwnersJSON(*patch.Owners)
		if err != nil {
			return err
		}
		sets, args = append(sets, "owners_json = ?"), append(args, owners)
	}
	if patch.JoinPolicy != nil {
		sets, args = append(sets, "join_policy = ?"), append(args, *patch.JoinPolicy)
	}
	if len(sets) == 0 {
		if _, ok, err := m.GetSection(ctx, roomID, sectionID); err != nil || !ok {
			if err == nil {
				err = ErrSectionNotFound
			}
			return err
		}
		return nil
	}
	args = append(args, tnt, roomID, sectionID)
	res, err := m.DB.ExecContext(ctx, `UPDATE room_sections SET `+strings.Join(sets, ", ")+`
WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND `+sqlNotDeleted, args...)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrSectionNotFound)
}

// DeleteSection soft-deletes a section and moves its channels to the top
// level in one transaction.
func (m *MySQLChannel) DeleteSection(ctx context.Context, roomID, sectionID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE room_sections SET deleted_at_ms = ?
WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND `+sqlNotDeleted, atMs, tnt, roomID, sectionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_channels SET section_id = ''
WHERE tenant_id = ? AND room_id = ? AND section_id = ?`, tnt, roomID, sectionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *MySQLChannel) FollowChannel(ctx context.Context, roomID, channelID, userID string, atMs int64, marks FollowMarks) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// The key carries the tenant, so the upsert never touches another
	// tenant's row; a repeat keeps the first joined_at_ms.
	update := "joined_at_ms = joined_at_ms"
	group := ""
	if marks.Group != nil {
		group = *marks.Group
	}
	args := []any{tnt, roomID, channelID, userID, atMs, marks.Starred != nil && *marks.Starred, marks.Hidden != nil && *marks.Hidden, group}
	if marks.Starred != nil {
		update += ", starred = VALUES(starred)"
	}
	if marks.Hidden != nil {
		update += ", hidden = VALUES(hidden)"
	}
	if marks.Group != nil {
		update += ", group_id = VALUES(group_id)"
	}
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO channel_follows (tenant_id, room_id, channel_id, user_id, joined_at_ms, starred, hidden, group_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE `+update, args...)
	return err
}

func (m *MySQLChannel) PutChannelGroup(ctx context.Context, g ChannelGroup) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO channel_groups (tenant_id, room_id, user_id, group_id, name, position, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, tnt, g.RoomID, g.UserID, g.GroupID, g.Name, g.Position, g.CreatedAtMs)
	return dupAs(err, ErrChannelGroupExists)
}

func (m *MySQLChannel) ChannelGroups(ctx context.Context, roomID, userID string) ([]ChannelGroup, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT tenant_id, room_id, user_id, group_id, name, position, created_at_ms FROM channel_groups
WHERE tenant_id = ? AND room_id = ? AND user_id = ?
ORDER BY position ASC, created_at_ms ASC, group_id ASC`, func(sc rowScanner) (ChannelGroup, error) {
		var g ChannelGroup
		err := sc.Scan(&g.TenantID, &g.RoomID, &g.UserID, &g.GroupID, &g.Name, &g.Position, &g.CreatedAtMs)
		return g, err
	}, tnt, roomID, userID)
	if out == nil && err == nil {
		out = []ChannelGroup{}
	}
	return out, err
}

func (m *MySQLChannel) UpdateChannelGroup(ctx context.Context, roomID, userID, groupID string, patch ChannelGroupPatch) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// A no-op patch still answers whether the group exists: the update
	// rewrites the name with itself.
	sets, args := []string{"name = name"}, []any{}
	if patch.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *patch.Name)
	}
	if patch.Position != nil {
		sets, args = append(sets, "position = ?"), append(args, *patch.Position)
	}
	var n int
	if err := m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_groups WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND group_id = ?`,
		tnt, roomID, userID, groupID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrChannelGroupNotFound
	}
	args = append(args, tnt, roomID, userID, groupID)
	_, err = m.DB.ExecContext(ctx, `UPDATE channel_groups SET `+strings.Join(sets, ", ")+`
WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND group_id = ?`, args...)
	return err
}

// DeleteChannelGroup deletes the group and takes the member's channels out
// of it in one transaction.
func (m *MySQLChannel) DeleteChannelGroup(ctx context.Context, roomID, userID, groupID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_groups WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND group_id = ?`,
		tnt, roomID, userID, groupID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE channel_follows SET group_id = ''
WHERE tenant_id = ? AND room_id = ? AND user_id = ? AND group_id = ?`, tnt, roomID, userID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *MySQLChannel) UnfollowChannel(ctx context.Context, roomID, channelID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `DELETE FROM channel_follows WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND user_id = ?`,
		tnt, roomID, channelID, userID)
	return err
}

func (m *MySQLChannel) ChannelFollows(ctx context.Context, roomID, userID string) ([]ChannelFollow, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT tenant_id, room_id, channel_id, user_id, joined_at_ms, starred, hidden, last_posted_at_ms, group_id FROM channel_follows
WHERE tenant_id = ? AND room_id = ? AND user_id = ? ORDER BY channel_id`, func(sc rowScanner) (ChannelFollow, error) {
		var f ChannelFollow
		err := sc.Scan(&f.TenantID, &f.RoomID, &f.ChannelID, &f.UserID, &f.JoinedAtMs, &f.Starred, &f.Hidden, &f.LastPostedAtMs, &f.GroupID)
		return f, err
	}, tnt, roomID, userID)
	if out == nil && err == nil {
		out = []ChannelFollow{}
	}
	return out, err
}

func (m *MySQLChannel) ChannelFollowerCounts(ctx context.Context, roomID string) (map[string]int, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	type count struct {
		channel string
		n       int
	}
	rows, err := queryAll(ctx, m.DB, `
SELECT channel_id, COUNT(*) FROM channel_follows WHERE tenant_id = ? AND room_id = ? GROUP BY channel_id`, func(sc rowScanner) (count, error) {
		var c count
		err := sc.Scan(&c.channel, &c.n)
		return c, err
	}, tnt, roomID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, c := range rows {
		out[c.channel] = c.n
	}
	return out, nil
}

// ClaimChannelPost is one upsert: without slow mode it records the post;
// with it, the row keeps its last post when that is within the gap (the
// IF), and the read-back tells a refusal (last post not this one) apart.
func (m *MySQLChannel) ClaimChannelPost(ctx context.Context, roomID, channelID, userID string, atMs, minGapMs int64) (int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	if minGapMs <= 0 {
		_, err := m.DB.ExecContext(ctx, `
INSERT INTO channel_follows (tenant_id, room_id, channel_id, user_id, joined_at_ms, last_posted_at_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE last_posted_at_ms = GREATEST(last_posted_at_ms, VALUES(last_posted_at_ms))`,
			tnt, roomID, channelID, userID, atMs, atMs)
		return 0, err
	}
	if _, err := m.DB.ExecContext(ctx, `
INSERT INTO channel_follows (tenant_id, room_id, channel_id, user_id, joined_at_ms, last_posted_at_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE last_posted_at_ms = IF(last_posted_at_ms > ?, last_posted_at_ms, VALUES(last_posted_at_ms))`,
		tnt, roomID, channelID, userID, atMs, atMs, atMs-minGapMs); err != nil {
		return 0, err
	}
	var last int64
	if err := m.DB.QueryRowContext(ctx, `
SELECT last_posted_at_ms FROM channel_follows WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND user_id = ?`,
		tnt, roomID, channelID, userID).Scan(&last); err != nil {
		return 0, err
	}
	if last == atMs {
		return 0, nil
	}
	return last + minGapMs - atMs, nil
}
