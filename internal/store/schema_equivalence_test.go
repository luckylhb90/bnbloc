package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// Schema equivalence and migration-lock tests, gated on BNBLOC_TEST_MYSQL_DSN
// (the DSN's user needs CREATE/DROP DATABASE: each test works in throwaway
// databases so it never touches the DSN's own schema):
//
//	BNBLOC_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3306)/bnbloc?parseTime=true' go test ./internal/store -run Schema

// baselineSchema is the control-plane schema as it stood just before the
// migration ledger was introduced (git f373c80^): the oldest shape the
// migrations promise to upgrade. rooms additionally carries the plaza-era
// idx_room_visibility that some live DBs kept after schema.sql dropped it.
const baselineSchema = `
CREATE TABLE rooms (
  room_id        VARCHAR(64) PRIMARY KEY,
  name           VARCHAR(255) NOT NULL,
  description    VARCHAR(500) NOT NULL DEFAULT '',
  visibility     ENUM('shared','private') NOT NULL DEFAULT 'private',
  sync_mode      ENUM('git','mutagen','none') NOT NULL DEFAULT 'git',
  created_by     VARCHAR(128) NOT NULL,
  created_at_ms  BIGINT NOT NULL,
  KEY idx_room_visibility (visibility)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE room_members (
  room_id        VARCHAR(64) NOT NULL,
  user_id        VARCHAR(128) NOT NULL,
  role           ENUM('owner','admin','member','visitor') NOT NULL,
  joined_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (room_id, user_id),
  KEY idx_member_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE room_invites (
  code           VARCHAR(64) PRIMARY KEY,
  room_id        VARCHAR(64) NOT NULL,
  role           ENUM('member','visitor') NOT NULL,
  expires_at_ms  BIGINT NOT NULL,
  capacity       INT NOT NULL,
  redeemed_count INT NOT NULL DEFAULT 0,
  created_by     VARCHAR(128) NOT NULL,
  KEY idx_invite_room (room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE agent_sessions (
  room_id             VARCHAR(64) NOT NULL,
  agent_session_id    VARCHAR(128) NOT NULL,
  provider_session_id VARCHAR(128) NULL,
  owner_user_id       VARCHAR(128) NOT NULL,
  state_json          JSON NOT NULL,
  message_version     BIGINT NOT NULL,
  updated_at_ms       BIGINT NOT NULL,
  PRIMARY KEY (room_id, agent_session_id),
  KEY idx_session_owner (owner_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE agent_messages (
  room_id          VARCHAR(64) NOT NULL,
  agent_session_id VARCHAR(128) NOT NULL,
  seq              BIGINT NOT NULL,
  client_submit_id VARCHAR(128) NULL,
  payload_json     JSON NOT NULL,
  created_at_ms    BIGINT NOT NULL,
  PRIMARY KEY (room_id, agent_session_id, seq),
  UNIQUE KEY uq_client_submit (room_id, agent_session_id, client_submit_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE sync_states (
  room_id           VARCHAR(64) NOT NULL,
  agent_session_id  VARCHAR(128) NOT NULL,
  pending_count     INT NOT NULL DEFAULT 0,
  failed_count      INT NOT NULL DEFAULT 0,
  last_error        TEXT NULL,
  message_cursor    BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, agent_session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE shared_agent_grants (
  grant_id            VARCHAR(64) PRIMARY KEY,
  room_id             VARCHAR(64) NOT NULL,
  target_id           VARCHAR(128) NOT NULL,
  owner_user_id       VARCHAR(128) NOT NULL,
  grantee_user_id     VARCHAR(128) NOT NULL,
  allowed_models_json JSON NOT NULL,
  capabilities_json   JSON NOT NULL,
  run_quota           INT NULL,
  cost_allowance      DECIMAL(18,6) NULL,
  max_concurrent      INT NOT NULL DEFAULT 1,
  reset_at_ms         BIGINT NULL,
  runs_used           INT NOT NULL DEFAULT 0,
  cost_used           DECIMAL(18,6) NOT NULL DEFAULT 0,
  revoked_at_ms       BIGINT NULL,
  expires_at_ms       BIGINT NULL,
  created_at_ms       BIGINT NOT NULL,
  KEY idx_grant_room (room_id),
  KEY idx_grant_grantee (grantee_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE shared_agent_leases (
  lease_id        VARCHAR(64) PRIMARY KEY,
  grant_id        VARCHAR(64) NOT NULL,
  acquired_at_ms  BIGINT NOT NULL,
  released_at_ms  BIGINT NULL,
  KEY idx_lease_grant (grant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE shared_agent_audit (
  audit_id      BIGINT AUTO_INCREMENT PRIMARY KEY,
  room_id       VARCHAR(64) NOT NULL,
  grant_id      VARCHAR(64) NULL,
  actor_user_id VARCHAR(128) NOT NULL,
  owner_user_id VARCHAR(128) NOT NULL,
  action        ENUM('start','resume','settings','turn','release') NOT NULL,
  decision      ENUM('allow','deny') NOT NULL,
  deny_reason   VARCHAR(64) NULL,
  model         VARCHAR(128) NULL,
  at_ms         BIGINT NOT NULL,
  KEY idx_audit_room_time (room_id, at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE devices (
  device_id       VARCHAR(64) PRIMARY KEY,
  account_id      VARCHAR(128) NOT NULL,
  ed25519_pub     VARBINARY(32) NOT NULL,
  enrolled_at_ms  BIGINT NOT NULL,
  revoked_at_ms   BIGINT NULL,
  KEY idx_device_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE room_messages (
  room_id       VARCHAR(64) NOT NULL,
  seq           BIGINT NOT NULL,
  kind          VARCHAR(32) NOT NULL,
  author        VARCHAR(128) NULL,
  client_msg_id VARCHAR(128) NULL,
  payload_json  JSON NOT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  PRIMARY KEY (room_id, seq),
  UNIQUE KEY uq_room_client_msg (room_id, client_msg_id),
  KEY idx_room_messages_time (room_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE room_read_state (
  room_id       VARCHAR(64) NOT NULL,
  user_id       VARCHAR(128) NOT NULL,
  last_read_seq BIGINT NOT NULL DEFAULT 0,
  updated_at_ms BIGINT NOT NULL,
  PRIMARY KEY (room_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT INTO rooms (room_id, name, created_by, created_at_ms) VALUES ('legacy', 'Legacy', 'alice', 1);
INSERT INTO room_members (room_id, user_id, role, joined_at_ms) VALUES ('legacy', 'alice', 'owner', 1);
INSERT INTO room_messages (room_id, seq, kind, author, payload_json, created_at_ms) VALUES ('legacy', 1, 'chat_message', 'alice', '{}', 1);
`

