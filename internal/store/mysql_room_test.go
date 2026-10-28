package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// MySQL room-store regressions, run in a scratch database (see testmysql.Fresh;
// skipped without BNBLOC_TEST_MYSQL_DSN).

// BE-24: DeleteRoom removes every room-owned row in one transaction. Every
// roomOwnedTables entry is seeded for two rooms and counted with the entry's
// own predicate: the deleted room keeps nothing, the other room loses nothing.
func TestMySQLDeleteRoomRemovesEveryRoomOwnedRow(t *testing.T) {
	db, err := OpenMySQL(testmysql.Fresh(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewMySQLRoom(NewDB(db, nil, 0, 0))
	ctx := tenantCtx(tenantA)
	seeds := map[string]string{
		"room_messages":               `INSERT INTO room_messages (tenant_id, room_id, channel_id, seq, kind, payload_json, created_at_ms) VALUES ('org-a', '{id}', 'ch', 1, 'chat_message', '{}', 1)`,
		"room_read_state":             `INSERT INTO room_read_state (tenant_id, room_id, channel_id, user_id, last_read_seq, updated_at_ms) VALUES ('org-a', '{id}', 'ch', 'alice', 1, 1)`,
		"room_sections":               `INSERT INTO room_sections (section_id, tenant_id, room_id, name, created_by, created_at_ms) VALUES ('sec-{id}', 'org-a', '{id}', 'Design', 'alice', 1)`,
		"channel_follows":             `INSERT INTO channel_follows (tenant_id, room_id, channel_id, user_id, joined_at_ms) VALUES ('org-a', '{id}', 'ch', 'alice', 1)`,
		"section_members":             `INSERT INTO section_members (tenant_id, room_id, section_id, user_id, added_by, joined_at_ms) VALUES ('org-a', '{id}', 'sec-{id}', 'alice', 'alice', 1)`,
		"section_join_requests":       `INSERT INTO section_join_requests (tenant_id, room_id, section_id, user_id, requested_at_ms) VALUES ('org-a', '{id}', 'sec-{id}', 'bob', 1)`,
		"channel_groups":              `INSERT INTO channel_groups (tenant_id, room_id, user_id, group_id, name, created_at_ms) VALUES ('org-a', '{id}', 'alice', 'grp-{id}', 'Mine', 1)`,
		"room_invites":                `INSERT INTO room_invites (code, tenant_id, room_id, role, expires_at_ms, capacity, created_by) VALUES ('inv-{id}', 'org-a', '{id}', 'member', 9, 1, 'alice')`,
		"message_tasks":               `INSERT INTO message_tasks (tenant_id, room_id, seq, status, title, created_by, created_at_ms) VALUES ('org-a', '{id}', 1, 'open', 'the message text', 'alice', 1)`,
		"agent_sessions":              `INSERT INTO agent_sessions (tenant_id, room_id, agent_session_id, message_version) VALUES ('org-a', '{id}', 's', 1)`,
		"agent_messages":              `INSERT INTO agent_messages (tenant_id, room_id, agent_session_id, seq, payload_json, created_at_ms) VALUES ('org-a', '{id}', 's', 1, '{}', 1)`,
		"sync_states":                 `INSERT INTO sync_states (tenant_id, room_id, agent_session_id, message_cursor) VALUES ('org-a', '{id}', 's', 1)`,
		"attachments":                 `INSERT INTO attachments (id, tenant_id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms) VALUES ('att-{id}', 'org-a', '{id}', 'alice', 'f', 'text/plain', 1, 'x', 'k', 1)`,
		"artifacts":                   `INSERT INTO artifacts (artifact_id, tenant_id, room_id, kind, title, created_by, created_at_ms, updated_at_ms) VALUES ('art-{id}', 'org-a', '{id}', 'doc', 't', 'alice', 1, 1)`,
		"artifact_versions":           `INSERT INTO artifact_versions (tenant_id, artifact_id, version, parent_version, content_hash, content, author_user_id, created_at_ms) VALUES ('org-a', 'art-{id}', 1, 0, 'h', 'c', 'alice', 1)`,
		"artifact_shares":             `INSERT INTO artifact_shares (share_id, token_hash, tenant_id, room_id, artifact_id, version, created_by, created_at_ms, expires_at_ms) VALUES ('sh-{id}', SHA2('{id}', 256), 'org-a', '{id}', 'art-{id}', 0, 'alice', 1, 9)`,
		"agent_jobs":                  `INSERT INTO agent_jobs (job_id, tenant_id, room_id, message_seq, requester_user_id, owner_user_id, core, provider, created_at_ms, updated_at_ms) VALUES ('job-{id}', 'org-a', '{id}', 1, 'alice', 'alice', 'c', 'p', 1, 1)`,
		"user_agent_prefs":            `INSERT INTO user_agent_prefs (tenant_id, user_id, room_id, channel_id, updated_at_ms) VALUES ('org-a', 'alice', '{id}', '', 1)`,
		"shared_agent_grants":         `INSERT INTO shared_agent_grants (grant_id, tenant_id, room_id, target_id, owner_user_id, grantee_user_id, allowed_models_json, capabilities_json, created_at_ms) VALUES ('g-{id}', 'org-a', '{id}', 't', 'alice', 'bob', '[]', '[]', 1)`,
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
		"app_files":                   `INSERT INTO app_files (app_id, file_id, tenant_id, room_id, by_key, public, name_sealed, author_sealed, content_type, size_bytes, blob_key, created_at_ms) VALUES ('app-{id}', 'f1', 'org-a', '{id}', 'p', 0, 'n', 'a', 'image/png', 1, 'sha256/x', 1)`,
		"room_groups":                 `INSERT INTO room_groups (tenant_id, room_id, name, notifier, address, connected_by, created_at_ms) VALUES ('org-a', '{id}', 'g', 'n', 'a', 'maya', 1)`,
		"room_group_codes":            `INSERT INTO room_group_codes (code, tenant_id, room_id, name, created_by, expires_at_ms) VALUES ('c-{id}', 'org-a', '{id}', 'g', 'maya', 1)`,
		"room_source_grants":          `INSERT INTO room_source_grants (tenant_id, room_id, source_name, component_id, component_name, component_version, purpose, alias_key, asked_by, asked_at_ms) VALUES ('org-a', '{id}', 'runs', 'cc', 'review', '1.0.0', 'p', 'k', 'maya', 1)`,
		"room_source_opt_ins":         `INSERT INTO room_source_opt_ins (tenant_id, room_id, source_name, opted_by, opted_at_ms) VALUES ('org-a', '{id}', 'runs', 'maya', 1)`,
		"room_notes":                  `INSERT INTO room_notes (tenant_id, room_id, note_id, type, title, body, status, author, revision, created_at_ms, updated_at_ms) VALUES ('org-a', '{id}', 'n1', 'memory', 't', 'b', 'active', 'maya', 1, 1, 1)`,
		"room_note_revisions":         `INSERT INTO room_note_revisions (tenant_id, room_id, note_id, revision, type, title, body, status, changed_by, at_ms) VALUES ('org-a', '{id}', 'n1', 1, 'memory', 't', 'b', 'active', 'maya', 1)`,
		"app_keys":                    `INSERT INTO app_keys (app_id, tenant_id, room_id, wrapped_key, created_at_ms) VALUES ('app-{id}', 'org-a', '{id}', 'wk', 1)`,
		// More documents than one shred batch (appShredBatch), so the
		// batched delete loops.
		"app_docs": `SET SESSION cte_max_recursion_depth = 5000;
			INSERT INTO app_docs (app_id, collection, doc_id, tenant_id, room_id, by_key, public, author_sealed, version, body_sealed, created_at_ms, updated_at_ms)
			WITH RECURSIVE seq (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 2005)
			SELECT 'app-{id}', 'c', CONCAT('d', n), 'org-a', '{id}', 'p', 0, 'a', 1, 'b', 1, 1 FROM seq`,
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
	// app_docs is counted through its app's key row, which is gone too:
	// count the documents by their own room column as well.
	var docs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_docs WHERE tenant_id = ? AND room_id = ?`, tenantA, "gone").Scan(&docs); err != nil || docs != 0 {
		t.Errorf("app_docs: %d rows remain for the deleted room (%v)", docs, err)
	}
	if _, err := s.GetRoom(ctx, "gone"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("GetRoom after delete = %v, want not found", err)
	}
	if err := s.CreateRoom(ctx, Room{RoomID: "gone", Name: "again"}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("re-create of a deleted id = %v, want ErrRoomExists", err)
	}
}
