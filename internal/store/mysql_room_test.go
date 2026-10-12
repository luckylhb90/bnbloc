package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// MySQL room-store regressions, run in a scratch database (see testmysql.Fresh;
// skipped without BNBLOC_TEST_MYSQL_DSN).

// BE-1: rooms' primary key is room_id alone, so the former create upsert let a
// caller in ANY tenant re-"create" an existing room, rename it, and be stamped
// its owner — which IsMemberAnyTenant then honored as cross-tenant access.
func TestMySQLRoomCreateRejectsExistingIDAcrossTenants(t *testing.T) {
	db, err := OpenMySQL(testmysql.Fresh(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewMySQLRoom(db)
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)

	if err := s.CreateRoomWithOwnerAndChannel(ca, Room{RoomID: "r1", Name: "Alpha", CreatedBy: "alice", CreatedAtMs: 1},
		Membership{RoomID: "r1", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1},
		RoomChannel{ChannelID: "ch-a", RoomID: "r1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	err = s.CreateRoomWithOwnerAndChannel(cb, Room{RoomID: "r1", Name: "hijack", CreatedBy: "mallory", CreatedAtMs: 2},
		Membership{RoomID: "r1", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2},
		RoomChannel{ChannelID: "ch-m", RoomID: "r1", Name: "general", IsDefault: true, CreatedBy: "mallory", CreatedAtMs: 2})
	if !errors.Is(err, ErrRoomExists) {
		t.Fatalf("cross-tenant create of existing id = %v, want ErrRoomExists", err)
	}
	if err := s.CreateRoomWithOwner(ca, Room{RoomID: "r1", Name: "again", CreatedBy: "mallory", CreatedAtMs: 2},
		Membership{RoomID: "r1", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("same-tenant create of existing id = %v, want ErrRoomExists", err)
	}
	if err := s.CreateRoom(ca, Room{RoomID: "r1", Name: "x"}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("CreateRoom existing id = %v, want ErrRoomExists", err)
	}
	if got, err := s.GetRoom(ca, "r1"); err != nil || got.Name != "Alpha" {
		t.Fatalf("room after failed creates = %+v, %v; want Alpha unchanged", got, err)
	}
	if memberAnyTenant(s, ca, "r1", "mallory") {
		t.Fatal("a failed create must not leave the caller a member")
	}

	// A membership row filed under a foreign tenant (as the old upsert left
	// behind) grants nothing.
	if _, err := db.Exec(`INSERT INTO room_members (tenant_id, room_id, user_id, role, joined_at_ms) VALUES (?, 'r1', 'stray', 'owner', 3)`, tenantB); err != nil {
		t.Fatalf("seed stray: %v", err)
	}
	if memberAnyTenant(s, cb, "r1", "stray") {
		t.Fatal("foreign-tenant membership must not count")
	}
	if got := s.RoomsForUserAllTenants(cb, "stray"); len(got) != 0 {
		t.Fatalf("stray all-tenant rooms = %v, want none", got)
	}
	if !memberAnyTenant(s, cb, "r1", "alice") {
		t.Fatal("the real member resolves from any tenant context")
	}
}

// BE-24: DeleteRoom removes every room-owned row in one transaction. Every
// roomOwnedTables entry is seeded for two rooms and counted with the entry's
// own predicate: the deleted room keeps nothing, the other room loses nothing.
func TestMySQLDeleteRoomRemovesEveryRoomOwnedRow(t *testing.T) {
	db, err := OpenMySQL(testmysql.Fresh(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewMySQLRoom(db)
	ctx := tenantCtx(tenantA)
	seeds := map[string]string{
		"room_messages":               `INSERT INTO room_messages (tenant_id, room_id, channel_id, seq, kind, payload_json, created_at_ms) VALUES ('org-a', '{id}', 'ch', 1, 'chat_message', '{}', 1)`,
		"room_read_state":             `INSERT INTO room_read_state (tenant_id, room_id, channel_id, user_id, last_read_seq, updated_at_ms) VALUES ('org-a', '{id}', 'ch', 'alice', 1, 1)`,
		"room_invites":                `INSERT INTO room_invites (code, tenant_id, room_id, role, expires_at_ms, capacity, created_by) VALUES ('inv-{id}', 'org-a', '{id}', 'member', 9, 1, 'alice')`,
		"message_tasks":               `INSERT INTO message_tasks (tenant_id, room_id, seq, status, title, created_by, created_at_ms) VALUES ('org-a', '{id}', 1, 'open', 'the message text', 'alice', 1)`,
		"agent_sessions":              `INSERT INTO agent_sessions (tenant_id, room_id, agent_session_id, owner_user_id, state_json, message_version, updated_at_ms) VALUES ('org-a', '{id}', 's', 'alice', '{}', 1, 1)`,
		"agent_messages":              `INSERT INTO agent_messages (tenant_id, room_id, agent_session_id, seq, payload_json, created_at_ms) VALUES ('org-a', '{id}', 's', 1, '{}', 1)`,
		"sync_states":                 `INSERT INTO sync_states (tenant_id, room_id, agent_session_id, message_cursor) VALUES ('org-a', '{id}', 's', 1)`,
		"attachments":                 `INSERT INTO attachments (id, tenant_id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms) VALUES ('att-{id}', 'org-a', '{id}', 'alice', 'f', 'text/plain', 1, 'x', 'k', 1)`,
		"artifacts":                   `INSERT INTO artifacts (artifact_id, tenant_id, room_id, kind, title, created_by, created_at_ms, updated_at_ms) VALUES ('art-{id}', 'org-a', '{id}', 'doc', 't', 'alice', 1, 1)`,
		"artifact_versions":           `INSERT INTO artifact_versions (tenant_id, artifact_id, version, parent_version, content_hash, content, author_user_id, created_at_ms) VALUES ('org-a', 'art-{id}', 1, 0, 'h', 'c', 'alice', 1)`,
		"artifact_shares":             `INSERT INTO artifact_shares (share_id, token_hash, tenant_id, room_id, artifact_id, version, created_by, created_at_ms, expires_at_ms) VALUES ('sh-{id}', SHA2('{id}', 256), 'org-a', '{id}', 'art-{id}', 0, 'alice', 1, 9)`,
		"agent_jobs":                  `INSERT INTO agent_jobs (job_id, tenant_id, room_id, message_seq, requester_user_id, owner_user_id, core, provider, created_at_ms, updated_at_ms) VALUES ('job-{id}', 'org-a', '{id}', 1, 'alice', 'alice', 'c', 'p', 1, 1)`,
		"user_agent_prefs":            `INSERT INTO user_agent_prefs (tenant_id, user_id, room_id, channel_id, updated_at_ms) VALUES ('org-a', 'alice', '{id}', '', 1)`,
		"shared_agent_grants":         `INSERT INTO shared_agent_grants (grant_id, tenant_id, room_id, target_id, owner_user_id, grantee_user_id, allowed_models_json, capabilities_json, created_at_ms) VALUES ('g-{id}', 'org-a', '{id}', 't', 'alice', 'bob', '[]', '[]', 1)`,
		"shared_agent_leases":         `INSERT INTO shared_agent_leases (lease_id, tenant_id, grant_id, acquired_at_ms) VALUES ('l-{id}', 'org-a', 'g-{id}', 1)`,
		"shared_agent_audit":          `INSERT INTO shared_agent_audit (tenant_id, room_id, actor_user_id, owner_user_id, action, decision, at_ms) VALUES ('org-a', '{id}', 'bob', 'alice', 'turn', 'allow', 1)`,
		"shared_agent_quota_requests": `INSERT INTO shared_agent_quota_requests (request_id, tenant_id, room_id, owner_user_id, requester_user_id, provider, created_at_ms) VALUES ('q-{id}', 'org-a', '{id}', 'alice', 'bob', 'p', 1)`,
		"room_imports":                `INSERT INTO room_imports (tenant_id, room_id, source_room_id, source_exported_at_ms, imported_by, imported_at_ms, suggested_members_json) VALUES ('org-a', '{id}', 'src', 1, 'alice', 1, '[]')`,
		"room_schedules":              `INSERT INTO room_schedules (schedule_id, tenant_id, room_id, created_by, text, recipient_json, recurrence_json, timezone, created_at_ms, updated_at_ms) VALUES ('sch-{id}', 'org-a', '{id}', 'alice', 'x', '{}', '{}', 'UTC', 1, 1)`,
		"notification_reply_tokens":   `INSERT INTO notification_reply_tokens (token_hash, tenant_id, notifier, event_id, room_id, user_id, created_at_ms, expires_at_ms) VALUES (SHA2('tok-{id}', 256), 'org-a', 'n', 'e', '{id}', 'alice', 1, 9)`,
		"notification_reply_refs":     `INSERT INTO notification_reply_refs (notifier, ref_id, recipient, token_hash, created_at_ms, expires_at_ms) VALUES ('n', 'ref-{id}', 'alice', SHA2('tok-{id}', 256), 1, 9)`,
		"content_drafts":              `INSERT INTO content_drafts (id, tenant_id, room_id, job_id, requester, author, kind, name, version, manifest_json, bundle_blob_key, sha256, size_bytes, created_at_ms, expires_at_ms) VALUES ('d-{id}', 'org-a', '{id}', 'j', 'alice', 'alice', 'expert', 'n', '1.0.0', '{}', 'k', 'x', 1, 1, 9)`,
		"content_room_shares":         `INSERT INTO content_room_shares (room_id, component_id, tenant_id, version, shared_by, created_at_ms, updated_at_ms) VALUES ('{id}', 'cc', 'org-a', '1.0.0', 'alice', 1, 1)`,
		"content_component_uses":      `INSERT INTO content_component_uses (scope_kind, scope_id, component_id, tenant_id, kind, created_by, created_at_ms) VALUES ('room', '{id}', 'cc', 'org-a', 'expert', 'alice', 1)`,
		"org_sync_default_rooms":      `INSERT INTO org_sync_default_rooms (user_id, room_id, granted_at_ms) VALUES ('alice', '{id}', 1)`,
	}
	// Rows CreateRoomWithOwnerAndChannel already writes.
	created := map[string]bool{"room_members": true, "room_channels": true}
	for _, rt := range roomOwnedTables {
		if _, ok := seeds[rt.table]; !ok && !created[rt.table] {
			t.Fatalf("roomOwnedTables lists %s but this test seeds no row for it; add one", rt.table)
		}
	}
	for _, id := range []string{"gone", "kept"} {
		if err := s.CreateRoomWithOwnerAndChannel(ctx, Room{RoomID: id, Name: id, CreatedBy: "alice", CreatedAtMs: 1},
			Membership{RoomID: id, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1},
			RoomChannel{ChannelID: "ch-" + id, RoomID: id, Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		for table, q := range seeds {
			if _, err := db.Exec(strings.ReplaceAll(q, "{id}", id)); err != nil {
				t.Fatalf("seed %s for %s: %v", table, id, err)
			}
		}
	}
	if err := s.DeleteRoom(ctx, "gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, rt := range roomOwnedTables {
		count := func(room string) int {
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM `+rt.table+` WHERE `+rt.where, rt.args(tenantA, room)...).Scan(&n); err != nil {
				t.Fatalf("count %s: %v", rt.table, err)
			}
			return n
		}
		if n := count("gone"); n != 0 {
			t.Errorf("%s: %d rows remain for the deleted room", rt.table, n)
		}
		if count("kept") == 0 {
			t.Errorf("%s: the other room's rows were deleted too", rt.table)
		}
	}
	if _, err := s.GetRoom(ctx, "gone"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("GetRoom after delete = %v, want not found", err)
	}
	if err := s.CreateRoom(ctx, Room{RoomID: "gone", Name: "again"}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("re-create of a deleted id = %v, want ErrRoomExists", err)
	}
}
