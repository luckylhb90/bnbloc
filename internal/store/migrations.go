package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// migration is one ordered, idempotent schema change recorded in the
// schema_migrations ledger. run must be safe to invoke on a fresh DB (where the
// CREATE TABLE block already produced the target column/index), a previously
// live-ALTER'd DB, and an old DB missing the change entirely.
type migration struct {
	id  string
	run func(ctx context.Context, db sqlConn) error
}

// migrations is the ordered list applied after the CREATE TABLE block. IDs are
// permanent: never rename or reorder an already-shipped entry.
var migrations = []migration{
	{
		id: "2026_09_rooms_deleted_at_ms",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "rooms", "deleted_at_ms", "BIGINT NULL")
		},
	},
	{
		id: "2026_09_grants_expires_at_ms",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "shared_agent_grants", "expires_at_ms", "BIGINT NULL")
		},
	},
	{
		id: "2026_09_room_messages_client_msg_id",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "room_messages", "client_msg_id", "VARCHAR(128) NULL"); err != nil {
				return err
			}
			return addIndexIfMissing(ctx, db, "room_messages", "uq_room_client_msg",
				"ADD UNIQUE KEY uq_room_client_msg (room_id, client_msg_id)")
		},
	},
	{
		id: "2026_09_room_messages_deleted_at_ms",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "room_messages", "deleted_at_ms", "BIGINT NULL")
		},
	},
	{
		id: "2026_09_room_messages_edited_at_ms",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "room_messages", "edited_at_ms", "BIGINT NULL")
		},
	},
	{
		id: "2026_09_drop_idx_room_messages_time",
		run: func(ctx context.Context, db sqlConn) error {
			// Duplicate of the PRIMARY KEY (room_id, seq); drop it if present.
			return dropIndexIfExists(ctx, db, "room_messages", "idx_room_messages_time")
		},
	},
	{
		id: "2026_09_rooms_active_created_index",
		run: func(ctx context.Context, db sqlConn) error {
			return addIndexIfMissing(ctx, db, "rooms", "idx_rooms_active_created",
				"ADD KEY idx_rooms_active_created (deleted_at_ms, created_at_ms)")
		},
	},
	{
		id: "2026_09_lease_grant_composite",
		run: func(ctx context.Context, db sqlConn) error {
			// Replace the single-column idx_lease_grant with the composite
			// (grant_id, released_at_ms). Drop first so an old single-column
			// index is upgraded; the create is then guarded by name.
			cols, err := indexColumns(ctx, db, "shared_agent_leases", "idx_lease_grant")
			if err != nil {
				return err
			}
			if len(cols) != 2 {
				if err := dropIndexIfExists(ctx, db, "shared_agent_leases", "idx_lease_grant"); err != nil {
					return err
				}
			}
			return addIndexIfMissing(ctx, db, "shared_agent_leases", "idx_lease_grant",
				"ADD KEY idx_lease_grant (grant_id, released_at_ms)")
		},
	},
	{
		// Multi-tenancy: add tenant_id to every domain table and backfill the
		// pre-tenancy rows to the 'default' tenant so the upgrade is
		// non-breaking. New rows are written with a real tenant by the app.
		id: "2026_10_multitenancy_tenant_id",
		run: func(ctx context.Context, db sqlConn) error {
			// addColumnIfMissing runs "ALTER TABLE <t> ADD COLUMN tenant_id <ddl>",
			// so the ddl is the column TYPE only (not the name). NOT NULL DEFAULT
			// backfills existing rows to the default tenant in one statement.
			const ddl = "VARCHAR(64) NOT NULL DEFAULT 'default'"
			tables := []string{
				"rooms", "room_members", "room_invites",
				"agent_sessions", "agent_messages", "sync_states",
				"shared_agent_grants", "shared_agent_leases", "shared_agent_audit",
				"room_messages", "room_read_state",
			}
			for _, t := range tables {
				if err := addColumnIfMissing(ctx, db, t, "tenant_id", ddl); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Tenant registry tables. CREATE TABLE IF NOT EXISTS on a fresh DB
		// already made them; this covers an upgraded DB that predates them.
		id: "2026_10_multitenancy_tenant_tables",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenants (
  tenant_id     VARCHAR(64) PRIMARY KEY,
  name          VARCHAR(255) NOT NULL,
  status        ENUM('active','suspended') NOT NULL DEFAULT 'active',
  sso_issuer    VARCHAR(255) NOT NULL DEFAULT '',
  created_at_ms BIGINT NOT NULL,
  KEY idx_tenant_issuer (sso_issuer)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenant_members (
  tenant_id    VARCHAR(64) NOT NULL,
  user_id      VARCHAR(128) NOT NULL,
  role         ENUM('org_admin','member','viewer') NOT NULL DEFAULT 'member',
  joined_at_ms BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, user_id),
  KEY idx_tenant_member_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
				return err
			}
			// Ensure the 'default' tenant row exists for backfilled data.
			_, err := db.ExecContext(ctx, `INSERT IGNORE INTO tenants (tenant_id, name, status, created_at_ms) VALUES ('default','Default',?,?)`, "active", 0)
			return err
		},
	},
	{
		// Org-level quota ceilings on the tenant registry (0 = unlimited).
		id: "2026_10_multitenancy_tenant_quota",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "tenants", "run_quota", "INT NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			return addColumnIfMissing(ctx, db, "tenants", "cost_allowance", "DECIMAL(18,6) NOT NULL DEFAULT 0")
		},
	},
	{
		// Membership join requests: a user asks to join a tenant; an admin
		// approves (creating a membership) or rejects. One row per (tenant,user).
		id: "2026_10_multitenancy_join_requests",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenant_join_requests (
  tenant_id     VARCHAR(64) NOT NULL,
  user_id       VARCHAR(128) NOT NULL,
  message       VARCHAR(500) NOT NULL DEFAULT '',
  status        ENUM('pending','approved','rejected') NOT NULL DEFAULT 'pending',
  created_at_ms BIGINT NOT NULL,
  decided_at_ms BIGINT NOT NULL DEFAULT 0,
  decided_by    VARCHAR(128) NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, user_id),
  KEY idx_join_req_status (tenant_id, status),
  KEY idx_join_req_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		// Channels partition a room's message stream by topic. The message
		// channel_id columns default to '' so pre-existing rows stay addressable
		// until the room service backfills a default channel.
		id: "2026_10_room_channels",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS room_channels (
  channel_id    VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(255) NOT NULL,
  is_default    TINYINT(1) NOT NULL DEFAULT 0,
  created_by    VARCHAR(128) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  KEY idx_channel_room (tenant_id, room_id, deleted_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "room_messages", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "agent_messages", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			return addIndexIfMissing(ctx, db, "room_messages", "idx_room_channel_seq", "ADD KEY idx_room_channel_seq (room_id, channel_id, seq)")
		},
	},
	{
		// Per-channel read cursors: a user's last-read sequence is tracked per
		// (room, channel), so the read-state identity gains channel_id. Existing
		// rows keep channel_id '' (the empty/default channel), which is exactly
		// where pre-channel reads belong.
		id: "2026_10_room_channels_read_state",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "room_read_state", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			// The cursor row must be keyed by (room, channel, user) for the
			// MarkRead upsert to keep one cursor per channel. Swap the PK when
			// it does not already include channel_id (fresh DBs get it from
			// schema.sql's CREATE TABLE, so this only alters existing DBs).
			cols, err := indexColumns(ctx, db, "room_read_state", "PRIMARY")
			if err != nil {
				return err
			}
			for _, c := range cols {
				if c == "channel_id" {
					return nil
				}
			}
			_, err = db.ExecContext(ctx, `ALTER TABLE room_read_state DROP PRIMARY KEY, ADD PRIMARY KEY (room_id, channel_id, user_id)`)
			return err
		},
	},
	{
		// The audit trail records which channel a borrow turn was driven from.
		id: "2026_10_room_channels_audit",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "shared_agent_audit", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''")
		},
	},
	{
		// Bridge v2 durable job queue and bridge capability registry.
		id: "2026_10_bridge_v2_jobs",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_jobs (
  job_id              VARCHAR(64) PRIMARY KEY,
  tenant_id           VARCHAR(64) NOT NULL,
  room_id             VARCHAR(64) NOT NULL,
  channel_id          VARCHAR(64) NOT NULL DEFAULT '',
  message_seq         BIGINT NOT NULL,
  client_msg_id       VARCHAR(128) NOT NULL DEFAULT '',
  requester_user_id   VARCHAR(128) NOT NULL,
  owner_user_id       VARCHAR(128) NOT NULL,
  core                VARCHAR(64) NOT NULL,
  provider            VARCHAR(128) NOT NULL,
  target_id           VARCHAR(128) NOT NULL DEFAULT '',
  message_text       TEXT NULL,
  model              VARCHAR(128) NOT NULL DEFAULT '',
  reasoning_effort   VARCHAR(64) NOT NULL DEFAULT '',
  status              ENUM('queued','claimed','running','reflowing','replied','failed','timed_out','cancelled') NOT NULL DEFAULT 'queued',
  failure_reason      VARCHAR(64) NOT NULL DEFAULT '',
  attempt             INT NOT NULL DEFAULT 0,
  lease_bridge_id     VARCHAR(128) NOT NULL DEFAULT '',
  lease_expires_at_ms BIGINT NOT NULL DEFAULT 0,
  created_at_ms       BIGINT NOT NULL,
  updated_at_ms       BIGINT NOT NULL,
  started_at_ms       BIGINT NOT NULL DEFAULT 0,
  finished_at_ms      BIGINT NOT NULL DEFAULT 0,
  diagnostics_json    JSON NULL,
  UNIQUE KEY uq_agent_job_origin (tenant_id, room_id, message_seq, owner_user_id, core, provider),
  KEY idx_agent_jobs_claim (tenant_id, owner_user_id, status, lease_expires_at_ms),
  KEY idx_agent_jobs_room (tenant_id, room_id, channel_id, updated_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS bridge_heartbeats (
  bridge_id       VARCHAR(128) PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  owner_user_id   VARCHAR(128) NOT NULL,
  app_instance_id VARCHAR(128) NOT NULL DEFAULT '',
  version         VARCHAR(64) NOT NULL DEFAULT '',
  status          ENUM('online','offline','draining') NOT NULL DEFAULT 'online',
  serve_all_owners TINYINT(1) NOT NULL DEFAULT 0,
  last_seen_at_ms BIGINT NOT NULL,
  metadata_json   JSON NULL,
  KEY idx_bridge_owner (tenant_id, owner_user_id, last_seen_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS bridge_agents (
  bridge_id          VARCHAR(128) NOT NULL,
  tenant_id          VARCHAR(64) NOT NULL,
  owner_user_id      VARCHAR(128) NOT NULL,
  core               VARCHAR(64) NOT NULL,
  provider           VARCHAR(128) NOT NULL,
  target_id          VARCHAR(128) NOT NULL,
  model_options_json JSON NULL,
  disabled           TINYINT(1) NOT NULL DEFAULT 0,
  last_seen_at_ms    BIGINT NOT NULL,
  PRIMARY KEY (bridge_id, core, provider),
  KEY idx_bridge_agents_lookup (tenant_id, owner_user_id, core, provider, disabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "message_text", "TEXT NULL"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "model", "VARCHAR(128) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			return addColumnIfMissing(ctx, db, "agent_jobs", "reasoning_effort", "VARCHAR(64) NOT NULL DEFAULT ''")
		},
	},
	{
		// Bridge workers need the original chat prompt and per-turn model options
		// in the durable claim payload; earlier queue schema stored routing only.
		id: "2026_10_bridge_v2_job_payload",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "message_text", "TEXT NULL"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "model", "VARCHAR(128) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			return addColumnIfMissing(ctx, db, "agent_jobs", "reasoning_effort", "VARCHAR(64) NOT NULL DEFAULT ''")
		},
	},
	{
		// Early queue rows copied the client-supplied chat timestamp, which could be
		// zero. Queue timeouts are computed from created_at_ms, so an epoch value
		// reads as "queued forever" and would be swept immediately. Repair those
		// rows to their last-update time.
		id: "2026_10_bridge_v2_job_created_at_repair",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `UPDATE agent_jobs SET created_at_ms = IF(updated_at_ms > 0, updated_at_ms, ?) WHERE created_at_ms <= 0`, time.Now().UnixMilli())
			return err
		},
	},
	{
		// bridge_agents keyed only (bridge_id, core, provider), but one bridge
		// advertises several targets per core/provider (codex, claude-code and
		// opencode all run on one multi-harness core). Every heartbeat carrying more than
		// one such target failed with a duplicate-key error, so the bridge never
		// registered its capabilities and its jobs were never claimable.
		id: "2026_10_bridge_agents_target_key",
		run: func(ctx context.Context, db sqlConn) error {
			var pk string
			// Re-keying is only needed where the old primary key is still in place.
			row := db.QueryRowContext(ctx, `SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bridge_agents' AND INDEX_NAME = 'PRIMARY'`)
			if err := row.Scan(&pk); err != nil {
				return nil // table absent on a fresh database: schema.sql already has the right key
			}
			if pk == "" || strings.Contains(pk, "target_id") {
				return nil
			}
			// Collapse any rows that would collide under the new key before adding it.
			if _, err := db.ExecContext(ctx, `DELETE a FROM bridge_agents a JOIN bridge_agents b ON a.bridge_id=b.bridge_id AND a.core=b.core AND a.provider=b.provider AND a.target_id=b.target_id AND a.last_seen_at_ms < b.last_seen_at_ms`); err != nil {
				return err
			}
			_, err := db.ExecContext(ctx, `ALTER TABLE bridge_agents DROP PRIMARY KEY, ADD PRIMARY KEY (bridge_id, core, provider, target_id)`)
			return err
		},
	},
	{
		// Rows written before channels existed carry an empty channel_id. Every
		// reader filters on an exact channel id, so those messages are invisible
		// in the default channel's history while still counting toward the room's
		// unread badge: a member sees a count they cannot locate, open, or clear.
		// Move them into each room's default channel, where they belong.
		id: "2026_10_legacy_messages_to_default_channel",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, `UPDATE room_messages m
JOIN room_channels c ON c.room_id = m.room_id AND c.is_default = 1
SET m.channel_id = c.channel_id
WHERE m.channel_id = ''`); err != nil {
				return err
			}
			// Carry each member's legacy read cursor over so the backfill does not
			// resurrect messages they had already read.
			_, err := db.ExecContext(ctx, `UPDATE room_read_state r
JOIN room_channels c ON c.room_id = r.room_id AND c.is_default = 1
LEFT JOIN room_read_state d ON d.room_id = r.room_id AND d.user_id = r.user_id AND d.channel_id = c.channel_id
SET r.channel_id = c.channel_id
WHERE r.channel_id = '' AND d.user_id IS NULL`)
			return err
		},
	},
	{
		// Collaboration deliverables (specs, reports, code) become first-class
		// server objects so every member — including web clients with no local
		// bridge — previews the same current version. Versions are immutable and
		// CAS-published; content is capped by the API, not the schema.
		id: "2026_09_room_artifacts",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS artifacts (
  artifact_id     VARCHAR(64) PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  channel_id      VARCHAR(64) NOT NULL DEFAULT '',
  kind            VARCHAR(32) NOT NULL,
  title           VARCHAR(256) NOT NULL,
  language        VARCHAR(64) NOT NULL DEFAULT '',
  current_version INT NOT NULL DEFAULT 0,
  created_by      VARCHAR(128) NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  KEY idx_artifacts_room (tenant_id, room_id, channel_id, updated_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
				return err
			}
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS artifact_versions (
  tenant_id       VARCHAR(64) NOT NULL,
  artifact_id     VARCHAR(64) NOT NULL,
  version         INT NOT NULL,
  parent_version  INT NOT NULL,
  content_hash    CHAR(64) NOT NULL,
  content         MEDIUMTEXT NOT NULL,
  summary         VARCHAR(512) NOT NULL DEFAULT '',
  author_user_id  VARCHAR(128) NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, artifact_id, version),
  KEY idx_artifact_versions_recent (tenant_id, artifact_id, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		// Platform-wide key/value settings, used first by desktop updater policy
		// and release metadata. It is intentionally not tenant-scoped: these are
		// instance-level controls owned by platform super-admins.
		id: "2026_09_platform_settings",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS platform_settings (
  setting_key      VARCHAR(128) PRIMARY KEY,
  setting_value    TEXT NOT NULL,
  updated_at_ms    BIGINT NOT NULL,
  updated_by       VARCHAR(128) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		id: "2026_09_attachments",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attachments (
  id            VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  uploader      VARCHAR(128) NOT NULL,
  filename      VARCHAR(512) NOT NULL,
  content_type  VARCHAR(128) NOT NULL,
  size_bytes    BIGINT NOT NULL,
  sha256        VARCHAR(64) NOT NULL,
  blob_key      VARCHAR(128) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  KEY idx_attachments_room (tenant_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		id: "2026_09_agent_jobs_attachment_ids",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "agent_jobs", "attachment_ids", "JSON NULL")
		},
	},
	{
		id: "2026_09_extension_audit",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extension_audit (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  kind       VARCHAR(64) NOT NULL,
  name       VARCHAR(128) NOT NULL,
  action     VARCHAR(32) NOT NULL,
  actor      VARCHAR(128) NOT NULL DEFAULT '',
  key_id     VARCHAR(128) NOT NULL DEFAULT '',
  tenant_id  VARCHAR(64) NOT NULL DEFAULT '',
  outcome    VARCHAR(32) NOT NULL DEFAULT '',
  detail     TEXT,
  at_ms      BIGINT NOT NULL,
  KEY idx_extension_audit_kind (kind, at_ms),
  KEY idx_extension_audit_at (at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		id: "2026_09_component_installs",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS component_installs (
  subject          VARCHAR(255) NOT NULL,
  kind             VARCHAR(64) NOT NULL,
  name             VARCHAR(255) NOT NULL,
  installed_at_ms  BIGINT NOT NULL,
  PRIMARY KEY (subject, kind, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		// Converge every upgraded DB on the fresh schema.sql (OPS-13). The
		// multitenancy migration added tenant_id but left upgraded DBs different
		// from fresh ones in ways no later migration repaired:
		//   - tenant_id kept DEFAULT 'default' (a write that forgot its tenant
		//     silently landed in the default org instead of failing);
		//   - the secondary indexes kept their pre-tenancy, non-tenant-leading
		//     columns, so every tenant-scoped scan missed its index;
		//   - rooms kept the stale idx_room_visibility from the plaza era;
		//   - shared_agent_audit.action kept its original enum, so audit writes
		//     for login/room_create/room_join/room_leave failed.
		// Each step inspects information_schema first, so it is a no-op on a
		// fresh DB and safe to re-run.
		id: "2026_10_converge_fresh_schema",
		run: func(ctx context.Context, db sqlConn) error {
			for _, t := range []string{
				"rooms", "room_members", "room_invites",
				"agent_sessions", "agent_messages", "sync_states",
				"shared_agent_grants", "shared_agent_leases", "shared_agent_audit",
				"room_messages", "room_read_state",
			} {
				if err := dropColumnDefault(ctx, db, t, "tenant_id"); err != nil {
					return err
				}
			}
			for _, ix := range []struct {
				table, name string
				cols        []string
			}{
				{"rooms", "idx_rooms_active_created", []string{"tenant_id", "deleted_at_ms", "created_at_ms"}},
				{"room_members", "idx_member_user", []string{"tenant_id", "user_id"}},
				{"room_invites", "idx_invite_room", []string{"tenant_id", "room_id"}},
				{"agent_sessions", "idx_session_owner", []string{"tenant_id", "owner_user_id"}},
				{"shared_agent_grants", "idx_grant_room", []string{"tenant_id", "room_id"}},
				{"shared_agent_grants", "idx_grant_grantee", []string{"tenant_id", "grantee_user_id"}},
				{"shared_agent_audit", "idx_audit_room_time", []string{"tenant_id", "room_id", "at_ms"}},
			} {
				if err := ensureIndexColumns(ctx, db, ix.table, ix.name, ix.cols); err != nil {
					return err
				}
			}
			if err := dropIndexIfExists(ctx, db, "rooms", "idx_room_visibility"); err != nil {
				return err
			}
			return ensureColumnType(ctx, db, "shared_agent_audit", "action",
				"enum('start','resume','settings','turn','release','login','room_create','room_join','room_leave')",
				"ENUM('start','resume','settings','turn','release','login','room_create','room_join','room_leave') NOT NULL")
		},
	},
	{
		// The device-authority service was removed (no caller enrolled devices,
		// nothing verified its tokens), so its table goes too. Clean cutover:
		// the rows were never read by anything.
		id: "2026_10_drop_devices",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS devices")
			return err
		},
	},
	{
		// Borrow quota requests (F3): a borrower asks an owner for more runs
		// on the owner's room authorization; the owner approves/declines.
		id: "2026_10_borrow_quota_requests",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS shared_agent_quota_requests (
  request_id        VARCHAR(64) PRIMARY KEY,
  tenant_id         VARCHAR(64) NOT NULL,
  room_id           VARCHAR(64) NOT NULL,
  owner_user_id     VARCHAR(128) NOT NULL,
  requester_user_id VARCHAR(128) NOT NULL,
  provider          VARCHAR(128) NOT NULL,
  requested_runs    INT NULL,
  note              VARCHAR(500) NOT NULL DEFAULT '',
  status            ENUM('pending','approved','declined') NOT NULL DEFAULT 'pending',
  granted_runs      INT NULL,
  pending_key       VARCHAR(64) NULL,
  created_at_ms     BIGINT NOT NULL,
  decided_at_ms     BIGINT NOT NULL DEFAULT 0,
  decided_by        VARCHAR(128) NOT NULL DEFAULT '',
  UNIQUE KEY uq_quota_req_pending (pending_key),
  KEY idx_quota_req_owner (tenant_id, owner_user_id, status, created_at_ms),
  KEY idx_quota_req_requester (tenant_id, requester_user_id, created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
			return err
		},
	},
	{
		// Usage reporting (F1 team dashboard, F3 borrow billing): a per-turn
		// borrow cost on the audit row (NULL = the reporter supplied none) and
		// tenant+time indexes for the range scans over jobs, agent replies,
		// and borrow audit records.
		id: "2026_10_usage_indexes",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "shared_agent_audit", "cost", "DECIMAL(18,6) NULL"); err != nil {
				return err
			}
			for _, ix := range []struct{ table, name, ddl string }{
				{"agent_jobs", "idx_agent_jobs_tenant_created", "ADD KEY idx_agent_jobs_tenant_created (tenant_id, created_at_ms)"},
				{"agent_messages", "idx_agent_messages_tenant_time", "ADD KEY idx_agent_messages_tenant_time (tenant_id, created_at_ms)"},
				{"shared_agent_audit", "idx_audit_tenant_time", "ADD KEY idx_audit_tenant_time (tenant_id, at_ms)"},
			} {
				if err := addIndexIfMissing(ctx, db, ix.table, ix.name, ix.ddl); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// H3: SSO hand-offs move from gateway process memory to the shared
		// store so any replica can redeem them.
		id: "2026_10_sso_handoff_codes",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "sso_handoff_codes")
		},
	},
	{
		// H4: server-side refresh-token families (rotation, reuse detection,
		// revocation).
		id: "2026_10_refresh_tokens",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "refresh_token_families")
		},
	},
	{
		// F4: provenance of rooms created from a handoff package import.
		id: "2026_10_room_imports",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "room_imports")
		},
	},
	{
		// F5: per-member notification preferences.
		id: "2026_10_notification_prefs",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "notification_prefs")
		},
	},
	{
		// F5: durable outbound-notification queue, one row per (event,
		// notifier component). Changed in place (notifier column + key) before
		// any deployment: only dev/test databases ever ran it — reset those.
		id: "2026_10_notification_outbox",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "notification_outbox")
		},
	},
	{
		// F2: the prompt library.
		id: "2026_10_prompt_library",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "prompts")
		},
	},
	{
		// F6: chat messages marked as tasks.
		id: "2026_10_message_tasks",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "message_tasks")
		},
	},
	{
		// F7: global message search. search_text holds each message's plain
		// text (written on append/edit; rows stored before this migration stay
		// unindexed — dev stores are disposable, no backfill), FULLTEXT-indexed
		// with the ngram parser for CJK. Stopwords are disabled for the index
		// build exactly as schema.sql does (the ngram parser drops any token
		// containing a stopword such as "a" or "i").
		id: "2026_10_message_search",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "room_messages", "search_text", "MEDIUMTEXT NULL"); err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx, "SET SESSION innodb_ft_enable_stopword = OFF"); err != nil {
				return err
			}
			defer func() { _, _ = db.ExecContext(ctx, "SET SESSION innodb_ft_enable_stopword = DEFAULT") }()
			return addIndexIfMissing(ctx, db, "room_messages", "ft_room_messages_search",
				"ADD FULLTEXT KEY ft_room_messages_search (search_text) WITH PARSER ngram")
		},
	},
	{
		// G2: a recorded borrow turn carries its reported token usage next
		// to cost (NULL = not reported).
		id: "2026_10_g2_borrow_usage_tokens",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "shared_agent_audit", "input_tokens", "BIGINT NULL"); err != nil {
				return err
			}
			return addColumnIfMissing(ctx, db, "shared_agent_audit", "output_tokens", "BIGINT NULL")
		},
	},
	{
		// G4: a forked sign-in (the desktop's agent bridge) records the family
		// it was forked from, so the session list nests it under its device.
		id: "2026_10_g4_session_parent",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "refresh_token_families", "parent_family_id", "VARCHAR(32) NOT NULL DEFAULT ''")
		},
	},
	{
		// N4: members' browser Web Push subscriptions (built-in
		// notification channel).
		id: "2026_10_n4_web_push_subscriptions",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "web_push_subscriptions")
		},
	},
	{
		// N2: scheduled prompts.
		id: "2026_10_n2_room_schedules",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "room_schedules")
		},
	},
	{
		// N3: room templates.
		id: "2026_10_n3_room_templates",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "room_templates")
		},
	},
	{
		// O1: super-admin-issued API tokens for unattended callers.
		id: "2026_10_o1_api_tokens",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "api_tokens")
		},
	},
	{
		// N1: reply tokens of delivered notifications (reply from the
		// messenger).
		id: "2026_10_n1_notification_reply_tokens",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "notification_reply_tokens")
		},
	},
	{
		// Personal bots moved into the desktop client (a plugin with
		// desktop-local settings): the server no longer stores members'
		// notifier settings. Deployed databases created the table with the
		// former 2026_10_member_notifier_settings migration (removed with the
		// table from schema.sql); fresh databases never have it.
		id: "2026_10_drop_member_notifier_settings",
		run: func(ctx context.Context, db sqlConn) error {
			_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS member_notifier_settings")
			return err
		},
	},
	{
		// notification_outbox became one row per (event, notifier) by an
		// in-place edit of 2026_10_notification_outbox, but that migration had
		// already run on deployed databases, which kept the per-event shape
		// (no notifier column, PRIMARY KEY (tenant_id, id)) and refused every
		// enqueue. The outbox is a transient delivery queue and the old rows
		// belong to the removed tenant-webhook delivery (undeliverable now),
		// so an old-shape table is replaced with the schema.sql definition.
		// A table that already has the notifier column is left untouched.
		id: "2026_10_notification_outbox_per_notifier",
		run: func(ctx context.Context, db sqlConn) error {
			var n int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'notification_outbox' AND column_name = 'notifier'`).Scan(&n); err != nil {
				return fmt.Errorf("inspect notification_outbox.notifier: %w", err)
			}
			if n > 0 {
				return nil
			}
			if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS notification_outbox"); err != nil {
				return err
			}
			return createSchemaTable(ctx, db, "notification_outbox")
		},
	},
	{
		// R9: messenger message references a notifier registers for a reply
		// token, so any notifier replica resolves a reply (no process
		// memory).
		id: "2026_10_b3_r9_notification_reply_refs",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "notification_reply_refs")
		},
	},
	{
		// R10: agent run records — the claim phase timestamp, and the
		// room / owner / requester listing indexes the run views page over.
		id: "2026_10_b3_r10_agent_run_records",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "claimed_at_ms", "BIGINT NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			for _, ix := range []struct{ name, ddl string }{
				{"idx_agent_jobs_room_created", "ADD KEY idx_agent_jobs_room_created (tenant_id, room_id, created_at_ms)"},
				{"idx_agent_jobs_owner_created", "ADD KEY idx_agent_jobs_owner_created (tenant_id, owner_user_id, created_at_ms)"},
				{"idx_agent_jobs_requester_created", "ADD KEY idx_agent_jobs_requester_created (tenant_id, requester_user_id, created_at_ms)"},
			} {
				if err := addIndexIfMissing(ctx, db, "agent_jobs", ix.name, ix.ddl); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// R13: audit log page — the per-tenant extension_audit scan and the
		// cross-tenant (platform) shared_agent_audit scan.
		id: "2026_10_b3_r13_audit_log_indexes",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addIndexIfMissing(ctx, db, "extension_audit", "idx_extension_audit_tenant", "ADD KEY idx_extension_audit_tenant (tenant_id, at_ms)"); err != nil {
				return err
			}
			return addIndexIfMissing(ctx, db, "shared_agent_audit", "idx_audit_time", "ADD KEY idx_audit_time (at_ms)")
		},
	},
	{
		// R8: a borrowed run reserves its quota before the drive; its audit
		// record carries the reservation id and when its usage was settled.
		id: "2026_10_b3_r8_run_reservations",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "shared_agent_audit", "reservation_id", "VARCHAR(64) NULL"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "shared_agent_audit", "settled_at_ms", "BIGINT NULL"); err != nil {
				return err
			}
			return addIndexIfMissing(ctx, db, "shared_agent_audit", "uk_audit_reservation",
				"ADD UNIQUE KEY uk_audit_reservation (tenant_id, reservation_id)")
		},
	},
	{
		// R11: organization sync. A tenant membership records its owner
		// (manual | sync) so sync never removes a manual grant; the last
		// asserted directory attributes of each synced subject back the
		// resync; default rooms are added once per (user, room).
		id: "2026_10_b3_r11_org_sync",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "tenant_members", "source", "VARCHAR(16) NOT NULL DEFAULT 'manual'"); err != nil {
				return err
			}
			if err := createSchemaTable(ctx, db, "org_sync_subjects"); err != nil {
				return err
			}
			return createSchemaTable(ctx, db, "org_sync_default_rooms")
		},
	},
	{
		// R12: monthly usage budgets per member and per tenant.
		id: "2026_10_b3_r12_usage_budgets",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "usage_budgets")
		},
	},
	{
		// People chat (2026-10-08 UI redesign §3): a channel's kind — '' for an
		// ordinary channel, 'people' for the room's agent-invisible people
		// chat. Existing channels are ordinary.
		id: "2026_10_ui_people_channel",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "room_channels", "kind", "VARCHAR(16) NOT NULL DEFAULT ''")
		},
	},
	{
		// Agent VM sandbox (docs/prd/2026-10-09-agent-vm-sandbox.md): the
		// org-admin policy that borrowed drives must run sandboxed.
		id: "2026_10_sandbox_require_for_borrow",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "tenants", "require_sandbox_for_borrow", "TINYINT(1) NOT NULL DEFAULT 0")
		},
	},
	{
		// Preview workbench phase 1
		// (docs/prd/2026-10-09-preview-workbench-and-previewer-components.md):
		// binary-capable artifacts. An artifact carries its current MIME type
		// and file name; a version is inline text or a blob reference, typed,
		// sized, and linked to the job/message that produced it.
		id: "2026_10_preview_artifact_files",
		run: func(ctx context.Context, db sqlConn) error {
			for _, c := range [][3]string{
				{"artifacts", "mime_type", "VARCHAR(128) NOT NULL DEFAULT ''"},
				{"artifacts", "file_name", "VARCHAR(256) NOT NULL DEFAULT ''"},
				{"artifact_versions", "storage", "VARCHAR(8) NOT NULL DEFAULT 'inline'"},
				{"artifact_versions", "blob_key", "VARCHAR(80) NOT NULL DEFAULT ''"},
				{"artifact_versions", "size_bytes", "BIGINT NOT NULL DEFAULT 0"},
				{"artifact_versions", "mime_type", "VARCHAR(128) NOT NULL DEFAULT ''"},
				{"artifact_versions", "source_job_id", "VARCHAR(128) NOT NULL DEFAULT ''"},
				{"artifact_versions", "source_seq", "BIGINT NOT NULL DEFAULT 0"},
			} {
				if err := addColumnIfMissing(ctx, db, c[0], c[1], c[2]); err != nil {
					return err
				}
			}
			return addIndexIfMissing(ctx, db, "artifact_versions", "idx_artifact_versions_source",
				"ADD KEY idx_artifact_versions_source (tenant_id, source_seq)")
		},
	},
	{
		// Preview workbench phase 1: per-version thumbnails (blob key, type,
		// dimensions, and the writer's source rank).
		id: "2026_10_preview_thumbnails",
		run: func(ctx context.Context, db sqlConn) error {
			for _, c := range [][2]string{
				{"thumb_blob_key", "VARCHAR(80) NOT NULL DEFAULT ''"},
				{"thumb_mime_type", "VARCHAR(32) NOT NULL DEFAULT ''"},
				{"thumb_width", "INT NOT NULL DEFAULT 0"},
				{"thumb_height", "INT NOT NULL DEFAULT 0"},
				{"thumb_rank", "INT NOT NULL DEFAULT 0"},
			} {
				if err := addColumnIfMissing(ctx, db, "artifact_versions", c[0], c[1]); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Public share links of artifacts
		// (docs/prd/2026-10-09-artifact-shares-and-site-previews.md).
		id: "2026_10_artifact_shares",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "artifact_shares")
		},
	},
	{
		// The org-admin switch that turns public share links off.
		id: "2026_10_tenant_public_shares",
		run: func(ctx context.Context, db sqlConn) error {
			return addColumnIfMissing(ctx, db, "tenants", "public_shares_disabled", "TINYINT(1) NOT NULL DEFAULT 0")
		},
	},
	{
		// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2):
		// the host's hidden agent-host room membership, the grant source
		// ('' = owner-issued, 'platform') and who issued the grant (the
		// member who added a platform agent).
		id: "2026_10_platform_agents",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "room_members", "agent_host", "TINYINT(1) NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "shared_agent_grants", "source", "VARCHAR(16) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			return addColumnIfMissing(ctx, db, "shared_agent_grants", "created_by", "VARCHAR(128) NOT NULL DEFAULT ''")
		},
	},
	{
		// Platform agents v2 (docs/prd/2026-10-10-platform-agents-v2-onboarding.md
		// §3): a host's not-yet-started jobs are counted per agent across
		// tenants (store.PendingAgentJobs) on every platform-agent listing;
		// the owner-first index also serves the cross-tenant claim.
		id: "2026_10_agent_jobs_owner_status",
		run: func(ctx context.Context, db sqlConn) error {
			return addIndexIfMissing(ctx, db, "agent_jobs", "idx_agent_jobs_owner_status",
				"ADD KEY idx_agent_jobs_owner_status (owner_user_id, status, core, provider)")
		},
	},
	{
		// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
		// §1): one optional display name per user.
		id: "2026_10_user_profiles",
		run: func(ctx context.Context, db sqlConn) error {
			return createSchemaTable(ctx, db, "user_profiles")
		},
	},
	{
		// Data retention (docs/prd/2026-10-10-enterprise-scale.md §3): the
		// tenant override columns (null = the platform default) and the
		// indexes the retention worker deletes by age and the blob
		// collection checks references with.
		id: "2026_10_data_retention",
		run: func(ctx context.Context, db sqlConn) error {
			for _, col := range []string{"retention_messages_days", "retention_artifact_versions_days", "retention_audit_days", "retention_agent_jobs_days"} {
				if err := addColumnIfMissing(ctx, db, "tenants", col, "INT NULL"); err != nil {
					return err
				}
			}
			for _, ix := range []struct{ table, name, ddl string }{
				{"room_messages", "idx_room_messages_retention", "ADD KEY idx_room_messages_retention (tenant_id, kind, created_at_ms)"},
				{"attachments", "idx_attachments_created", "ADD KEY idx_attachments_created (tenant_id, created_at_ms)"},
				{"attachments", "idx_attachments_blob", "ADD KEY idx_attachments_blob (blob_key)"},
				{"artifact_versions", "idx_artifact_versions_created", "ADD KEY idx_artifact_versions_created (tenant_id, created_at_ms)"},
				{"artifact_versions", "idx_artifact_versions_blob", "ADD KEY idx_artifact_versions_blob (blob_key)"},
				{"artifact_versions", "idx_artifact_versions_thumb", "ADD KEY idx_artifact_versions_thumb (thumb_blob_key)"},
			} {
				if err := addIndexIfMissing(ctx, db, ix.table, ix.name, ix.ddl); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Room tools (docs/prd/2026-10-12-experts-and-teams.md §7): a job an
		// agent dispatched names its parent job and its hop depth; the
		// dispatch guards count a parent's dispatches on the index.
		id: "2026_10_agent_job_dispatch",
		run: func(ctx context.Context, db sqlConn) error {
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "dispatched_by_job_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if err := addColumnIfMissing(ctx, db, "agent_jobs", "dispatch_depth", "INT NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			return addIndexIfMissing(ctx, db, "agent_jobs", "idx_agent_jobs_dispatched_by",
				"ADD KEY idx_agent_jobs_dispatched_by (tenant_id, dispatched_by_job_id)")
		},
	},
	{
		// Content components (docs/prd/2026-10-12-experts-and-teams.md):
		// experts and skill packs, their immutable versions, where they are
		// enabled (member, room) and permission confirmations.
		id: "2026_10_content_components",
		run: func(ctx context.Context, db sqlConn) error {
			for _, t := range []string{"content_components", "content_component_versions", "content_component_uses", "content_permission_acks"} {
				if err := createSchemaTable(ctx, db, t); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// The expert a chat asked its agent to wear, pinned on the job
		// (docs/prd/2026-10-12-experts-and-teams.md §2 "Records").
		id: "2026_10_agent_job_expert",
		run: func(ctx context.Context, db sqlConn) error {
			for _, c := range [][2]string{
				{"expert_id", "VARCHAR(64) NOT NULL DEFAULT ''"},
				{"expert_version", "VARCHAR(64) NOT NULL DEFAULT ''"},
				{"expert_name", "VARCHAR(256) NOT NULL DEFAULT ''"},
			} {
				if err := addColumnIfMissing(ctx, db, "agent_jobs", c[0], c[1]); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Per-expert usage counts (docs/prd/2026-10-12-experts-and-teams.md,
		// step 6: the Library's runs and members of an expert) read the jobs
		// that wore it and their requesters on this index.
		id: "2026_10_agent_jobs_expert_usage",
		run: func(ctx context.Context, db sqlConn) error {
			return addIndexIfMissing(ctx, db, "agent_jobs", "idx_agent_jobs_expert",
				"ADD KEY idx_agent_jobs_expert (tenant_id, expert_id, requester_user_id)")
		},
	},
	{
		// Prompt packs replace the prompt library (docs/prd/2026-10-12-
		// experts-and-teams.md §4, decided: start empty): its table is
		// dropped with no conversion, and per-prompt use counts of the packs
		// get their table.
		id: "2026_10_prompt_packs",
		run: func(ctx context.Context, db sqlConn) error {
			if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS prompts"); err != nil {
				return err
			}
			return createSchemaTable(ctx, db, "content_prompt_uses")
		},
	},
}

// sqlConn is the statement surface a migration runs on. runMigrations passes
// one pinned *sql.Conn so every statement — the advisory lock, the DDL, and
// the ledger writes — executes on the same MySQL session.
type sqlConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// migrationLockName is the MySQL advisory lock serializing migration runs
// across every service that starts against the shared DB.
const migrationLockName = "bnbloc_schema_migrations"

// migrationLockWaitSeconds bounds how long a starter waits for another
// starter's migration run. Generous: a rebuild of a large table can take
// minutes, and a starter that gave up early would race the same DDL.
const migrationLockWaitSeconds = 600

// runMigrations ensures the ledger exists and applies any migration whose id is
// not yet recorded, in order. Each successful migration is written to the
// ledger so re-runs are no-ops.
//
// Every service starts and migrates against the shared DB at once, so the run
// is serialized with a MySQL advisory lock. GET_LOCK is owned by the SESSION
// that took it, so the lock, every migration statement, and RELEASE_LOCK run
// on ONE pinned connection (BE-19: on the pooled *sql.DB they could land on
// different sessions — RELEASE_LOCK then released nothing and the lock leaked
// until that pooled connection closed, while a concurrent starter's DDL ran
// unserialized). Failing to take the lock is an error, not a fall-through:
// running DDL unserialized is exactly what the lock exists to prevent.
func runMigrations(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin migration connection: %w", err)
	}
	defer conn.Close()
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrationLockName, migrationLockWaitSeconds).Scan(&locked); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("acquire migration lock: timed out after %ds waiting for another migration run", migrationLockWaitSeconds)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName) }()
	return applyMigrations(ctx, conn)
}

// applyMigrations runs the ledger + pending migrations on q. The caller holds
// the migration lock on q's session.
func applyMigrations(ctx context.Context, q sqlConn) error {
	if _, err := q.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  id            VARCHAR(128) PRIMARY KEY,
  applied_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, m := range migrations {
		applied, err := migrationApplied(ctx, q, m.id)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := m.run(ctx, q); err != nil {
			return fmt.Errorf("migration %s: %w", m.id, err)
		}
		// INSERT IGNORE keeps a ledger id that is somehow already present (a
		// manual repair, a restored backup) from turning into a fatal
		// duplicate-key error; the DDL itself is guarded and idempotent.
		if _, err := q.ExecContext(ctx,
			"INSERT IGNORE INTO schema_migrations (id, applied_at_ms) VALUES (?, ?)",
			m.id, time.Now().UnixMilli(),
		); err != nil {
			return fmt.Errorf("record migration %s: %w", m.id, err)
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, q sqlConn, id string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE id = ?", id,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("check migration %s: %w", id, err)
	}
	return n > 0, nil
}

// addColumnIfMissing adds table.column with the given DDL type only when it is
// not already present in the current schema (MySQL lacks ADD COLUMN IF NOT
// EXISTS).
func addColumnIfMissing(ctx context.Context, db sqlConn, table, column, ddl string) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		table, column).Scan(&n); err != nil {
		return fmt.Errorf("inspect column %s.%s: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// addIndexIfMissing runs "ALTER TABLE <table> <ddl>" only when indexName does
// not already exist on the table.
func addIndexIfMissing(ctx context.Context, db sqlConn, table, indexName, ddl string) error {
	exists, err := indexExists(ctx, db, table, indexName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s %s", table, ddl)); err != nil {
		return fmt.Errorf("add index %s on %s: %w", indexName, table, err)
	}
	return nil
}

// dropIndexIfExists drops indexName from table when present.
func dropIndexIfExists(ctx context.Context, db sqlConn, table, indexName string) error {
	exists, err := indexExists(ctx, db, table, indexName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP INDEX %s", table, indexName)); err != nil {
		return fmt.Errorf("drop index %s on %s: %w", indexName, table, err)
	}
	return nil
}

func indexExists(ctx context.Context, db sqlConn, table, indexName string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics
    WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`,
		table, indexName).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect index %s on %s: %w", indexName, table, err)
	}
	return n > 0, nil
}