// scratchDB creates a throwaway database on the test server and returns a DSN
// for it; the database is dropped when the test ends.
func scratchDB(t *testing.T, tag string) string {
	t.Helper()
	raw := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL schema tests")
	}
	cfg, err := mysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := fmt.Sprintf("bnbloc_%s_%s", tag, hex.EncodeToString(b))
	admin := cfg.Clone()
	admin.DBName = ""
	adb, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = adb.Close() })
	if _, err := adb.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
		t.Skipf("cannot create scratch database (needs CREATE privilege): %v", err)
	}
	t.Cleanup(func() { _, _ = adb.Exec("DROP DATABASE IF EXISTS " + name) })
	cfg.DBName = name
	return cfg.FormatDSN()
}

// schemaSnapshot renders a database's columns and indexes as sorted lines, so
// two databases compare by content regardless of column order (ALTER ADD
// COLUMN appends, CREATE TABLE places tenant_id first).
func schemaSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	rows, err := db.Query(`SELECT table_name, column_name, column_type, is_nullable, column_default, collation_name
		FROM information_schema.columns WHERE table_schema = DATABASE()`)
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	for rows.Next() {
		var tbl, col, typ, nullable string
		var def, coll sql.NullString
		if err := rows.Scan(&tbl, &col, &typ, &nullable, &def, &coll); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		d := "<none>"
		if def.Valid {
			d = "'" + def.String + "'"
		}
		out = append(out, fmt.Sprintf("column %s.%s %s null=%s default=%s collation=%s", tbl, col, typ, nullable, d, coll.String))
	}
	_ = rows.Close()
	rows, err = db.Query(`SELECT table_name, index_name, non_unique, GROUP_CONCAT(column_name ORDER BY seq_in_index)
		FROM information_schema.statistics WHERE table_schema = DATABASE()
		GROUP BY table_name, index_name, non_unique`)
	if err != nil {
		t.Fatalf("indexes: %v", err)
	}
	for rows.Next() {
		var tbl, idx, cols string
		var nonUnique int
		if err := rows.Scan(&tbl, &idx, &nonUnique, &cols); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		out = append(out, fmt.Sprintf("index %s.%s unique=%v (%s)", tbl, idx, nonUnique == 0, cols))
	}
	_ = rows.Close()
	sort.Strings(out)
	return out
}

