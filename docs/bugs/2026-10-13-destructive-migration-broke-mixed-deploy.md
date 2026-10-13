# A destructive migration broke sign-in during a mixed deploy (2026-10-13)

## Symptom

During the rollout of the share-link sign-in release (482bd0a), sign-in and
session refresh returned 500 for every member. Every tenant read failed:

```
SELECT tenant_id, name, status, sso_issuer, …, public_shares_disabled, … FROM tenants …
Error 1054: Unknown column 'public_shares_disabled' in 'field list'
```

## What happened

1. Migration `2026_10_share_link_access` added `tenants.anonymous_links_disabled`
   and **dropped `tenants.public_shares_disabled` in the same release** that
   stopped reading it.
2. The backends started on the new image and ran the migration (every service
   runs `runMigrations` at startup against the shared database).
3. The new gateway refused to start: `BNBLOC_SITES_ORIGIN` named the main
   host (`BNBLOC_PUBLIC_URL`'s), which `ParseSitesOrigin` treated as a fatal
   startup error. The value was harmless: it means the same as unset (sites
   served in place, sandbox-only).
4. The orchestrator kept the old gateway serving. The old gateway's tenant
   reads named the dropped column, so every request that reads a tenant
   (sign-in, refresh, the tenant gate) failed with 500.

4b2a593 relaxed the sites-origin check (a value on the main host is logged
and treated as unset), which let the new gateway start and ended the outage.

## Root causes

1. **A destructive schema change was not deploy-safe.** Services start, and
   migrate, at different times; for a while old and new code run against one
   schema. A migration that drops (or renames, or narrows) something the
   previous release still reads breaks that previous release the moment it
   runs. The repo's "pre-launch, no compatibility code" rule was read as
   covering schema steps across a deploy; it covers code paths only.
2. **A startup check was fatal for a harmless value.** A well-formed value
   equivalent to unset stopped the new gateway, which turned a short skew
   into an outage. A fatal check is for a dangerous or malformed value.

## Fix

- **Expand/contract rule** (AGENTS.md, "Deploy/rebuild traps"): a migration
  only adds. A drop, rename, narrowing or NOT NULL without a default lands in
  a later release than the code that stopped using the column or table.
- **`TestMigrationsAreDeploySafe`** (`internal/store/deploy_safe_test.go`)
  enforces it on every migration newer than the `released` marker of
  `migrations.lock`; a drop passes only when the lock recorded the name as
  `unreferenced` when that release was cut.
- **Startup checks reviewed**: see "Startup checks" below.
- **Version skew rule**: AGENTS.md and `docs/production-deployment.md`
  describe how to verify that one service a release behind returns no 500
  because of schema.

## Migrations since 2026-10-11, reviewed for the same hazard

All of these already ran on QA; their ids are locked and immutable, so none
is edited. They are the released baseline (`released 2026_10_artifact_app_sdk`
in `migrations.lock`). "Unsafe" means: in a mixed deploy the previous
release's code fails against the migrated schema. The previous-release
reference was checked at each migration commit's parent.

| Migration | Change | Mixed deploy |
| --- | --- | --- |
| `2026_10_data_retention` | nullable columns, indexes | safe |
| `2026_10_agent_job_dispatch` | NOT NULL DEFAULT columns, index | safe |
| `2026_10_content_components` | new tables | safe |
| `2026_10_agent_job_expert` | NOT NULL DEFAULT columns | safe |
| `2026_10_agent_jobs_expert_usage` | index | safe |
| `2026_10_prompt_packs` | `DROP TABLE prompts` with the code that read it (8228c02) | **unsafe**: the old prompt library routes fail (1146) |
| `2026_10_content_sharing` | columns, a unique key widened by `source_id`, new table | safe |
| `2026_10_notification_prefs_content` | NOT NULL DEFAULT columns | safe |
| `2026_10_content_room_shares` | new table | safe |
| `2026_10_content_platform_tenants` | nullable column | safe |
| `2026_10_content_org_names` | nullable column, data rewrite (duplicates retired), unique key over a NULL-able column | safe (no 500; duplicate org rows unpublished) |
| `2026_10_agent_jobs_room_expert` | NOT NULL DEFAULT columns | safe |
| `2026_10_content_drafts` | new table | safe |
| `2026_10_agent_prefs_owner` | NOT NULL DEFAULT column | safe |
| `2026_10_agent_messages_usage` | NOT NULL DEFAULT / nullable columns | safe |
| `2026_10_identity_columns_bin` | collation to `utf8mb4_bin`, type kept | safe (comparisons become exact) |
| `2026_10_store_indexes` | indexes | safe |
| `2026_10_tenant_issuer_unique` | `sso_issuer` made NULL-able and `''` rewritten to NULL (a2d5877) | **unsafe**: the old code scans `sso_issuer` into a `string`; every tenant without an issuer fails to read ("converting NULL to string"), the same blast radius as this incident |
| `2026_10_drop_unused_columns` | drops `agent_sessions` snapshot columns, `sync_states` counters, `agent_messages.channel_id` (96a59e5) | **unsafe**: the old activity's `PutSessionState` inserts `provider_session_id`, `owner_user_id`, `state_json`, `updated_at_ms` (1054), so session-state reports fail |
| `2026_10_room_id_width_json_columns` | `room_id` VARCHAR(128)→(64), TEXT→JSON | safe in practice: old writers pass JSON text and ≤64-byte room ids (a narrowing all the same) |
| `2026_10_bridge_keys_bin` | collation to `utf8mb4_bin` | safe |
| `2026_10_drop_legacy_borrow_and_submit` | `DROP TABLE shared_agent_leases`, drop `agent_messages.client_submit_id` (ff66fbb) | **unsafe**: the old activity's reply append inserts `client_submit_id` (agent replies fail), the old `DeleteRoom` deletes from `shared_agent_leases` (room deletion fails) |
| `2026_10_agent_jobs_attempts` | nullable / NOT NULL DEFAULT columns, index | safe |
| `2026_10_content_use_counts_tenant` | `content_prompt_uses` dropped and recreated with `tenant_id NOT NULL` (77f3c74) | **unsafe**: the old insert names no `tenant_id` (1364), so recording a prompt use fails |
| `2026_10_app_data` | new tables | safe |
| `2026_10_share_link_access` | adds two columns, drops `tenants.public_shares_disabled` (482bd0a) | **unsafe**: this incident |
| `2026_10_artifact_app_sdk` | NOT NULL DEFAULT column | safe |

## Startup checks

Every fatal check in `services/*/cmd/main.go` and what they call at startup
(`internal/config`, `svcauth`, `membership`, `store`, `shard`, `sealbox`,
`obs`, `search`, `notify`, `blobstore`, the gateway's resolvers). Rule
(AGENTS.md): fatal for a dangerous or malformed value, never for one that
means the same as unset.

Downgraded (a harmless value no longer stops a service):

| Check | Before | Now |
| --- | --- | --- |
| A dev-only switch set to an explicit off in production (`BNBLOC_DEV_DISCOVERY=false`/`0`/`no`/`off`) | fatal: `config.RefuseInProduction` refused any non-blank value | the same as unset. On (or malformed) still refuses. `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL` had its own copy of the rule (a malformed value read as off); it now goes through the same guard |
| `BNBLOC_SITES_ORIGIN` on the main host | fatal | logged, treated as unset (4b2a593) |
| `BNBLOC_SITES_ORIGIN` with one trailing slash | fatal | the same origin (canonical form without it) |
| `BNBLOC_METRICS_ADDR` equal to an internal service's own listen address | bind conflict, the service exited | metrics on the main listener, as when unset (logged). On the gateway it refuses with a clear error: it would publish `/metrics` |
| `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS=yes`/`no`/`on`/`off` | fatal (`strconv.ParseBool`, unlike every other switch); `_PATH_STYLE=yes` silently disabled attachments | read by `config.StrictBool` (the repo's switch words, a superset of `strconv`'s); any other value is still fatal |
| Whitespace-only `BNBLOC_SEARCH_OPENSEARCH_URL` / `_INDEX_PREFIX` | fatal ("not an http(s) URL") | trimmed: unset |

Kept fatal:

| Check | Why |
| --- | --- |
| Production without `BNBLOC_INTERNAL_KEY`, `BNBLOC_SESSION_SIGNING_KEY`, `BNBLOC_MYSQL_DSN`, `BNBLOC_ROOM_BASE_URL` | dangerous: each selects a dev fallback (unsigned identity, ephemeral keys, memory store, empty directory) |
| A dev-only switch on in production | dangerous |
| Malformed `BNBLOC_SECRET_STORE_KEY`, VAPID key pair (half set, not P-256, mismatched), OpenSearch URL/prefix/half credentials, `BNBLOC_TRUSTED_PROXIES` entry, `BNBLOC_AUTH_ACS_PATH`, sites origin (http, path, query, credentials), AWS config secret (unreadable, non-string value) | malformed: the operator meant something the service cannot do |
| DSN charset/collation not utf8mb4, or a `character_set_*`/`names` session variable | the store owns the session charset; `names` resets the connection collation, so even a "utf8mb4" value is not provably harmless |
| `BNBLOC_ARTIFACT_MAX_BYTES=0` | ambiguous (default or unlimited) |
| `<P>_PEERS` without `<P>_SELF`, `_SELF` without `_PEERS`, `_PEERS=","`, realtime peers with the disk blob store | an ambiguous or unsafe replica set: with two replicas, either half alone would let each own every key |
| OpenSearch unusable at startup in production | search would fail for every member |
| Bridge without `BNBLOC_GATEWAY_BASE_URL`/`BNBLOC_BRIDGE_OWNER`, unknown `BNBLOC_SANDBOX_MODE` | malformed (the desktop writes them) |
| Listener bind failures, MySQL unreachable, a failed migration | the service cannot run |

Seen in the review, not startup checks, left as they are:
`BNBLOC_BRIDGE_SERVE_ALL_OWNERS` is on for any non-empty value (`false`
too), and `BNBLOC_EXT_ALLOW_UNSIGNED` has no production guard (the
production overlay pins it empty).

## Status

Fixed 2026-10-13.

- 4b2a593: the sites-origin check that stopped the gateway (the outage).
- 16812a0: the startup-check review (the downgrades above).
- b6ddb5a: the expand/contract rule, `TestMigrationsAreDeploySafe`, the
  `released 2026_10_artifact_app_sdk` baseline, the version-skew rule.

Verified: with the marker moved back to `2026_10_app_data` the test
refuses `2026_10_share_link_access` ("drops tenants.public_shares_disabled
…, which the released code may still use"); moved back to the first
migration it refuses the unsafe migrations above, the rewrite in
`2026_10_room_id_width_json_columns` (it would need a `deploy-safe`
reason), four older drops and rewrites (`2026_10_converge_fresh_schema`,
`2026_10_drop_devices`, `2026_10_drop_member_notifier_settings`,
`2026_10_notification_outbox_per_notifier`), and none of the additive
migrations.