// indexColumns returns the ordered column names composing indexName, or an
// empty slice when the index does not exist.
func indexColumns(ctx context.Context, db sqlConn, table, indexName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.statistics
    WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?
    ORDER BY seq_in_index`, table, indexName)
	if err != nil {
		return nil, fmt.Errorf("inspect index columns %s on %s: %w", indexName, table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan index column %s on %s: %w", indexName, table, err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// ensureIndexColumns makes a non-unique index named indexName cover exactly
// cols, in order: it adds a missing index, rebuilds one whose columns differ
// (drop + add in one ALTER, so the table is never left without it), and is a
// no-op when the index already matches.
func ensureIndexColumns(ctx context.Context, db sqlConn, table, indexName string, cols []string) error {
	have, err := indexColumns(ctx, db, table, indexName)
	if err != nil {
		return err
	}
	if strings.Join(have, ",") == strings.Join(cols, ",") {
		return nil
	}
	add := fmt.Sprintf("ADD KEY %s (%s)", indexName, strings.Join(cols, ", "))
	if len(have) > 0 {
		add = fmt.Sprintf("DROP INDEX %s, %s", indexName, add)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s %s", table, add)); err != nil {
		return fmt.Errorf("rebuild index %s on %s: %w", indexName, table, err)
	}
	return nil
}

// dropColumnDefault removes table.column's DEFAULT when it has one.
func dropColumnDefault(ctx context.Context, db sqlConn, table, column string) error {
	var def sql.NullString
	err := db.QueryRowContext(ctx, `SELECT column_default FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect default %s.%s: %w", table, column, err)
	}
	if !def.Valid {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, column)); err != nil {
		return fmt.Errorf("drop default %s.%s: %w", table, column, err)
	}
	return nil
}

// ensureColumnType rewrites table.column to ddl when its information_schema
// COLUMN_TYPE differs from columnType (e.g. an enum that gained values).
func ensureColumnType(ctx context.Context, db sqlConn, table, column, columnType, ddl string) error {
	var have string
	err := db.QueryRowContext(ctx, `SELECT column_type FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&have)
	if err != nil {
		return fmt.Errorf("inspect type %s.%s: %w", table, column, err)
	}
	if strings.EqualFold(have, columnType) {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", table, column, ddl)); err != nil {
		return fmt.Errorf("modify %s.%s: %w", table, column, err)
	}
	return nil
}
