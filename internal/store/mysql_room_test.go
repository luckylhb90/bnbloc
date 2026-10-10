package store

import (
	"errors"
	"testing"
)

// MySQL room-store regressions, run in a scratch database (see scratchDB;
// skipped without BNBLOC_TEST_MYSQL_DSN).

// BE-1: rooms' primary key is room_id alone, so the former create upsert let a
// caller in ANY tenant re-"create" an existing room, rename it, and be stamped
// its owner — which IsMemberAnyTenant then honored as cross-tenant access.
func TestMySQLRoomCreateRejectsExistingIDAcrossTenants(t *testing.T) {
	db, err := OpenMySQL(scratchDB(t, "roomcreate"))
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

// BE-24: DeleteRoom removes every room-owned row in one transaction, not just
// the pre-channels child tables.
func TestMySQLDeleteRoomRemovesEveryRoomOwnedRow(t *testing.T) {
	db, err := OpenMySQL(scratchDB(t, "roomdelete"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewMySQLRoom(db)
	ctx := tenantCtx(tenantA)
	for _, id := range []string{"gone", "kept"} {
		if err := s.CreateRoomWithOwnerAndChannel(ctx, Room{RoomID: id, Name: id, CreatedBy: "alice", CreatedAtMs: 1},
			Membership{RoomID: id, UserID: "alice", Role: RoleOwner, JoinedAtMs: 1},
			RoomChannel{ChannelID: "ch-" + id, RoomID: id, Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		seeds := []string{
			`INSERT INTO room_messages (tenant_id, room_id, channel_id, seq, kind, payload_json, created_at_ms) VALUES ('org-a', '` + id + `', 'ch', 1, 'chat_message', '{}', 1)`,
			`INSERT INTO room_read_state (tenant_id, room_id, channel_id, user_id, last_read_seq, updated_at_ms) VALUES ('org-a', '` + id + `', 'ch', 'alice', 1, 1)`,
			`INSERT INTO room_invites (code, tenant_id, room_id, role, expires_at_ms, capacity, created_by) VALUES ('inv-` + id + `', 'org-a', '` + id + `', 'member', 9, 1, 'alice')`,
			`INSERT INTO attachments (id, tenant_id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms) VALUES ('att-` + id + `', 'org-a', '` + id + `', 'alice', 'f', 'text/plain', 1, 'x', 'k', 1)`,
			`INSERT INTO artifacts (artifact_id, tenant_id, room_id, kind, title, created_by, created_at_ms, updated_at_ms) VALUES ('art-` + id + `', 'org-a', '` + id + `', 'doc', 't', 'alice', 1, 1)`,
			`INSERT INTO artifact_versions (tenant_id, artifact_id, version, parent_version, content_hash, content, author_user_id, created_at_ms) VALUES ('org-a', 'art-` + id + `', 1, 0, 'h', 'c', 'alice', 1)`,
			`INSERT INTO artifact_shares (share_id, token_hash, tenant_id, room_id, artifact_id, version, created_by, created_at_ms, expires_at_ms) VALUES ('sh-` + id + `', SHA2('` + id + `', 256), 'org-a', '` + id + `', 'art-` + id + `', 0, 'alice', 1, 9)`,
			`INSERT INTO agent_jobs (job_id, tenant_id, room_id, message_seq, requester_user_id, owner_user_id, core, provider, created_at_ms, updated_at_ms) VALUES ('job-` + id + `', 'org-a', '` + id + `', 1, 'alice', 'alice', 'c', 'p', 1, 1)`,
			`INSERT INTO user_agent_prefs (tenant_id, user_id, room_id, channel_id, updated_at_ms) VALUES ('org-a', 'alice', '` + id + `', '', 1)`,
			`INSERT INTO shared_agent_grants (grant_id, tenant_id, room_id, target_id, owner_user_id, grantee_user_id, allowed_models_json, capabilities_json, created_at_ms) VALUES ('g-` + id + `', 'org-a', '` + id + `', 't', 'alice', 'bob', '[]', '[]', 1)`,
			`INSERT INTO shared_agent_leases (lease_id, tenant_id, grant_id, acquired_at_ms) VALUES ('l-` + id + `', 'org-a', 'g-` + id + `', 1)`,
		}
		for _, q := range seeds {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("seed %s: %v\n%s", id, err, q)
			}
		}
	}
	if err := s.DeleteRoom(ctx, "gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	counts := map[string]string{
		"room_members":        `SELECT COUNT(*) FROM room_members WHERE room_id = ?`,
		"room_channels":       `SELECT COUNT(*) FROM room_channels WHERE room_id = ?`,
		"room_messages":       `SELECT COUNT(*) FROM room_messages WHERE room_id = ?`,
		"room_read_state":     `SELECT COUNT(*) FROM room_read_state WHERE room_id = ?`,
		"room_invites":        `SELECT COUNT(*) FROM room_invites WHERE room_id = ?`,
		"attachments":         `SELECT COUNT(*) FROM attachments WHERE room_id = ?`,
		"artifacts":           `SELECT COUNT(*) FROM artifacts WHERE room_id = ?`,
		"artifact_versions":   `SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = CONCAT('art-', ?)`,
		"artifact_shares":     `SELECT COUNT(*) FROM artifact_shares WHERE room_id = ?`,
		"agent_jobs":          `SELECT COUNT(*) FROM agent_jobs WHERE room_id = ?`,
		"user_agent_prefs":    `SELECT COUNT(*) FROM user_agent_prefs WHERE room_id = ?`,
		"shared_agent_grants": `SELECT COUNT(*) FROM shared_agent_grants WHERE room_id = ?`,
		"shared_agent_leases": `SELECT COUNT(*) FROM shared_agent_leases WHERE grant_id = CONCAT('g-', ?)`,
	}
	for table, q := range counts {
		var gone, kept int
		if err := db.QueryRow(q, "gone").Scan(&gone); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if err := db.QueryRow(q, "kept").Scan(&kept); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if gone != 0 {
			t.Errorf("%s: %d rows remain for the deleted room", table, gone)
		}
		if kept == 0 {
			t.Errorf("%s: the other room's rows were deleted too", table)
		}
	}
	if _, err := s.GetRoom(ctx, "gone"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("GetRoom after delete = %v, want not found", err)
	}
	if err := s.CreateRoom(ctx, Room{RoomID: "gone", Name: "again"}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("re-create of a deleted id = %v, want ErrRoomExists", err)
	}
}