// OPS-13 / OPS-14: a database upgraded from the oldest supported baseline must
// end up structurally identical to a fresh one — same columns (type,
// nullability, default) and same indexes (columns, order, uniqueness).
// Upgraded DBs used to keep tenant_id DEFAULT 'default', non-tenant-leading
// secondary indexes, a stale idx_room_visibility, and the original audit
// action enum.
func TestSchemaMigratedEqualsFresh(t *testing.T) {
	freshDSN := scratchDB(t, "fresh")
	migDSN := scratchDB(t, "migrated")

	fresh, err := OpenMySQL(freshDSN)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })

	rawCfg, err := mysql.ParseDSN(migDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	rawCfg.MultiStatements = true
	raw, err := sql.Open("mysql", rawCfg.FormatDSN())
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	if _, err := raw.Exec(baselineSchema); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	_ = raw.Close()
	migrated, err := OpenMySQL(migDSN)
	if err != nil {
		t.Fatalf("upgrade baseline: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	// Re-running is a no-op on both (the ledger and the guards).
	for name, db := range map[string]*sql.DB{"fresh": fresh, "migrated": migrated} {
		if err := runMigrations(db); err != nil {
			t.Fatalf("re-run migrations on %s: %v", name, err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(migrations) {
			t.Fatalf("%s ledger = %d (%v), want %d", name, n, err, len(migrations))
		}
	}

	want, got := schemaSnapshot(t, fresh), schemaSnapshot(t, migrated)
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		onlyFresh, onlyMig := diffLines(want, got)
		t.Fatalf("migrated schema differs from fresh\nonly in fresh:\n  %s\nonly in migrated:\n  %s",
			strings.Join(onlyFresh, "\n  "), strings.Join(onlyMig, "\n  "))
	}

	// The legacy rows survived the upgrade, backfilled to the default tenant.
	var tid string
	if err := migrated.QueryRow(`SELECT tenant_id FROM rooms WHERE room_id = 'legacy'`).Scan(&tid); err != nil || tid != "default" {
		t.Fatalf("legacy room tenant = %q (%v), want default", tid, err)
	}
}

func diffLines(a, b []string) (onlyA, onlyB []string) {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, l := range a {
		inA[l] = true
	}
	for _, l := range b {
		inB[l] = true
		if !inA[l] {
			onlyB = append(onlyB, l)
		}
	}
	for _, l := range a {
		if !inB[l] {
			onlyA = append(onlyA, l)
		}
	}
	return onlyA, onlyB
}

// BE-19: concurrent starters serialize on the migration lock, none fails, and
// the lock is released afterwards (it is session-owned, so taking and
// releasing it on different pooled connections leaked it).
func TestRunMigrationsConcurrentStartersReleaseLock(t *testing.T) {
	dsn := scratchDB(t, "lock")
	db, err := OpenMySQL(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Start from an empty ledger so every starter has real work to race on.
	if _, err := db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("reset ledger: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- runMigrations(db)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent runMigrations: %v", err)
		}
	}
	var free int
	if err := db.QueryRow(`SELECT IS_FREE_LOCK(?)`, migrationLockName).Scan(&free); err != nil || free != 1 {
		t.Fatalf("migration lock free = %d (%v), want 1 (released)", free, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(migrations) {
		t.Fatalf("ledger = %d (%v), want %d", n, err, len(migrations))
	}
}
