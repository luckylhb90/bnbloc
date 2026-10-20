-- bnbloc control-plane schema (MySQL 8, InnoDB, utf8mb4).
-- Timestamps are BIGINT unix-ms for parity with the wire contracts.
-- No provider credentials, host paths, or account cookies are ever stored.
--
-- Multi-tenancy: every row is scoped by tenant_id (the organization). It is the
-- hard isolation boundary — the store refuses a query with no tenant. Existing
-- single-tenant data is backfilled to tenant 'default' by a migration, so the
-- upgrade is non-breaking. tenant_id leads composite keys/indexes so per-tenant
-- scans stay selective.

CREATE TABLE IF NOT EXISTS tenants (
  tenant_id     VARCHAR(64) PRIMARY KEY,
  name          VARCHAR(255) NOT NULL,
  status        ENUM('active','suspended') NOT NULL DEFAULT 'active',
  sso_issuer    VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  run_quota     INT NOT NULL DEFAULT 0,
  cost_allowance DECIMAL(18,6) NOT NULL DEFAULT 0,
  require_sandbox_for_borrow TINYINT(1) NOT NULL DEFAULT 0,
  anonymous_links_disabled TINYINT(1) NOT NULL DEFAULT 0,
  outside_links_disabled TINYINT(1) NOT NULL DEFAULT 0,
  publish_approval VARCHAR(16) NOT NULL DEFAULT 'required',
  assist_disabled TINYINT(1) NOT NULL DEFAULT 0,
  created_at_ms BIGINT NOT NULL,
  retention_messages_days INT NULL,
  retention_artifact_versions_days INT NULL,
  retention_audit_days INT NULL,
  retention_agent_jobs_days INT NULL,
  UNIQUE KEY uq_tenant_issuer (sso_issuer)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tenant_members (
  tenant_id    VARCHAR(64) NOT NULL,
  user_id      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  role         ENUM('org_admin','member','viewer') NOT NULL DEFAULT 'member',
  joined_at_ms BIGINT NOT NULL,
  source       VARCHAR(16) NOT NULL DEFAULT 'manual',
  PRIMARY KEY (tenant_id, user_id),
  KEY idx_tenant_member_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS org_sync_subjects (
  user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin PRIMARY KEY,
  email         VARCHAR(255) NOT NULL DEFAULT '',
  display_name  VARCHAR(255) NOT NULL DEFAULT '',
  department    VARCHAR(255) NOT NULL DEFAULT '',
  groups_json   JSON NOT NULL,
  synced_at_ms  BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS org_sync_default_rooms (
  user_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  granted_at_ms  BIGINT NOT NULL,
  PRIMARY KEY (user_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tenant_join_requests (
  tenant_id     VARCHAR(64) NOT NULL,
  user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  message       VARCHAR(500) NOT NULL DEFAULT '',
  status        ENUM('pending','approved','rejected') NOT NULL DEFAULT 'pending',
  created_at_ms BIGINT NOT NULL,
  decided_at_ms BIGINT NOT NULL DEFAULT 0,
  decided_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, user_id),
  KEY idx_join_req_status (tenant_id, status),
  KEY idx_join_req_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS platform_settings (
  setting_key      VARCHAR(128) PRIMARY KEY,
  setting_value    TEXT NOT NULL,
  updated_at_ms    BIGINT NOT NULL,
  updated_by       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS component_installs (
  subject          VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  kind             VARCHAR(64) NOT NULL,
  name             VARCHAR(255) NOT NULL,
  installed_at_ms  BIGINT NOT NULL,
  PRIMARY KEY (subject, kind, name),
  KEY idx_component_installs_kind (kind, name, subject)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;


CREATE TABLE IF NOT EXISTS extension_audit (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  kind       VARCHAR(64) NOT NULL,
  name       VARCHAR(128) NOT NULL,
  action     VARCHAR(32) NOT NULL,
  actor      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  key_id     VARCHAR(128) NOT NULL DEFAULT '',
  tenant_id  VARCHAR(64) NOT NULL DEFAULT '',
  outcome    VARCHAR(32) NOT NULL DEFAULT '',
  detail     TEXT,
  at_ms      BIGINT NOT NULL,
  KEY idx_extension_audit_kind (kind, at_ms),
  KEY idx_extension_audit_at (at_ms),
  KEY idx_extension_audit_tenant (tenant_id, at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;


CREATE TABLE IF NOT EXISTS agent_jobs (
  job_id              VARCHAR(64) PRIMARY KEY,
  tenant_id           VARCHAR(64) NOT NULL,
  room_id             VARCHAR(64) NOT NULL,
  channel_id          VARCHAR(64) NOT NULL DEFAULT '',
  message_seq         BIGINT NOT NULL,
  client_msg_id       VARCHAR(128) NOT NULL DEFAULT '',
  requester_user_id   VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  owner_user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  core                VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  provider            VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  target_id           VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  message_text       TEXT NULL,
  attachment_ids     JSON NULL,
  model              VARCHAR(128) NOT NULL DEFAULT '',
  reasoning_effort   VARCHAR(64) NOT NULL DEFAULT '',
  status              ENUM('queued','claimed','running','reflowing','replied','failed','timed_out','cancelled') NOT NULL DEFAULT 'queued',
  failure_reason      VARCHAR(64) NOT NULL DEFAULT '',
  attempt             INT NOT NULL DEFAULT 0,
  lease_bridge_id     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  lease_expires_at_ms BIGINT NOT NULL DEFAULT 0,
  created_at_ms       BIGINT NOT NULL,
  updated_at_ms       BIGINT NOT NULL,
  started_at_ms       BIGINT NOT NULL DEFAULT 0,
  finished_at_ms      BIGINT NOT NULL DEFAULT 0,
  diagnostics_json    JSON NULL,
  claimed_at_ms       BIGINT NOT NULL DEFAULT 0,
  dispatched_by_job_id VARCHAR(64) NOT NULL DEFAULT '',
  dispatch_depth      INT NOT NULL DEFAULT 0,
  expert_id           VARCHAR(64) NOT NULL DEFAULT '',
  expert_version      VARCHAR(64) NOT NULL DEFAULT '',
  expert_name         VARCHAR(256) NOT NULL DEFAULT '',
  room_expert_id      VARCHAR(64) NOT NULL DEFAULT '',
  room_expert_version VARCHAR(64) NOT NULL DEFAULT '',
  room_expert_name    VARCHAR(256) NOT NULL DEFAULT '',
  attempts_json       JSON NULL,
  not_before_ms       BIGINT NOT NULL DEFAULT 0,
  file_ref_json       JSON NULL,
  UNIQUE KEY uq_agent_job_origin (tenant_id, room_id, message_seq, owner_user_id, core, provider),
  KEY idx_agent_jobs_claim (tenant_id, owner_user_id, status, lease_expires_at_ms),
  KEY idx_agent_jobs_room (tenant_id, room_id, channel_id, updated_at_ms),
  KEY idx_agent_jobs_tenant_created (tenant_id, created_at_ms),
  KEY idx_agent_jobs_room_created (tenant_id, room_id, created_at_ms),
  KEY idx_agent_jobs_owner_created (tenant_id, owner_user_id, created_at_ms),
  KEY idx_agent_jobs_requester_created (tenant_id, requester_user_id, created_at_ms),
  KEY idx_agent_jobs_owner_status (owner_user_id, status, core, provider, not_before_ms),
  KEY idx_agent_jobs_dispatched_by (tenant_id, dispatched_by_job_id),
  KEY idx_agent_jobs_expert (tenant_id, expert_id, requester_user_id),
  KEY idx_agent_jobs_status_created (status, created_at_ms),
  KEY idx_agent_jobs_status_lease (status, lease_expires_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS bridge_heartbeats (
  bridge_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  owner_user_id   VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  app_instance_id VARCHAR(128) NOT NULL DEFAULT '',
  version         VARCHAR(64) NOT NULL DEFAULT '',
  status          ENUM('online','offline','draining') NOT NULL DEFAULT 'online',
  serve_all_owners TINYINT(1) NOT NULL DEFAULT 0,
  last_seen_at_ms BIGINT NOT NULL,
  metadata_json   JSON NULL,
  KEY idx_bridge_owner (tenant_id, owner_user_id, last_seen_at_ms),
  KEY idx_bridge_heartbeats_owner_seen (owner_user_id, last_seen_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS bridge_agents (
  bridge_id          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tenant_id          VARCHAR(64) NOT NULL,
  owner_user_id      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  core               VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  provider           VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  target_id          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  model_options_json JSON NULL,
  disabled           TINYINT(1) NOT NULL DEFAULT 0,
  last_seen_at_ms    BIGINT NOT NULL,
  PRIMARY KEY (bridge_id, core, provider, target_id),
  KEY idx_bridge_agents_lookup (tenant_id, owner_user_id, core, provider, disabled),
  KEY idx_bridge_agents_owner (owner_user_id, disabled, core, provider)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_agent_prefs (
  tenant_id        VARCHAR(64) NOT NULL,
  user_id          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  room_id          VARCHAR(64) NOT NULL,
  channel_id       VARCHAR(64) NOT NULL DEFAULT '',
  selected_owner   VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  selected_core    VARCHAR(64) NOT NULL DEFAULT '',
  selected_provider VARCHAR(128) NOT NULL DEFAULT '',
  selected_model   VARCHAR(128) NOT NULL DEFAULT '',
  selected_reasoning VARCHAR(64) NOT NULL DEFAULT '',
  updated_at_ms    BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, user_id, room_id, channel_id),
  KEY idx_uap_user (tenant_id, user_id, updated_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS room_channels (
  channel_id    VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(255) NOT NULL,
  is_default    TINYINT(1) NOT NULL DEFAULT 0,
  kind          VARCHAR(16) NOT NULL DEFAULT '',
  created_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  KEY idx_channel_room (tenant_id, room_id, deleted_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS rooms (
  tenant_id      VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  name           VARCHAR(255) NOT NULL,
  description    VARCHAR(500) NOT NULL DEFAULT '',
  visibility     ENUM('shared','private') NOT NULL DEFAULT 'private',
  sync_mode      ENUM('git','mutagen','none') NOT NULL DEFAULT 'git',
  created_by     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms  BIGINT NOT NULL,
  deleted_at_ms  BIGINT NULL,
  PRIMARY KEY (room_id),
  KEY idx_rooms_active_created (tenant_id, deleted_at_ms, created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS room_members (
  tenant_id      VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  user_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  role           ENUM('owner','admin','member','visitor') NOT NULL,
  joined_at_ms   BIGINT NOT NULL,
  agent_host     TINYINT(1) NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, user_id),
  KEY idx_member_user (tenant_id, user_id),
  KEY idx_member_user_host (user_id, agent_host)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS room_invites (
  code           VARCHAR(64) PRIMARY KEY,
  tenant_id      VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  role           ENUM('member','visitor') NOT NULL,
  expires_at_ms  BIGINT NOT NULL,
  capacity       INT NOT NULL,
  redeemed_count INT NOT NULL DEFAULT 0,
  created_by     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  KEY idx_invite_room (tenant_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS agent_sessions (
  tenant_id           VARCHAR(64) NOT NULL,
  room_id             VARCHAR(64) NOT NULL,
  agent_session_id    VARCHAR(128) NOT NULL,
  message_version     BIGINT NOT NULL,
  PRIMARY KEY (room_id, agent_session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS agent_messages (
  tenant_id        VARCHAR(64) NOT NULL,
  room_id          VARCHAR(64) NOT NULL,
  agent_session_id VARCHAR(128) NOT NULL,
  seq              BIGINT NOT NULL,
  payload_json     JSON NOT NULL,
  created_at_ms    BIGINT NOT NULL,
  usage_reported      TINYINT(1) NOT NULL DEFAULT 0,
  usage_owner_user_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  usage_provider      VARCHAR(128) NOT NULL DEFAULT '',
  usage_input_tokens  BIGINT NOT NULL DEFAULT 0,
  usage_output_tokens BIGINT NOT NULL DEFAULT 0,
  usage_cost_usd      DECIMAL(18,6) NULL,
  PRIMARY KEY (room_id, agent_session_id, seq),
  KEY idx_agent_messages_tenant_time (tenant_id, created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS sync_states (
  tenant_id         VARCHAR(64) NOT NULL,
  room_id           VARCHAR(64) NOT NULL,
  agent_session_id  VARCHAR(128) NOT NULL,
  message_cursor    BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, agent_session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS shared_agent_grants (
  grant_id            VARCHAR(64) PRIMARY KEY,
  tenant_id           VARCHAR(64) NOT NULL,
  room_id             VARCHAR(64) NOT NULL,
  target_id           VARCHAR(128) NOT NULL,
  owner_user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  grantee_user_id     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
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
  source              VARCHAR(16) NOT NULL DEFAULT '',
  created_by          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  KEY idx_grant_room (tenant_id, room_id),
  KEY idx_grant_grantee (tenant_id, grantee_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS shared_agent_audit (
  audit_id      BIGINT AUTO_INCREMENT PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  grant_id      VARCHAR(64) NULL,
  actor_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  owner_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  action        ENUM('start','resume','settings','turn','release','login','room_create','room_join','room_leave') NOT NULL,
  decision      ENUM('allow','deny') NOT NULL,
  deny_reason   VARCHAR(64) NULL,
  model         VARCHAR(128) NULL,
  channel_id    VARCHAR(64) NOT NULL DEFAULT '',
  at_ms         BIGINT NOT NULL,
  cost          DECIMAL(18,6) NULL,
  input_tokens  BIGINT NULL,
  output_tokens BIGINT NULL,
  reservation_id VARCHAR(64) NULL,
  settled_at_ms  BIGINT NULL,
  KEY idx_audit_room_time (tenant_id, room_id, at_ms),
  KEY idx_audit_tenant_time (tenant_id, at_ms),
  KEY idx_audit_time (at_ms),
  UNIQUE KEY uk_audit_reservation (tenant_id, reservation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- search_text is the message's plain text (chat text / joined agent replies),
-- indexed FULLTEXT with the ngram parser so CJK text is searchable. The ngram
-- parser drops every token that CONTAINS a stopword (the default list holds
-- "a" and "i"), so the index is created with stopwords disabled; the setting
-- is read only at index creation and is restored right after.
SET SESSION innodb_ft_enable_stopword = OFF;
CREATE TABLE IF NOT EXISTS room_messages (
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  channel_id    VARCHAR(64) NOT NULL DEFAULT '',
  seq           BIGINT NOT NULL,
  kind          VARCHAR(32) NOT NULL,
  author        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  client_msg_id VARCHAR(128) NULL,
  payload_json  JSON NOT NULL,
  search_text   MEDIUMTEXT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  edited_at_ms  BIGINT NULL,
  revised_at_ms BIGINT NULL,
  PRIMARY KEY (room_id, seq),
  UNIQUE KEY uq_room_client_msg (room_id, client_msg_id),
  KEY idx_room_channel_seq (room_id, channel_id, seq),
  KEY idx_room_messages_revised (room_id, revised_at_ms),
  KEY idx_room_messages_retention (tenant_id, kind, created_at_ms),
  FULLTEXT KEY ft_room_messages_search (search_text) WITH PARSER ngram
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
SET SESSION innodb_ft_enable_stopword = DEFAULT;

-- Message tasks (F6): a persisted room message (by room sequence) marked as a
-- task. title/message_author are snapshots taken when the task was marked.
CREATE TABLE IF NOT EXISTS message_tasks (
  tenant_id      VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  channel_id     VARCHAR(64) NOT NULL DEFAULT '',
  seq            BIGINT NOT NULL,
  status         VARCHAR(16) NOT NULL,
  title          VARCHAR(512) NOT NULL,
  message_author VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  created_by     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms  BIGINT NOT NULL,
  done_by        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  done_at_ms     BIGINT NULL,
  PRIMARY KEY (room_id, seq),
  KEY idx_message_tasks_room (tenant_id, room_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS attachments (
  id            VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  uploader      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  filename      VARCHAR(512) NOT NULL,
  content_type  VARCHAR(128) NOT NULL,
  size_bytes    BIGINT NOT NULL,
  sha256        VARCHAR(64) NOT NULL,
  blob_key      VARCHAR(128) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  KEY idx_attachments_room (tenant_id, room_id),
  KEY idx_attachments_created (tenant_id, created_at_ms),
  KEY idx_attachments_blob (blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS room_read_state (
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  channel_id    VARCHAR(64) NOT NULL DEFAULT '',
  user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  last_read_seq BIGINT NOT NULL DEFAULT 0,
  updated_at_ms BIGINT NOT NULL,
  PRIMARY KEY (room_id, channel_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS artifacts (
  artifact_id     VARCHAR(64) PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  channel_id      VARCHAR(64) NOT NULL DEFAULT '',
  kind            VARCHAR(32) NOT NULL,
  title           VARCHAR(256) NOT NULL,
  language        VARCHAR(64) NOT NULL DEFAULT '',
  current_version INT NOT NULL DEFAULT 0,
  created_by      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  mime_type       VARCHAR(128) NOT NULL DEFAULT '',
  file_name       VARCHAR(256) NOT NULL DEFAULT '',
  KEY idx_artifacts_room (tenant_id, room_id, channel_id, updated_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS artifact_versions (
  tenant_id       VARCHAR(64) NOT NULL,
  artifact_id     VARCHAR(64) NOT NULL,
  version         INT NOT NULL,
  parent_version  INT NOT NULL,
  content_hash    CHAR(64) NOT NULL,
  content         MEDIUMTEXT NOT NULL,
  summary         VARCHAR(512) NOT NULL DEFAULT '',
  author_user_id  VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  storage         VARCHAR(8) NOT NULL DEFAULT 'inline',
  blob_key        VARCHAR(80) NOT NULL DEFAULT '',
  size_bytes      BIGINT NOT NULL DEFAULT 0,
  mime_type       VARCHAR(128) NOT NULL DEFAULT '',
  source_job_id   VARCHAR(128) NOT NULL DEFAULT '',
  source_seq      BIGINT NOT NULL DEFAULT 0,
  thumb_blob_key  VARCHAR(80) NOT NULL DEFAULT '',
  thumb_mime_type VARCHAR(32) NOT NULL DEFAULT '',
  thumb_width     INT NOT NULL DEFAULT 0,
  thumb_height    INT NOT NULL DEFAULT 0,
  thumb_rank      INT NOT NULL DEFAULT 0,
  loads_app_sdk   TINYINT(1) NOT NULL DEFAULT 0,
  edited_from     INT NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, artifact_id, version),
  KEY idx_artifact_versions_source (tenant_id, source_seq),
  KEY idx_artifact_versions_created (tenant_id, created_at_ms),
  KEY idx_artifact_versions_blob (blob_key),
  KEY idx_artifact_versions_thumb (thumb_blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Share links of one artifact (docs/prd/2026-10-09-artifact-shares-
-- and-site-previews.md): an expiring, revocable token (internal/sharelink,
-- docs/prd/2026-10-13-short-share-links.md); only its sha256 hex is
-- stored, and the link is found by it. version 0 follows the artifact's
-- latest version. access is who may open it: 'org' (signed-in members of
-- the organization), 'signed_in' (anyone signed in) or 'anyone'
-- (docs/prd/2026-10-13-share-link-sign-in.md). token_sealed is the token
-- sealed under the deployment key, so its creator can copy it again until it
-- is revoked (docs/prd/2026-10-16-share-link-copy-again.md). Not exported
-- with the room.
CREATE TABLE IF NOT EXISTS artifact_shares (
  share_id          VARCHAR(32) PRIMARY KEY,
  token_hash        CHAR(64) NOT NULL,
  tenant_id         VARCHAR(64) NOT NULL,
  room_id           VARCHAR(64) NOT NULL,
  artifact_id       VARCHAR(64) NOT NULL,
  version           INT NOT NULL,
  access            VARCHAR(16) NOT NULL DEFAULT 'org',
  created_by        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms     BIGINT NOT NULL,
  expires_at_ms     BIGINT NOT NULL,
  revoked_at_ms     BIGINT NULL,
  revoked_by        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  view_count        BIGINT NOT NULL DEFAULT 0,
  last_viewed_at_ms BIGINT NULL,
  token_sealed      VARCHAR(128) NULL,
  UNIQUE KEY uq_artifact_shares_token (token_hash),
  KEY idx_artifact_shares_artifact (tenant_id, room_id, artifact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Handoff import provenance: one row per room created from a handoff package.
-- suggested_members_json is the source room's member list, offered as invite
-- suggestions only; it never grants anyone access.
CREATE TABLE IF NOT EXISTS room_imports (
  tenant_id              VARCHAR(64) NOT NULL,
  room_id                VARCHAR(64) PRIMARY KEY,
  source_room_id         VARCHAR(64) NOT NULL,
  source_room_name       VARCHAR(255) NOT NULL DEFAULT '',
  source_exported_at_ms  BIGINT NOT NULL,
  source_exported_by     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  imported_by            VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  imported_at_ms         BIGINT NOT NULL,
  suggested_members_json JSON NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Pending SSO hand-offs (desktop one-time codes, CLI sign-in grants), shared
-- by every gateway replica. Keyed by a hash of the code/nonce (never the raw
-- secret); holds the verified identity, never a token. Single use via
-- consumed_at_ms; expired rows are pruned on insert.
CREATE TABLE IF NOT EXISTS sso_handoff_codes (
  key_hash        CHAR(64) PRIMARY KEY,
  nonce_hash      CHAR(64) NOT NULL,
  subject         VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  email           VARCHAR(255) NOT NULL DEFAULT '',
  tenant_hint     VARCHAR(64) NOT NULL DEFAULT '',
  expires_at_ms   BIGINT NOT NULL,
  consumed_at_ms  BIGINT NULL,
  KEY idx_sso_handoff_codes_expiry (expires_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Refresh-token families: one row per sign-in, tracking the current token id
-- so rotation, reuse detection, logout and admin revocation are server-side.
CREATE TABLE IF NOT EXISTS refresh_token_families (
  family_id        CHAR(32) PRIMARY KEY,
  subject          VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  email            VARCHAR(255) NOT NULL DEFAULT '',
  tenant_hint      VARCHAR(64) NOT NULL DEFAULT '',
  tenant_id        VARCHAR(64) NOT NULL DEFAULT '',
  role             VARCHAR(32) NOT NULL DEFAULT '',
  client           VARCHAR(32) NOT NULL DEFAULT '',
  user_agent       VARCHAR(255) NOT NULL DEFAULT '',
  current_jti      CHAR(32) NOT NULL,
  previous_jti     VARCHAR(32) NOT NULL DEFAULT '',
  rotated_at_ms    BIGINT NOT NULL,
  created_at_ms    BIGINT NOT NULL,
  last_used_at_ms  BIGINT NOT NULL,
  expires_at_ms    BIGINT NOT NULL,
  revoked_at_ms    BIGINT NULL,
  parent_family_id VARCHAR(32) NOT NULL DEFAULT '',
  KEY idx_refresh_families_subject (subject, tenant_id),
  KEY idx_refresh_families_tenant (tenant_id),
  KEY idx_refresh_families_expiry (expires_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Borrow quota requests: a borrower asks an agent owner for more runs on the
-- owner's room authorization. requester/owner are always the authenticated
-- callers that created/decided the row. pending_key is set only while the
-- request is pending (NULL once decided), so the unique key admits at most one
-- open request per (tenant, room, owner, provider, requester).
CREATE TABLE IF NOT EXISTS shared_agent_quota_requests (
  request_id        VARCHAR(64) PRIMARY KEY,
  tenant_id         VARCHAR(64) NOT NULL,
  room_id           VARCHAR(64) NOT NULL,
  owner_user_id     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  requester_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  provider          VARCHAR(128) NOT NULL,
  kind              VARCHAR(16) NOT NULL DEFAULT 'quota',
  requested_runs    INT NULL,
  note              VARCHAR(500) NOT NULL DEFAULT '',
  status            ENUM('pending','approved','declined') NOT NULL DEFAULT 'pending',
  granted_runs      INT NULL,
  pending_key       VARCHAR(64) NULL,
  created_at_ms     BIGINT NOT NULL,
  decided_at_ms     BIGINT NOT NULL DEFAULT 0,
  decided_by        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  UNIQUE KEY uq_quota_req_pending (pending_key),
  KEY idx_quota_req_owner (tenant_id, owner_user_id, status, created_at_ms),
  KEY idx_quota_req_requester (tenant_id, requester_user_id, created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS notification_prefs (
  tenant_id       VARCHAR(64) NOT NULL,
  user_id         VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  mentions        TINYINT(1) NOT NULL,
  agent_finished  TINYINT(1) NOT NULL,
  agent_failed    TINYINT(1) NOT NULL,
  borrow_requests TINYINT(1) NOT NULL,
  content_shared  TINYINT(1) NOT NULL DEFAULT 1,
  content_review  TINYINT(1) NOT NULL DEFAULT 1,
  app_notices     TINYINT(1) NOT NULL DEFAULT 1,
  quiet_rooms     JSON NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS notification_outbox (
  tenant_id          VARCHAR(64) NOT NULL,
  id                 VARCHAR(64) NOT NULL,
  notifier           VARCHAR(255) NOT NULL,
  event_json         JSON NOT NULL,
  attempts           INT NOT NULL DEFAULT 0,
  next_attempt_at_ms BIGINT NOT NULL,
  claim_token        VARCHAR(64) NOT NULL DEFAULT '',
  delivered_at_ms    BIGINT NULL,
  failed_at_ms       BIGINT NULL,
  last_error         VARCHAR(512) NOT NULL DEFAULT '',
  created_at_ms      BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, id, notifier),
  KEY idx_notification_outbox_due (delivered_at_ms, failed_at_ms, next_attempt_at_ms),
  KEY idx_notification_outbox_claim (claim_token),
  KEY idx_notification_outbox_created (created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Notification reply tokens (N1): a notifier component posts a member's
-- reply to a notification back into the room with one of these. Only the
-- SHA-256 of the token is stored; looked up by hash (cross-tenant: the
-- notifier has no session); many replies per token until expires_at_ms.
CREATE TABLE IF NOT EXISTS notification_reply_tokens (
  token_hash     CHAR(64) PRIMARY KEY,
  tenant_id      VARCHAR(64) NOT NULL,
  notifier       VARCHAR(255) NOT NULL,
  event_id       VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  channel_id     VARCHAR(64) NOT NULL DEFAULT '',
  user_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  reply_owner_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  reply_provider VARCHAR(128) NOT NULL DEFAULT '',
  reply_core     VARCHAR(64) NOT NULL DEFAULT '',
  created_at_ms  BIGINT NOT NULL,
  expires_at_ms  BIGINT NOT NULL,
  KEY idx_notification_reply_tokens_notifier (notifier),
  KEY idx_notification_reply_tokens_expires (expires_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS notification_reply_refs (
  notifier      VARCHAR(255) NOT NULL,
  ref_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  recipient     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  token_hash    CHAR(64) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  expires_at_ms BIGINT NOT NULL,
  PRIMARY KEY (notifier, ref_id, recipient),
  KEY idx_notification_reply_refs_expires (expires_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS web_push_subscriptions (
  tenant_id     VARCHAR(64) NOT NULL,
  id            CHAR(64) NOT NULL,
  user_id       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  endpoint      TEXT NOT NULL,
  p256dh        VARCHAR(255) NOT NULL,
  auth_secret   VARCHAR(255) NOT NULL,
  user_agent    VARCHAR(255) NOT NULL DEFAULT '',
  created_at_ms BIGINT NOT NULL,
  updated_at_ms BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, id),
  KEY idx_web_push_subscriptions_user (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Scheduled prompts (N2): a member's message posted into the room at a time,
-- as that member, through the normal chat path. next_run_at_ms is NULL when
-- the schedule will not fire (paused, or a one-shot that already ran).
-- claim_token/claimed_until_ms are the scheduler's atomic claim of a due run
-- (a crashed claim expires and the slot is re-claimed; the chat clientMsgId
-- of the slot dedupes the post).
CREATE TABLE IF NOT EXISTS room_schedules (
  schedule_id      VARCHAR(64) PRIMARY KEY,
  tenant_id        VARCHAR(64) NOT NULL,
  room_id          VARCHAR(64) NOT NULL,
  channel_id       VARCHAR(64) NOT NULL DEFAULT '',
  created_by       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  text             MEDIUMTEXT NOT NULL,
  recipient_json   JSON NOT NULL,
  recurrence_json  JSON NOT NULL,
  timezone         VARCHAR(64) NOT NULL,
  enabled          TINYINT(1) NOT NULL DEFAULT 1,
  next_run_at_ms   BIGINT NULL,
  claim_token      VARCHAR(64) NULL,
  claimed_until_ms BIGINT NULL,
  last_run_at_ms   BIGINT NULL,
  last_status      VARCHAR(16) NOT NULL DEFAULT '',
  last_error       VARCHAR(512) NOT NULL DEFAULT '',
  last_summary     VARCHAR(512) NOT NULL DEFAULT '',
  failed_runs      BIGINT NOT NULL DEFAULT 0,
  last_seq         BIGINT NOT NULL DEFAULT 0,
  run_count        BIGINT NOT NULL DEFAULT 0,
  created_at_ms    BIGINT NOT NULL,
  updated_at_ms    BIGINT NOT NULL,
  KEY idx_room_schedules_room (tenant_id, room_id),
  KEY idx_room_schedules_due (enabled, next_run_at_ms),
  KEY idx_room_schedules_claim (claim_token)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Room templates (N3), retired: room packages (bnbloc.room.package/v1,
-- docs/prd/2026-10-20-rooms-as-components.md) replaced them, and no code
-- reads or writes this table. A release after the one that stopped using
-- it drops it (AGENTS.md, "A migration only adds"); nothing is kept.
CREATE TABLE IF NOT EXISTS room_templates (
  template_id   VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  name          VARCHAR(256) NOT NULL,
  description   VARCHAR(2048) NOT NULL DEFAULT '',
  visibility    VARCHAR(16) NOT NULL,
  created_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  body_json     JSON NOT NULL,
  use_count     BIGINT NOT NULL DEFAULT 0,
  created_at_ms BIGINT NOT NULL,
  updated_at_ms BIGINT NOT NULL,
  KEY idx_room_templates_tenant_updated (tenant_id, updated_at_ms),
  KEY idx_room_templates_tenant_author (tenant_id, created_by)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- API tokens (O1): super-admin-issued long-lived credentials for unattended
-- callers (nightly smoke, deploy pipelines), exchanged at
-- POST /v1/auth/api-token for a 1-hour session of subject. Only the SHA-256
-- of the secret is stored; revocable; platform-wide (not tenant-scoped).
CREATE TABLE IF NOT EXISTS api_tokens (
  token_id        CHAR(32) PRIMARY KEY,
  secret_hash     CHAR(64) NOT NULL,
  subject         VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  email           VARCHAR(255) NOT NULL DEFAULT '',
  tenant_hint     VARCHAR(64) NOT NULL DEFAULT '',
  label           VARCHAR(128) NOT NULL DEFAULT '',
  created_by      VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  expires_at_ms   BIGINT NOT NULL,
  last_used_at_ms BIGINT NOT NULL DEFAULT 0,
  revoked_at_ms   BIGINT NULL,
  revoked_by      VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  KEY idx_api_tokens_created (created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Usage budgets (R12): monthly token/cost budgets set by org admins, per
-- member (user_id) and per tenant (user_id ''). NULL tokens / cost = no limit
-- on that dimension. warned_period ('YYYY-MM') / warned_level (a percentage)
-- record the highest warning already sent that month, so each threshold warns
-- once per month whichever service replica evaluates it.
CREATE TABLE IF NOT EXISTS usage_budgets (
  tenant_id       VARCHAR(64) NOT NULL,
  user_id         VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tokens          BIGINT NULL,
  cost            DECIMAL(18,6) NULL,
  warn_at_percent INT NOT NULL,
  hard_stop       TINYINT(1) NOT NULL DEFAULT 0,
  warned_period   CHAR(7) NOT NULL DEFAULT '',
  warned_level    INT NOT NULL DEFAULT 0,
  updated_by      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  updated_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
-- §1): the name a user chose to be shown as instead of their id (usually an
-- e-mail address). Global per user, not per tenant: a person reads the same
-- in every organization. No row = the default (store.DisplayName).
-- email is the address the identity provider last asserted at sign-in ("" =
-- none asserted); an app reads its caller's own (me().account, docs/prd/
-- 2026-10-15-app-identity-schedules-notify-ai.md §3).
CREATE TABLE IF NOT EXISTS user_profiles (
  user_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  display_name   VARCHAR(40) NOT NULL,
  email          VARCHAR(320) NOT NULL DEFAULT '',
  updated_at_ms  BIGINT NOT NULL,
  PRIMARY KEY (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Content components (docs/prd/2026-10-12-experts-and-teams.md): member- and
-- platform-authored packages that are mostly text (experts, skill packs,
-- prompt packs). One row per component; (tenant, scope, owner, kind, name,
-- source_id) is unique. scope: personal | shared | org | platform; status:
-- active | pending | retired. display_name / description / category are the
-- latest version's. A shared copy (owner = the recipient, shared_by = the
-- author) and an organization publication (owner = the author) name the
-- author's component they were made from in source_id. enabled_tenants
-- restricts platform content to some organizations (NULL = every one).
-- org_name is the name of an organization row (NULL otherwise): an
-- organization has one component of a kind and name.
CREATE TABLE IF NOT EXISTS content_components (
  id              VARCHAR(64) NOT NULL PRIMARY KEY,
  kind            VARCHAR(64) NOT NULL,
  name            VARCHAR(64) NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL DEFAULT '',
  owner           VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  scope           VARCHAR(16) NOT NULL,
  status          VARCHAR(16) NOT NULL,
  latest_version  VARCHAR(64) NOT NULL,
  display_name    VARCHAR(256) NOT NULL DEFAULT '',
  description     VARCHAR(2048) NOT NULL DEFAULT '',
  category        VARCHAR(64) NOT NULL DEFAULT '',
  source_id       VARCHAR(64) NOT NULL DEFAULT '',
  shared_by       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  enabled_tenants JSON NULL,
  org_name        VARCHAR(64) NULL,
  created_at_ms   BIGINT NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  UNIQUE KEY uq_content_components_name (tenant_id, scope, owner, kind, name, source_id),
  UNIQUE KEY uq_content_components_org_name (tenant_id, kind, org_name),
  KEY idx_content_components_owner (owner, kind, updated_at_ms),
  KEY idx_content_components_scope (tenant_id, scope, kind, updated_at_ms),
  KEY idx_content_components_platform (scope, kind, updated_at_ms),
  KEY idx_content_components_source (source_id, scope)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Immutable versions of a content component: the manifest and the bundle
-- (a tar blob addressed by sha256; RetentionStore.ReferencedBlobKeys keeps
-- it from blob collection). signature is set for platform-signed versions.
CREATE TABLE IF NOT EXISTS content_component_versions (
  component_id    VARCHAR(64) NOT NULL,
  version         VARCHAR(64) NOT NULL,
  manifest_json   JSON NOT NULL,
  bundle_blob_key VARCHAR(128) NOT NULL,
  sha256          CHAR(64) NOT NULL,
  size_bytes      BIGINT NOT NULL,
  signature       TEXT NULL,
  created_by      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (component_id, version),
  KEY idx_content_versions_blob (bundle_blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Staged drafts of content components (an expert or skill pack an agent
-- drafted and another agent of the room wears for one quick try, the room
-- tools' try_draft). Never listed: no content component row exists. Scoped
-- to the job that staged it (job_id) and its room; expired rows are deleted
-- by the retention pass, then blob collection reclaims the bundle.
CREATE TABLE IF NOT EXISTS content_drafts (
  id              VARCHAR(64) PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  job_id          VARCHAR(64) NOT NULL,
  requester       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  author          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  kind            VARCHAR(64) NOT NULL,
  name            VARCHAR(64) NOT NULL,
  display_name    VARCHAR(256) NOT NULL DEFAULT '',
  version         VARCHAR(64) NOT NULL,
  manifest_json   JSON NOT NULL,
  bundle_blob_key VARCHAR(128) NOT NULL,
  sha256          CHAR(64) NOT NULL,
  size_bytes      BIGINT NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  expires_at_ms   BIGINT NOT NULL,
  KEY idx_content_drafts_expires (expires_at_ms),
  KEY idx_content_drafts_blob (bundle_blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Where a content component is enabled or pinned: scope_kind member (a
-- member's skill packs, for all their agents) or room (room skill packs and
-- pinned experts). pinned_version '' follows the latest version.
CREATE TABLE IF NOT EXISTS content_component_uses (
  scope_kind      VARCHAR(16) NOT NULL,
  scope_id        VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  component_id    VARCHAR(64) NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL DEFAULT '',
  kind            VARCHAR(64) NOT NULL,
  pinned_version  VARCHAR(64) NOT NULL DEFAULT '',
  created_by      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (scope_kind, scope_id, component_id),
  KEY idx_content_uses_component (component_id, tenant_id, scope_kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A member confirmed the declared permissions of a content component
-- version (shown and confirmed on first use).
CREATE TABLE IF NOT EXISTS content_permission_acks (
  subject         VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  component_id    VARCHAR(64) NOT NULL,
  version         VARCHAR(64) NOT NULL,
  acked_at_ms     BIGINT NOT NULL,
  PRIMARY KEY (subject, component_id, version),
  KEY idx_content_acks_component (component_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Requests to publish a version of an author's content component to the
-- organization, approved or declined by an org admin. pending_key is the
-- component id while the request is pending (NULL after), so a component
-- has at most one pending request. Deleted with the component.
CREATE TABLE IF NOT EXISTS content_publish_requests (
  request_id      VARCHAR(64) NOT NULL PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  component_id    VARCHAR(64) NOT NULL,
  kind            VARCHAR(64) NOT NULL,
  name            VARCHAR(64) NOT NULL,
  version         VARCHAR(64) NOT NULL,
  requested_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  note            VARCHAR(500) NOT NULL DEFAULT '',
  status          VARCHAR(16) NOT NULL,
  pending_key     VARCHAR(64) NULL,
  decided_by      VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  decision_note   VARCHAR(500) NOT NULL DEFAULT '',
  decided_at_ms   BIGINT NOT NULL DEFAULT 0,
  created_at_ms   BIGINT NOT NULL,
  UNIQUE KEY uq_content_requests_pending (pending_key),
  KEY idx_content_requests_tenant (tenant_id, status, created_at_ms),
  KEY idx_content_requests_component (component_id, created_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Standing shares of an author's content component with a room: the
-- room's members at share time got a copy, and a member who joins later
-- gets one too until the author revokes the share. Deleted with the
-- component.
CREATE TABLE IF NOT EXISTS content_room_shares (
  room_id         VARCHAR(64) NOT NULL,
  component_id    VARCHAR(64) NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL,
  version         VARCHAR(64) NOT NULL,
  shared_by       VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  note            VARCHAR(500) NOT NULL DEFAULT '',
  created_at_ms   BIGINT NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (room_id, component_id),
  KEY idx_content_room_shares_component (component_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Per-prompt use counts of prompt packs, per organization (built-in and
-- platform packs are used by every organization, and one's counts are not
-- another's): bumped once each time a member inserts the prompt in the
-- composer (never per keystroke); deleted with the component.
CREATE TABLE IF NOT EXISTS content_prompt_uses (
  component_id    VARCHAR(64) NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL,
  prompt_id       VARCHAR(64) NOT NULL,
  use_count       BIGINT NOT NULL DEFAULT 0,
  last_used_at_ms BIGINT NOT NULL,
  PRIMARY KEY (component_id, tenant_id, prompt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A member's favorite content components (the app gallery's star,
-- docs/prd/2026-10-13-app-gallery.md §7), per organization: built-in and
-- platform content is starred in every organization, and one's counts are
-- not another's. Deleted with the component.
CREATE TABLE IF NOT EXISTS content_favorites (
  tenant_id     VARCHAR(64) NOT NULL,
  member_id     VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  component_id  VARCHAR(64) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, member_id, component_id),
  KEY idx_content_favorites_component (component_id, tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A work version's stored cover (docs/prd/2026-10-19-work-covers.md §2.3):
-- a version published without one gets one the first time someone opens
-- it, drawn by a reader's viewer ('render') or by the image model for its
-- author ('ai', which replaces a rendered one). The bytes are a blob.
-- Deleted with the component.
CREATE TABLE IF NOT EXISTS content_version_covers (
  component_id  VARCHAR(64) NOT NULL,
  version       VARCHAR(64) NOT NULL,
  blob_key      VARCHAR(128) NOT NULL,
  mime_type     VARCHAR(32) NOT NULL,
  cover_rank    INT NOT NULL,
  created_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT NOT NULL,
  PRIMARY KEY (component_id, version),
  KEY idx_content_version_covers_blob (blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Interactive apps' documents (docs/prd/2026-10-12-interactive-apps.md
-- §5.2): one shared table for every app (app_id = the site artifact's id),
-- keyed app_id first, so one app's rows cluster together and every query is
-- a range inside its prefix. body_sealed and author_sealed are AES-256-GCM
-- under the app's data key (app_keys), written only by appdata.Cipher; the
-- rest is routing metadata. by_key is the author's per-app pseudonym (_by);
-- public marks a document every holder of the app may read. Deleted with
-- the room (crypto-shredded: the key first, then the rows in batches).
CREATE TABLE IF NOT EXISTS app_docs (
  app_id          VARCHAR(64) NOT NULL,
  collection      VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  doc_id          VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  by_key          VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  public          TINYINT(1) NOT NULL DEFAULT 0,
  author_sealed   VARBINARY(512) NOT NULL,
  version         BIGINT NOT NULL,
  body_sealed     MEDIUMBLOB NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  updated_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (app_id, collection, doc_id),
  KEY idx_app_docs_by (app_id, collection, by_key, doc_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Group chats a room's members connected to the room (docs/prd/2026-10-15-
-- app-identity-schedules-notify-ai.md §5a): by name, delivered through one
-- notifier at its own opaque address of the chat. Deleted with the room.
CREATE TABLE IF NOT EXISTS room_groups (
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  notifier      VARCHAR(128) NOT NULL,
  address       VARCHAR(512) NOT NULL,
  connected_by  VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, room_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- One-time codes that connect a group chat to a room: posted in the chat,
-- seen by the notifier's bot, spent at its bind. Deleted with the room.
CREATE TABLE IF NOT EXISTS room_group_codes (
  code          VARCHAR(32) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_by    VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  expires_at_ms BIGINT NOT NULL,
  KEY idx_room_group_codes_room (tenant_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- What a room (a project) knows (docs/prd/2026-10-16-room-memory-and-
-- evolution.md): its brief, knowledge, dated memory and lessons. Every change
-- appends a row to room_note_revisions (history and rollback); deleting a note
-- deletes its revisions. Both are deleted with the room.
CREATE TABLE IF NOT EXISTS room_notes (
  tenant_id      VARCHAR(64) NOT NULL,
  room_id        VARCHAR(64) NOT NULL,
  note_id        VARCHAR(32) NOT NULL,
  type           VARCHAR(16) NOT NULL,
  title          VARCHAR(480) NOT NULL,
  body           TEXT NOT NULL,
  status         VARCHAR(16) NOT NULL,
  supersedes     VARCHAR(32) NULL,
  sources_json   TEXT NULL,
  author         VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  pinned         TINYINT(1) NOT NULL DEFAULT 0,
  revision       INT NOT NULL,
  created_at_ms  BIGINT NOT NULL,
  updated_at_ms  BIGINT NOT NULL,
  verified_at_ms BIGINT NULL,
  PRIMARY KEY (tenant_id, room_id, note_id),
  KEY idx_room_notes_room (tenant_id, room_id, status, type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS room_note_revisions (
  tenant_id  VARCHAR(64) NOT NULL,
  room_id    VARCHAR(64) NOT NULL,
  note_id    VARCHAR(32) NOT NULL,
  revision   INT NOT NULL,
  type       VARCHAR(16) NOT NULL,
  title      VARCHAR(480) NOT NULL,
  body       TEXT NOT NULL,
  status     VARCHAR(16) NOT NULL,
  pinned     TINYINT(1) NOT NULL DEFAULT 0,
  changed_by VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  at_ms      BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, room_id, note_id, revision)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Files an interactive app's pages uploaded (app.files): the bytes in the
-- blob store (blob_key, like an attachment's), the name and the uploader's
-- identity sealed under the app's data key, the rest routing metadata.
-- Deleted with the app's data (a wipe) and with the room, like app_docs.
CREATE TABLE IF NOT EXISTS app_files (
  app_id          VARCHAR(64) NOT NULL,
  file_id         VARCHAR(64) NOT NULL,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  by_key          VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  public          TINYINT(1) NOT NULL DEFAULT 0,
  name_sealed     VARBINARY(1024) NOT NULL,
  author_sealed   VARBINARY(512) NOT NULL,
  content_type    VARCHAR(128) NOT NULL,
  size_bytes      BIGINT NOT NULL,
  blob_key        VARCHAR(128) NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  PRIMARY KEY (app_id, file_id),
  KEY idx_app_files_blob (blob_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Each app's data key, wrapped by the deployment key (internal/sealbox),
-- created on the app's first write. An empty wrapped_key is a key being
-- destroyed: the app takes no writes until its documents are gone.
-- Deleting the row (or emptying wrapped_key) makes every copy of the app's
-- documents unreadable.
CREATE TABLE IF NOT EXISTS app_keys (
  app_id          VARCHAR(64) NOT NULL PRIMARY KEY,
  tenant_id       VARCHAR(64) NOT NULL,
  room_id         VARCHAR(64) NOT NULL,
  wrapped_key     VARCHAR(255) NOT NULL,
  created_at_ms   BIGINT NOT NULL,
  KEY idx_app_keys_room (tenant_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
