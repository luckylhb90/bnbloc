# Code-health remediation: one rule, one owner (2026-10-12)

Status: planned. No code has landed yet. Update each batch's status and
landing commits here as the work lands.

## 1. Context

- **The bug.** The effective tenant was decided in several gateway handlers,
  and only some of them honored the acting-tenant switch. As a result, saved
  content disappeared from the Library
  (`docs/bugs/2026-10-12-acting-tenant-ignored-by-gateway-handlers.md`,
  fixed in 3abf760). AGENTS.md now has **"One rule, one owner (enforced)"**:
  every rule has exactly one owner function or package, the owner is named,
  and an architecture test fails if the rule is written again somewhere else.
- **The reviews.** Six read-only reviews were run on 2026-10-12, covering
  store, gateway, backend services, bridge/core/sandbox/cmd, web and desktop.
  They found about 45 high, 110 medium and 100 low findings. Most of the
  highs are the same defect as the tenant bug: a rule with several copies
  that have drifted apart.
- **Spot-check.** Every high finding was re-checked against `main` at
  17dabf3. All of them held (see §7).
- **Product-owner decisions (2026-10-12)** apply throughout. Details are in
  §5:
  - visitors are read-only;
  - subjects are case-sensitive;
  - pre-launch, so all compatibility and legacy code is removed in the
    batch that touches it, with no data migrations or backfills.

**Reference keys.**
- `store`: internal/store.
- `gw`: services/gateway.
- `svc`: realtime, activity, room, shareagent, notify, relay and the shared
  internal packages.
- `bridge`: bridge, core, sandbox and cmd. Its findings are numbered 1–51.
- `web`: web/src.
- `desk`: apps/desktop.

`H`/`M`/`L` + number is the finding's severity and number in that review.

**Sizes** (one agent, including tests): S ≤ 1 day, M = 2–3 days,
L ≥ 1 week.

**Migrations.** Every schema change gets a **new** migration id, because
deployed ids are immutable. Each one also means:
- updating both `schema.sql` copies;
- running `go test ./internal/store -run TestMigrationLock -update`;
- adding a dropped table to `retiredTables` (`schema_ddl.go`) with the id of
  the migration that drops it.

The migration ids below are illustrative.

## 2. Cross-cutting themes and their owners

The same root cause appears in several reviews. After the fix, each theme
has exactly one owner, and every caller goes through it.

| # | Theme | Copies today | Owner after the fix |
|---|---|---|---|
| T1 | Room membership and role ("may chat", human vs agent host, visitor, role abilities) | svc H2, H3, M5, M6, M10, M11, M17; gw M6; web H7, H8; store L4 | `membership.Member` methods `IsHuman()`, `MayChat()`, plus the role abilities from `store.Role`, obtained only through `membership.FromEnv`. Gateway: one cached `roomMembership`. Web: `lib/roomRoles.ts`, parity-tested against Go. |
| T2 | Identity equality (users, agents, targets) | store H5, H6; svc H6, M16; bridge 12, 14; web M12–M14 | Subjects compared with exact `==` (no fold anywhere). `wire.SameAgent` / `wire.AgentHandle` / `wire.ParseAgentHandle`. `shareagent.PlatformTarget` is the only `shared-agent:` codec. TS: `agentIdentity` / `agentHandle` (`lib/roomAgents.ts`) and an exact `sameUser` (`lib/format.ts`). |
| T3 | Tenant scoping | gw H1, H3–H5, M2, M19, L14; svc H4, M18; store H3, H7, H8, M2, M4; web H1, H2 | Gateway: `tenant_scope.go` (`roomClaims` variants, `contentGrant`), plus `resolveSession` with one `mintSession`. Backends: `membership.RoomScope`. Store: the tenant-isolation contract over both backends. Web: `useActingTenant()` in `lib/session.ts`. |
| T4 | Store reads that swallow errors; result semantics | store H1, H2, M5, M15, L1; svc H5 | Store interface reads return `error`. `queryAll[T]`. One duplicate-key mapper in `mysql.go`. Exported sentinels. `ClientFoundRows`. |
| T5 | Retryability and refusal classification | bridge 1, 2, 11, 21, 22; svc H5; store M5 | `store.AgentJobFailureReason.Retryable()` owns which reasons retry. The bridge's single `refusal` type and `fail(stage, err)` own which reason a failure gets. `core.Retryable` takes a `*bool` hint. |
| T6 | Hand-rolled signed internal requests | gw M1, L2; svc M7, M8, L10; bridge 36 | `svcauth.NewRequest`, `svcauth.Subject`, `svcauth.RequireService`. The gateway's `backend_client.go` is built on them. |
| T7 | Event kinds as string literals; payload shapes | svc M13–M15, M19–M21; web H5, M11; bridge 10 | A kind registry in `internal/wire` (`wire.RoomEventKinds` with attributes: durable, replay, revocation, channel-scoped). Also `wire.JobNotice` and `wire.DecodePayload[T]`. |
| T8 | Go↔TS (and Rust) wire drift | web H4, M19, M26–M28, L10, L11; gw M15, M18, L8; desk H5 | Projections and limits in `internal/wire`, generated into `web/src/lib/wire.gen.ts`, plus golden fixtures in `internal/wire/testdata/`. Rust decodes unknown kinds instead of failing. |
| T9 | Error, 5xx and response mapping | gw M10–M14, L5; svc H5, M12, L13; web M2–M4; desk M8 | `internal/httpx` (`WriteJSON`, `WriteError` with one body shape, `DecodeJSON`, `Unavailable` → 503) plus `obs.WriteStatus`. Web: `ApiError` + `errorText` / `statusText`. Desktop: `GatewayClient`. |
| T10 | Route permission and rate policy | gw H2, M3–M5; web H8 | A gateway route table `s.handle(pattern, perm, rate, h)` over `requiredPermission` / `rateLimited` / `rbac.Can`. Web: `lib/permissions.ts`, generated from `rbac`. |
| T11 | Workspace paths and the credential boundary | bridge 3–6, 19, 20, 23, 32, 33, 48; desk H3 | `core.BnblocOwnedPath` + `core.HostExecutableProjectConfig`; `protocol.HostConfineFromEnv` / `PassthroughFromEnv`; one vmexec `credentialPaths` list. |
| T12 | Grants, visibility, budgets | store H5, M11; svc M2, M3; gw M7, M16 | `store.Grant.Live(at)` and one admission rule; `extpipe.MemberVisibility`; `wire.TurnUsageFromMetrics`, applied once when a message is appended. |
| T13 | Package, artifact and digest rules | gw M8; bridge 7–9, 25–31, 34, 35, 50; desk H1, M4, M11, M12 | `protocol.CompatibleRange`, `protocol.KnownCapability`, `coreset.EntryFor` / `coreset.EnvKey` (with a fixture shared with Rust), `pkgmeta` for building and parsing artifact keys, one digest package, `tarbundle.Cache.LoadPinned`, `internal/semver`. |
| T14 | Memory vs MySQL behaviour | store H8, M1–M10, M12–M14, L7–L12 | One `forEachBackend` rig and one contract suite per store interface. |

## 3. Batches

Batches are ordered by risk first, then by leverage. In Batch 1, each
sub-batch lands on its own, in the order 1A → 1F. Legacy and compatibility
removals are folded into the batch that touches the area, per decision D3.

### Batch 1: security and data-correctness highs

#### 1A. Credential and sandbox boundary (M–L)

Status (bridge/core/vmexec rows): landed. Workspace path policy 71eb791
(`core/workspace_paths.go`: `BnblocOwnedPath`, `HostExecutableProjectConfig`,
`ProtectedWorkspacePath`; the whole `.claude/` dir counts as host-executed,
matched case-insensitively; one shared table
`core/testdata/workspace_paths.json`). `~/.bnbloc` read-deny, vmexec hook
exact parse, hooks exit 2: 67fea65 (the desktop cache dir stays readable: it
holds the stdio MCP tool binaries a confined CLI spawns). Tests:
`TestBnblocOwnedPathAgreesEverywhere`, `TestWorkspacePathTable`,
`TestWorkspacePathPolicySingleOwner`, `TestIgnoreMatcherExcludesProtectedPaths`,
`TestPackDirSkipsProtectedPaths`, `TestConfineDeniesBnblocCredentialPaths`,
`TestHookRewritesCommandsMerelyPrefixedByVMExec`. O2 (unreadable sandbox
policy fails closed): eb77683. Desktop and dev-discovery rows: other agents.

| Item | Ref | Fix | Size |
|---|---|---|---|
| Host-executed project config can be planted, and is synced | bridge 3, 33 | Split `core.AgentConfigPaths` into `BnblocOwnedPath` (`.room/` plus the config bnbloc writes) and `HostExecutableProjectConfig` (`.claude/settings*.json`, `.claude/agents/`, `.claude/commands/`, `.codex/`, `opencode.json`, …). Both lists apply to: sandbox deny-write, the sync ignore (which also adds `.room/`), `resetAgentConfig`, publish/sites, and `save_component`'s `packDir`. Fix `core/profile_test.go:50`. | M |
| `~/.bnbloc` is readable under confinement | bridge 4 | Add `.bnbloc` to one exported `credentialPaths` list (`confine.go:167`). A test reads the desktop identifier from `tauri.conf.json`. | S |
| vmexec hook skips any command that merely starts with the vmexec path | bridge 5 | Skip only an exact parse of `<vmexec> run [--stdio] -- <word>` (`hook.go:55`). | S |
| Claude-dialect hook fails open | bridge 6 | Exit 2 on decode/encode failure instead of 125 (`hook.go:72,133`). Confirm the harness's blocking exit code while fixing. | S |
| Desktop secrets are shared across members | desk H3 | Keep plugin settings, MCP settings and OAuth sign-ins in a per-member directory, keyed by a hash of (gateway origin, subject). On first start the old unkeyed directories are deleted, with no migration. | M |
| OAuth refresh can undo a sign-out | desk H4 | Every write or delete of a sign-in takes a per-tool lock. Refresh re-reads the token and compares it before saving. | S |
| Uninstall cleanup only works within one app session | desk H2 | Each successful reconcile sweeps `plugin-settings/`, `mcp-settings/`, `mcp-settings/oauth/`, `cores/` and `sandbox/` against the live catalog. | S |
| Orphan kill trusts the spawn record alone | desk H6 | One `owned_orphan(record, exe, root)` that requires both the pid match and an executable under the cache root. | S |
| Dev discovery is allowed in production | gw H6, L1 | `BNBLOC_DEV_DISCOVERY` becomes fatal at startup under `BNBLOC_ENV=production`. Delete the daemon-token bypass, which has no client (`service.go:665-679,763-786`, `localagent.go:308-315`), and fix `docs/bridge.md`. | S |

- **Status (row "Dev discovery is allowed in production" only):** landed in
  0b7bfda with `TestDevDiscoveryRefusedInProduction` and
  `TestNoDaemonTokenBypass`; the other 1A rows belong to other batches of
  work.
- **Owners:** `core.BnblocOwnedPath`, `core.HostExecutableProjectConfig`,
  vmexec `credentialPaths`, desktop `member_dir`, `owned_orphan`.
- **Tests:**
  - `TestBnblocOwnedPathAgreesEverywhere`
  - `TestConfineDeniesBnblocCredentialPaths`
  - `TestHookRewritesCommandsMerelyPrefixedByVMExec` (plus exit 2 on bad input)
  - `TestDevDiscoveryRefusedInProduction`
  - desk `uninstalled_while_closed_is_forgotten`
  - desk `orphan_requires_exe_evidence`
  - desk `sign_out_and_refresh_do_not_race`
- **Deploy:** bridge and `bnbloc-vmexec` (shipped in the desktop release),
  desktop app, gateway image.
- **Migration:** none.
- **Status (desktop rows, 2026-10-12): landed.**
  - desk H3 (3c19ed8, scope fix in the follow-up commit): `member_dir`
    keyed by sha256(gateway origin, subject, tenant), from the session
    token's `sub` and `tenant` claims (no tenant claim, no member dir). It
    holds plugin settings, MCP settings and sign-ins, and the sandbox data
    dirs. The bridge gets the manager's dir only when it runs for that
    same member and tenant; a dir change restarts the bridge (stabilizing
    watcher), the plugins with local settings and the sandbox. The unkeyed
    dirs (`<app config>/plugin-settings`, `mcp-settings`,
    `<app data>/sandbox`) are deleted at startup.
  - desk H2 scope rule: the sweep judges member state only against the
    catalog of the session it was fetched with (same member dir);
    machine-wide sweeping is limited to rebuildable caches (`cores/`,
    sandbox bundles). Test: `a_tenant_switch_never_sweeps_the_other_tenants_state`.
  - desk H4 (0160e60): `SignIns` (per-tool lock, `replace_if_current`);
    `sign_out_and_refresh_do_not_race`.
  - desk H2 (6f24923): `sidecar_sweep` after every successful reconcile;
    the in-memory uninstall paths are deleted;
    `uninstalled_while_closed_is_forgotten`.
  - desk H6 (1fbae0a): `owned_orphan` for fixed-port sidecars and the
    sandbox; the exe-only path and `recorded_orphan` are deleted;
    `orphan_requires_exe_evidence`.
  - Owners named in AGENTS.md and pinned by `internal_ext/owners_tests.rs`
    (b3f99f1).

#### 1B. Authorization, membership and tenant scope (L)

| Item | Ref | Fix | Size |
|---|---|---|---|
| A removed member keeps a live stream | svc H1 | Add `RoomClient.Forget(room, user)`, called when realtime delivers `member_removed` / `room_deleted`. Stream opens use an uncached lookup, and `handleEvents` re-checks membership at each heartbeat. The gateway's positive cache is cleared on the same signal. | M |
| Visitors can chat, drive and borrow agents, and create schedules | svc H2, M5 | Add `membership.Member.MayChat()` (human and not a visitor), per D1. It is used for realtime chat, typing, schedule create, the dispatch requester check, and shareagent `mayChat`, authorize, reserve and quota requests. Enforce it **inside** `PostChat` (`chat_send.go:97`) so that `handleRunSchedule` and `handleInternalScheduledPost` can no longer skip it. | M |
| Agent hosts get through shareagent's human-only routes | svc H3 | `requireMember` / reserve use `IsHuman`. Platform grants get their own id namespace, so `PUT`/`DELETE` of an authorization can neither overwrite nor revoke one. | S |
| Membership has two tenant meanings | svc H4 | The membership probe returns the room's tenant. Backends refuse a signed tenant that differs, through `membership.RoomScope`. Drop the misleading tenant parameter from `Lookup`. | M |
| Fail-open membership check | svc M10 | `handleRoomAgentJobs` (`bridge_jobs.go:409`) fails closed and normalizes `roomId`. | S |
| Viewers can call `POST /v1/local-agent/run` | gw H2 | Gate it through the existing `requiredPermission` / `rateLimited` (`PermCreateRoom`, `PermDriveAgent`). Batch 2.1 turns this into a route-table row. | S |
| `/v1/rooms/{id}` itself ignores the room's tenant | gw H1 | Add `apiPath.namesRoom()` (room id set, not `import`) and apply the room tenant whenever a path names a room. The reach check stays for sub-resources only. Cover `/v1/rooms-unread?rooms=` and `/v1/search`. | S |
| The job grant is honored only by opt-in handlers, and bundle downloads use a different copy | gw H3, H4, M2 | One owner: `contentGrant(r, claims) (contentActor, error)` in `content_requester.go`. `contentCaller` takes a grant mode. Handlers either act as the requester or refuse `?job=` with 400 (per-handler scope in §5, O6). `contentBundleEntitled` / `artifactEntitled` take the resolved actor. Named `roomClaims` variants are the only callers of `roomTenantOr`. | M |
| Session minting bypasses `resolveSession` | gw H5 | `handleSwitchTenant`, `handleAPITokenExchange` and `mintAdminToken` mint through one `mintSession`. The chosen tenant is stored on the refresh family's existing `tenant_hint`, so refresh, the bridge fork and the CLI keep it. Delete the web's re-apply (`sessionGrant.ts:120-150`). | M |
| The web Organization console overrides the acting tenant; the acting tenant survives logout | web H1, H2 | `useActingTenant()` in `lib/session.ts` owns the key (scoped by subject and gateway), the tenant list, the setter and the cleanup on logout. Delete `scoped()` and `?tenantId=` from the web, **and** the `?tenantId=` input from `tenant_scope.go`, which has no non-web caller. The header becomes the only input. | M |

- **Status:** landed: 48d6041 (membership owner, svc H1–H4, M10), 0b7bfda
  (namesRoom, local-agent gate, dev discovery), 244b1a6 (contentGrant), 4ce973d
  (mintSession and switch persistence, useActingTenant, no `?tenantId=`,
  `lib/roomRoles.ts`). The web composer's visitor gating stays in 5.2.
- **Owners:** `membership.Member.MayChat` / `IsHuman`, `RoomClient.Forget`,
  `membership.RoomScope`, `apiPath.namesRoom`, `contentGrant`, `mintSession`,
  `useActingTenant`.
- **Tests:**
  - `TestRevocationClosesAccess`
  - `TestHumanOnlyRoutes`, with visitor and agent-host rows in every service
  - `TestNoMembershipFailOpen`
  - `TestEffectiveTenantSingleOwner`, extended to forbid any read of
    `tenantId`
  - `TestProxyRoomTenantEveryRoomPath`
  - `TestJobGrantHonoredOrRefused`
  - `TestRoomGrantDetailBundleDownloadable`
  - web boundary rule 7
- **Deploy:** gateway, room, realtime, activity and shareagent images; web
  bundle (gateway and desktop).
- **Migration:** none.

#### 1C. Store data correctness and identity (L)

Status: done (2026-10-12). All ten rows landed:
- [x] H1 tenant reads return errors, `EnsureMember`, login fails on a store
  error, org ceiling fails closed (b8165b5);
- [x] H2 `clientFoundRows`, workarounds deleted, `requireMatched` (b8165b5);
- [x] H3 `PutArtifact` insert-only, `ErrArtifactIDTaken` (b8165b5);
- [x] H7 `AddMember` from the tenant's room, `PutChannel` plain insert
  (b8165b5);
- [x] H5 usage normalized at append into typed columns,
  `store.ReplyUsageOf` (7ce3307);
- [x] H9 `sync_states` row locked, fresh only when stored (7ce3307);
- [x] H4 `roomOwnedTables` registry, memory cascade (c501e25);
- [x] H8 tenant-isolation suite over both backends (cd29e9e);
- [x] svc H5 `writeJobStoreError`, unknown-job sentinel parity, realtime
  schedule 5xx logged (41142f4);
- [x] H6 / D2 exact identity in store, services, internal, bridge
  (dfa237e) and web (795236a).

Migrations: `2026_10_agent_messages_usage`, `2026_10_identity_columns_bin`.
Deviations: the gateway's `TenantMemberRole` hook keeps its
`(role, found)` shape (a read error is logged and refuses) so 1B's
switch-tenant rework is not touched; realtime canonicalizes
`recipient.ownerId` by trimming (exact ids have no other canonical form);
agent-handle and core/provider folds in the web (`roomEvents` agentKey,
composer handle matching, `samePlatformAgent`) are not subjects and stay
for the 5.2 `agentHandle`/`agentIdentity` owner. `RoomStore` and the other
allowlisted reads keep bool/no-error results until 4.2.

| Item | Ref | Fix | Size |
|---|---|---|---|
| A DB error at login rewrites an org role | store H1 | `GetMember`, `MemberCount`, `TenantForIssuer` and `TenantUsage` return `error`. Provisioning fails the login on error. The new-member path uses an insert-only `EnsureMember` (`INSERT IGNORE`, then read back). The shareagent org-ceiling check fails closed. | M |
| An update that changes nothing reports "not found" | store H2 | Set `cfg.ClientFoundRows = true` (`factory.go`), after checking the `ON DUPLICATE KEY UPDATE` sites that read `RowsAffected`. Delete the three workarounds (`room_schedules.go:396`, `mysql_tenant.go:149`, `requireUpdated`). | S |
| `PutArtifact` crosses rooms and tenants | store H3 | `INSERT IGNORE`, read back, and require the same tenant and room (`ErrArtifactIDTaken`), in both backends. | S |
| `DeleteRoom` leaves room-owned rows behind | store H4 | One room-owned-table registry, used by `DeleteRoom` and its test. It adds `message_tasks`, `content_room_shares`, `content_drafts`, `notification_reply_tokens`, `shared_agent_quota_requests`, and room-scoped `content_component_uses`. Memory gets the same behaviour. | M |
| The budget hard stop can be bypassed (raw JSON sums) | store H5 | Normalize usage once at append with `wire.TurnUsageFromMetrics` into typed `agent_messages` columns. Both stores sum those columns, and the `JSON_EXTRACT` path is deleted. | M |
| Cross-tenant upserts on tenant-less keys | store H7 | `AddMember` becomes `INSERT … SELECT FROM rooms WHERE tenant_id=? AND deleted_at_ms IS NULL`. `PutChannel` becomes a plain `INSERT`. | S |
| A concurrent report can publish a reply twice | store H9 | `SELECT … FOR UPDATE` on the `sync_states` row. A message counts as fresh only when its insert affected one row. | S |
| The tenant-isolation suite runs on memory only | store H8 | Run the existing suite over MySQL now. The full rig is Batch 4. | S |
| Store failures are reported as 409 | svc H5, store M5 (job part) | Only the store's lease sentinels map to 409. Everything else goes through `obs.ServerError` and 500, including realtime's schedule status branch. An unknown job returns the same sentinel from both backends. | S |
| Identity comparison is undefined | store H6, svc H6, bridge 12, web M14 | Per D2: exact `==` everywhere. Delete every `EqualFold` / `ToLower` on identities: about 20 store sites, the svc kick / quota / borrower / budget key, bridge `sameUserID`, and the web `sameUser` copies. Realtime canonicalizes `recipient.ownerId` to a member id at post. Identity columns move to `utf8mb4_bin`. | M |

- **Owners:** `EnsureMember`, `ErrArtifactIDTaken`, the room-owned-table
  registry, `wire.TurnUsageFromMetrics` (single use site), exact identity
  equality.
- **Tests:**
  - `TestRoomOwnedTablesDeletedWithRoom` (store T3)
  - Contract cases: an identical update succeeds; a re-put into another
    tenant or room is refused; case-variant ids are **distinct** in both
    stores; concurrent `AppendMessages` publishes once
  - `TestStoreErrorsNotConflict`
  - `TestStoreReadsReturnErrors`, starting with an allowlist that only
    shrinks
  - New `TestNoIdentityFold`: a static scan that forbids `EqualFold` /
    `ToLower` on subject, owner, author or user identifiers, allowlisted with
    a reason
- **Migrations:**
  - `2026_10_identity_columns_bin`: identity columns (user, owner, author,
    subject, created_by, …) get `COLLATE utf8mb4_bin`. No backfill.
  - `2026_10_agent_messages_usage`: typed usage columns. No backfill.
- **Deploy:** all service images (migrations run at startup), web bundle.

#### 1D. Job retry and refusal classification (M)

Status: landed in 017ed7d. `services/bridge/job_failure.go` owns the
classification (`refuse`, `failJob`, `atStage`, `driveFailureReason`);
`Retryable()` is now only `core_unavailable`, `runner_start_failed`,
`turn_timeout`; `runner_failed` and `reply_timeout` are final;
`runner_killed` and the substring classifier are deleted. New final reasons
`content_unavailable`, `team_unsupported`, `invalid_job`. Test:
`TestDriveRefusalsAreFinal` (+ `TestDriveStartFailureClassification`).

| Item | Ref | Fix | Size |
|---|---|---|---|
| A job retry re-runs a prompt that already started | bridge 1 | Classify failures by drive stage. A failure before start where `core.Retryable(err, false)` holds becomes `core_unavailable` / `runner_start_failed` (retryable). A started or ambiguous turn, and `reply_timeout`, get a non-retryable reason. Delete the substring classifier `classifyJobFailure` (`bridge.go:908`). Borrow quota is then reserved at most once per started turn. | M |
| Deterministic refusals are reported as retryable | bridge 2 | Add final reasons `content_unavailable`, `team_unsupported` and `invalid_job` (a `VARCHAR(64)` column, so no migration). Each refusal site reports its own reason. | S |

- **Owners:** `AgentJobFailureReason.Retryable()` (unchanged owner, new
  reasons) and a stage-aware failure mapper in the bridge. Batch 3.1 turns
  that mapper into the `refusal` type.
- **Tests:** `TestDriveRefusalsAreFinal`.
- **Deploy:** bridge (desktop release), activity.
- **Migration:** none.

#### 1E. Web client highs (M–L)

Status: landed on branch `worktree-agent-a7a5bfbb8064292e7` (not yet on
`main`): H4 1ff487c, H6 a5649d2, H3 4a3b724, H10 a6cd93f, H9/M6 05e9c09.
H10 also removed the gateway's unused `access_token` form on deliverable
downloads; the attachment ticket lives 15 min. H9 adds migration
`2026_10_agent_prefs_owner` (`user_agent_prefs.selected_owner`, no
backfill), so it is not migration-free as planned below.

| Item | Ref | Fix | Size |
|---|---|---|---|
| Session bearer in `<a href>` / `<img src>` | web H10 | Use a short-lived per-attachment ticket, reusing the site-ticket mechanism (`sharesApi.ts:31`). `access_token=` stays only in `sse.ts`. | M |
| The pop-out preview reads the wrong storage; a removed member keeps a preview | web H3 | Use `useAuth().subject`. Delete the stash/seed path (`desktop.ts:111-162`, `main.tsx:47`). `sse.ts` defaults `onAuthError` to `refreshSession`. | S |
| Restoring the saved agent borrows another member's agent | web H9, M6 | Extract `useAgentPrefs`. Prefs store `ownerId`, prefer self, and compare with `agentIdentity`. Server prefs are the only per-room store; `localStorage` is a read-only fallback. | M |
| "Load earlier" stops loading | web H6 | `FeedItem` carries an integer `seq` plus a `part`. Sort and dedupe on `(seq, part)`, and delete the roughly 10 floor copies. | M |
| The working indicator does not know which agent is working | web H4 | Add `Provider` / `Core` to `wire.ReportSessionStateInput`, key the indicator with `agentIdentity`, and add a golden fixture. | S |

- **Tests:**
  - web boundary rule 6 (`access_token=` only in `sse.ts`)
  - an `agent_session_state` fixture test
  - a `node --test` for `(seq, part)` ordering
- **Deploy:** web bundle, gateway (ticket), activity (wire struct).
- **Migration:** none.

#### 1F. Core install and desktop contract (M)

Status (Go rows): landed. `protocol.CompatibleRange` single owner and
`protocol.KnownCapability` (`protocol.Capabilities`): 4c31ee2.
`coreset.EntryFor(name, provider, declared)` builds every entry (env,
gateway activation, install probe); `mergeCoreEntries` and FromEnv's
`extraActivate` deleted: a007b26. Tests: `TestProtocolRangeMatchesRuntime`,
`TestCoreCapabilitiesAcceptEveryProtocolCapability`,
`TestEntryForAppliesEnvOverridesToEveryCore`,
`TestCoreEntriesBuiltOnlyByEntryFor`. Desktop env-key and catalog rows:
desktop agent.

| Item | Ref | Fix | Size |
|---|---|---|---|
| Two protocol-version rules | bridge 7 | Delete the `pkgmeta` copy and use `protocol.CompatibleRange`. | S |
| The install capability allowlist is stale | bridge 8 | Add `protocol.KnownCapability`, which covers `sessionFiles`, `sessionFileBytes` and `agentProfile`. | S |
| The gateway builds installed core entries a second way | bridge 9, 35; gw L12 | One `coreset.EntryFor(name, provider)`: env URL and token, `Explicit`, one discovery timeout. Delete `mergeCoreEntries`. | S |
| The desktop core env key differs from Go | desk H1 | `bridge.rs` uses `env_key`. Go exports `coreset.EnvKey`. Both are pinned by one fixture. | S |
| One unknown component kind breaks the whole desktop catalog | desk H5 | `#[serde(other)] Unknown`, with entries decoded one by one. | S |

- **Tests:**
  - `TestProtocolRangeMatchesRuntime`
  - `TestCoreCapabilitiesAcceptEveryProtocolCapability`
  - `core_env_key_matches_go` (`testdata/core_env_keys.json`)
  - `catalog_tolerates_unknown_kind`
- **Deploy:** gateway, CLI, desktop app.
- **Migration:** none.
- **Status (desktop rows, 2026-10-12): landed.**
  - desk H1 (61719ea): `coreset.EnvKey` exported; the desktop names core
    env vars only through `core_env_var`/`env_key` (b3f99f1);
    `internal/coreset/testdata/core_env_keys.json` pins both
    (`TestEnvKeyMatchesSharedFixture`, `core_env_key_matches_go`).
  - desk H5 (5e4bf22): `#[serde(other)] Unknown`, catalog entries decoded
    one by one; `catalog_tolerates_unknown_kind`.

### Batch 2: gateway and backend rule owners (L)

| # | Item | Refs | Size |
|---|---|---|---|
| 2.1 | **Gateway route table** `s.handle(pattern, perm, rate, h)` for every route, gateway-served and proxied. The 1B local-agent gate becomes a row. One super-admin check (`rbac.Can`) and one suspended-tenant gate. Delete `requireSuperAdminClaims`, `orgAdmin`, the store-nil wrappers and the unreachable 405 branches. One cached `roomMembership` {member, role, human}. One session-error mapping. | gw H2, M3–M6, M10, L3 | L |
| 2.2 | **`svcauth.NewRequest(ctx, method, url, body, subject, tenant)`** always sets the user, a defaulted tenant and the signature, in the right order. Add `svcauth.Subject(r)` and `svcauth.RequireService(key, allowed…)` per internal route; only dispatch and handoff accept a user identity. The gateway's `backend_client.go` (a client with a timeout) is built on it. `NewRoomTenantResolver` signs as the gateway service subject. The legacy-token tenant default appears once. | gw M1, L2; svc M7, M8, L10 | M |
| 2.3 | **Extend `internal/httpx`**: `WriteJSON`, `WriteError` (one `{"error"}` body for gateway and backends; realtime's `http.Error` calls move over), `DecodeJSON(w, r, limit, v)` (413 on oversize everywhere), `Unavailable` → 503. Add `obs.WriteStatus`, which logs any 5xx. Blob-error mapping happens once. The web `ApiError` reads the one body shape. | gw M10–M14, L5, L8; svc M12, L13 | M–L |
| 2.4 | **Membership plumbing**: `membership.FromEnv` (fatal in production without `BNBLOC_ROOM_BASE_URL`) returns one shared interface. Realtime gets one `s.room(w, r, need…)` preamble. One `join` path publishes `member_joined`. `ChannelAdmitsAgents` is checked at every site, including handoff import and activity before append. The gateway signs the effective org permission and backends use `rbac.Can`. `store.LoadLeasedJob` with one status mapper. | svc M4, M6, M9, M11, M17, M18 | M–L |
| 2.5 | **`wire` event-kind registry** with attributes. The hub retains only `replay` kinds, which fixes replayed toasts, badges and typing (web H5). Metrics labels and `PersistedFeedKind` derive from the registry. Add `wire.ChatMessagePayload` / `AgentMessagesPayload` + `DecodePayload[T]`. `wire.JobNotice` is used by bridge, activity and room tools, and readers skip notices. `contentshare.Announce` uses one dedup key. One `Service.publish`. `wire.FailureText`. A shared audit helper with outcome constants. | svc M13–M15, M19–M21; web H5; bridge 10 | M–L |
| 2.6 | **Grants and visibility**: `store.Grant.Live(at)` and one admission rule, `used + cost <= allowance` (a reservation costs 1 run). `RaiseRunQuota` checks expiry. `extpipe.MemberVisibility` is used everywhere, with a tenant-only helper. One name→component resolver and one tenant-list validation. Remove the dual adoption fields and the web's `?? x` reads. | store M11; svc M2, M3; gw M7, M15, M16 | M |
| 2.7 | **Gateway misc owners**: `s.publicOrigin(r)` honoring `TrustedProxies`. `pkgmeta` both builds and parses artifact keys. CORS `Access-Control-Expose-Headers`. Local-agent core selection goes through the rollout gate and `core.As`. `corePipeline` initializes with synchronization. Booleans and the super-admin list are parsed once, and env is read at startup. One audit-record builder that always sets the tenant. `memberContent` is tenant-scoped. One component-name check. Fix the room-id comment. | gw M8, M9, M17, M18, L6, L7, L9–L11, L13, L14; bridge 27 | M |

**Removals (D3):**
- The legacy borrow authority: shareagent start/resume/settings/turn/release/access, `handleAction`, `proxyTurn`, controller `Authorize`/`Release`, `staticPresence`/`unavailableOwners`, and store `AcquireLease`/`ConsumeRun` once unused (svc M1).
- Activity submit-provenance and sync-states endpoints, plus store `RecordSubmitProvenance`/`SubmitQueryable` (svc L1, store L17).
- Relay rendezvous and ice-config, plus the STUN/endpoint env (svc L1).
- Pre-channels compatibility: the empty-channel fallback, legacy mark-read and unread buckets, GET-creates-channel, and the empty typing body (svc L3).
- Unread `TenantID` body fields (svc L2).
- Built-in core leftovers (gw L4).

**Tests:**
- `TestEveryGatewayRouteGated`
- `TestSignedRequestsUseNewRequest` (merges gw 6 and svc 4)
- `TestSubjectReadOnlyViaSvcauth`
- `TestNoSilent5xx` extended (gw 7 and svc 8)
- `TestEventKindsRegistered`
- `TestNoticesCarryKind`
- `TestGrantLivenessOwned`
- `TestVisibilityOwned`
- `TestNoMembershipFailOpen`, moved to the `FromEnv` interface

**Deploy:**
- All service images.
- Bridge (`JobNotice`).
- Web bundle (error body, event kinds).
- Compose and Helm already set `BNBLOC_ROOM_BASE_URL` for every peer; verify before making it fatal.

**Migration:** none here. Table drops from the removals are collected in
4.5.

### Batch 3: bridge drive pipeline and core/package owners (L)

| # | Item | Refs | Size |
|---|---|---|---|
| 3.1 | **Drive pipeline of 8 named stages** (admit, resolve content, snapshot capabilities, plan execution, authorize, bind target, drive, settle). Each stage returns `(plan, *refusal)`. One `refusal{noticeKind, text, failureReason, diagnostics}` and one `fail(stage, err)` post the notice and the reason consistently, so every refusal gets a notice. Capabilities are snapshotted once per drive. A bad skill pack goes to `missing` instead of dropping all content. Add `b.detached(ctx)`, a `driveKey` type (no package-global `roomLocks`), `QualifyTargetID` / split for `TargetID`, `shareagent.PlatformTarget`, one `AgentProfile.BindingSum()`, and one usage projection. The sync conflict notice goes through the notice seam. One history decoder in `roomlog` (scope per §5, O1). This is a pipeline, not a strategy per drive kind: the kinds differ only in the inputs to stages 4 and 5. | bridge 1, 2 (structural), 11, 13–17, 39–43 | L |
| 3.2 | **Core client owners**: `core.REST.Request` / `JSON` shared by the protocol and sandbox clients, and the bridge goes through `doRaw`. The retry hint becomes `*bool`. manifestcore and the protocol client both return `capability_missing`, and both refuse disallowed workspace paths locally with `core.WorkspaceFileAllowed`. `protocol.HostConfineFromEnv` / `PassthroughFromEnv`. The sandbox mount is derived once. `core.As` everywhere, with no documented exception. `LogValue` on every secret-bearing type. | bridge 19–24, 36, 46, 47 | M |
| 3.3 | **Package and artifact owners**: `tarbundle.Cache.LoadPinned` refuses an empty or mismatching pin. One digest package. One MCP tool-name rule, with the reserved name refused at install. One "required env set" check. A field-to-kinds table plus `requireEndpointRuntime` / `requireSidecarRuntime`. `contentpkg` reads archives through `tarbundle`. One clean-relative-path check. One directory packer using the 1A exclusions, with `contentbuiltin` ignoring junk files. The CLI validates packages before upload. One semver and one env-name rule. One vmexec harness list, cwd check and shell. | bridge 25, 26, 28–34, 48, 50 | M–L |
| 3.4 | **CLI**: one JSON call helper that returns errors. `whoami` reports the session identity. `addGatewayFlags` / `addSigningFlags`. One 401 hint. `~` expansion. | bridge 37, 49 | S–M |

**Removals (D3):**
- `SessionIdle` / `LatestSession` from `core.Provider`, the manifestcore "older manifests" fallbacks, and the matching adapter routes and conformance cases (bridge 38).
- `serveRecipient`.
- PascalCase `bridgeJob.UnmarshalJSON`.
- `core.ShortID`.
- The nil-only error of `coreset.FromEnv`.
- `tarbundle.Cache.Put` / `Get`.
- Sandbox `Resize` / `ListPools`.
- The CLI legacy `"token"` key and `legacyArchKeyword` (bridge 44).
- The duplicate `roomctx` filters (bridge 45).

**Tests:**
- `TestDriveRefusalsAreFinal`, extended per stage
- `TestWorkspaceWriterEnforcesPolicy`
- `TestBundleLoadsArePinned`
- `TestOnlyTarbundleReadsArchives`
- `TestNoHandRolledAgentRefs`
- `TestVMExecEnvNamesOnlyInProtocol`
- `TestWireTypesEveryFieldTagged` and `TestWireMappersRoundTrip`
- `TestSecretsRedacted`
- `TestNoBareCapabilityAssertion`

**Deploy:**
- Bridge and vmexec (desktop release).
- Gateway and realtime (bundle cache, `contentbuiltin`).
- CLI.
- Installed Core Protocol adapters drop the removed endpoints, and the conformance suite changes with them.

**Migration:** none.

### Batch 4: store contract suite and parity (L)

| # | Item | Refs | Size |
|---|---|---|---|
| 4.1 | **One rig.** `forEachBackend(t, fn)` becomes the only rig. Delete the ~30 inline DSN checks and the 4 rig helpers. Add contract suites for every interface that lacks one: Activity, Attachment, Room core, Channel, Tenant, AgentPrefs (plus its `var _` assertion), Artifact core, BridgeJob lifecycle (merging the twin tests), Grant, RefreshToken, SSOHandoff, ComponentInstall, ExtensionAudit, PlatformSettings. | store H8, T1 | L |
| 4.2 | **Reads return errors** across TenantStore, RoomStore and GrantStore, finishing H1. The allowlist for those interfaces reaches zero. | store H1 | M |
| 4.3 | **Parity fixes**: `BridgeLive` plus one `liveBridgePred`, with `NowMs` from the caller. Bridge identity is global, and `BridgeOwner` is the one answer. Several targets per provider, and the claim uses `EXISTS`. Attachments are tenant-scoped. Exported sentinels plus one duplicate-key mapper. Re-put parity. `clampLimit(n, def, max)`. Platform-agents policy uses compare-and-set. Status `IN` lists are generated from the Go slices. A parity contract per list query. One blob-key list. LIKE escaping via `likePattern`. `queryAll[T]`. One `rowScanner`, generic null helpers and predicate constants. A `normalize()` per request type. `liveAt`. Deterministic ordering. Revoke at 0. Empty results serialize as `[]`. Full audit reads with a tie-break. The entitlement check runs in SQL. Budget equality in the column's precision. | store M1–M7, M9, M10, M12 (parity contract only), M13–M15, L1–L4, L7–L14 | L |
| 4.4 | **Indexes and bounded counts**: split `SweepAgentJobs` and add `(status, created_at_ms)` and `(status, lease_expires_at_ms)`. An outbox claim index with a full tie-break. `(scope, kind, updated_at_ms)`. A kind index for installs. Owner-leading indexes. A `claim_token` index. Remove the `OR` in `TenantTeamJobs`. A capped `CountContentUses`. A unique issuer (M8). Document the FULLTEXT size threshold (M22). | store M8, M16–M22 | M |
| 4.5 | **Removals (D3)**: never-written columns (`sync_states.pending_count`, `failed_count`, `last_error`; `agent_messages.channel_id`). Write-only `agent_sessions` (its columns, or the whole table via `retiredTables` if nothing reads it). `ExtAuditRecords`. The `ReindexSearchText` backfill and its route/CLI. `MaxRoomSeq`'s unused branches. The duplicate `idx_artifact_versions_recent`. Stale comments. Schema consistency: `room_id` `VARCHAR(64)` everywhere, JSON columns as JSON. Any tables left orphaned by the Batch 2 removals. | store L15–L21; svc L1 tables | M |

**Tests:**
- `TestEveryStoreInterfaceHasContract`
- `TestTenantTablesFilterTenant`, with a `// cross-tenant: <reason>` escape
- `TestBlobKeyColumnsReferenced`
- The single-source checks (store T5)
- The remaining contract cases (store T6)
- `TestNoWriteOnlyColumns`
- `TestStoreReadsReturnErrors`, with the allowlist only shrinking

**Migrations** (new ids):
- `2026_10_tenant_issuer_unique`: `''` becomes NULL in the same statement set, a prerequisite for the key, not a backfill.
- `2026_10_store_indexes` (M16–M20).
- `2026_10_drop_unused_columns` (L15, L16, L18).
- `2026_10_room_id_width_json_columns` (L21).

**Deploy:** all service images.

### Batch 5: Go→TS wire generation and web owners (L)

Status: partial, web-only items landed on branch
`worktree-agent-a7a5bfbb8064292e7` with 1E: formatters in `format.ts`
(M25, L3), `isSubmitEnter` (M18), `lib/clipboard.ts` + `ui/useCopy`
(M23), unparsable SSE data dropped (L12), `useDismissOnOutside` (L8), dead
exports (5.3, without knip). Boundary rules 6, 10 and L8 live in
`web/scripts/check-boundaries.mjs`. Everything else here is still open.

| # | Item | Refs | Size |
|---|---|---|---|
| 5.1 | **Projections in `internal/wire`**: room (list and detail), member, plaza, history, unread, targets, cores. Job and artifact projections leave out `tenantId`, `leaseBridgeId`, `diagnostics` and `blobKey` (§5, O4). Limits become wire constants, including the attachment and blob size limits. A generator (tygo or a small `go/ast` tool) writes `web/src/lib/wire.gen.ts`: `omitempty` → `?`, const groups → unions, `RoomEventKinds`, limits. `scripts/ci.sh go` runs it and then `git diff --exit-code`. Golden fixtures in `internal/wire/testdata/`, with a `node --test` over `normalizeRoomEvent`. Unparsable SSE data is dropped. | web M19, M26–M28, L10–L12; svc L8 | L |
| 5.2 | **Web owners**: `req` with `as:` and one `downloadCsv`. `ApiError` everywhere. `errorText` / `statusText`. A module store for `useLocalAgent`. `useMyAgents` and `useTenants`. `useVisiblePoll`. `queryKey(name, auth)` and `clearQueryCache()` at logout. `lib/roomRoles.ts` (`isOwner`, `canInvite`, `mayChat`, so visitors get no composer, per D1) with one role source. `lib/permissions.ts` generated from `rbac`. Generated event kinds plus a `member_joined` listener. One `agentHandle` / `parseAgentHandle` / `agentIdentity`. `<ActorLabel>` in notices. A deduped unread divider. `resolveRecipient()`. `isSubmitEnter` (IME). `ui/QueryState` with lists on `useQuery`. One mutation-feedback rule. `ConfirmDialog` for every destructive action. `lib/clipboard.ts`. `Record<Kind, …>` tables. Formatters in `format.ts`. Plaza paging. `useContentLibrary`. `lib/storageKeys.ts`. `roomPath()`. One agent label format. One room-gone path. `useDismissOnOutside`. Shared UI primitives. First-load errors are shown. Component splits only where these fixes extract code. | web H7, H8, M1–M5, M7–M18, M20–M25, M29, M30, L1–L9, L13 | L |
| 5.3 | **Removals (D3)**: `componentPackageModes.ts`, `expertHandle`, `Email`, `isMcpTool`, the `CorePackage*` re-exports, the phantom `AgentTarget` fields, and about 45 needless exports (enforced by knip). | web dead code, L11 | S |

**Tests:**
- `web/scripts/check-boundaries.mjs` rules 1–12, run from `scripts/ci.sh web`
- knip in CI
- the RBAC JSON parity test
- the event-kind parity test (via generation)
- the `wire.gen.ts` diff check

**Deploy:** web bundle (gateway and desktop), and the room, realtime,
activity and gateway images (projections).

**Migration:** none.

### Batch 6: desktop owners (L)

| # | Item | Refs | Size |
|---|---|---|---|
| 6.1 | **`trait KindRuntime`** (prepare, listener, `launch_env -> LaunchGuard`, on_spawned, on_exit, forget, `sweep(live)`) for core, plugin and sandbox. Previewer and MCP implement only sync and sweep. `LaunchGuard` revokes the host token on every exit path. Uninstall dispatches by kind, not by id prefix. | desk M9, M10 | L |
| 6.2 | **Process and settings owners**: `stopping` join handles, so a relaunch waits for the previous stop. One `RestartPolicy`. `AppState::update_settings` is the only writer, and `set_settings` leaves shell-owned fields alone. Corrupt files are moved aside and shown as an error state. A bounded log channel that emits only the new line. | desk M1–M3, M14, M15 | M |
| 6.3 | **Value owners**: `Sha256Pin`. `LoopbackHttp::parse`. `atomic_write` (unique temp name, fsync) plus single-flight for previewers. `ManagedSidecar::endpoint()`. `GatewayClient` (401 → `SessionExpired`; clear the adapter token at sign-out). An injective cache stem (`@`). Names validated at decode. A coalesced MCP pass with cached verification. `Secret::generate` / `ct_eq`. One `Dirs`. Non-redirecting loopback clients. Download size caps. One gateway-URL source. | desk M4–M8, M11–M13, L1, L2, L5–L9, L11, L12 | M–L |
| 6.4 | **Removals and fixes (D3)**: `bridge_restart` duplicates `bridge_start`. The previewer re-hash. `listen_addr` re-parsing. Stale and misleading error strings. The build script reads one arch table and uses `-mod=readonly`. | desk L3, L4, L10, L13 | S |

**Tests:**
- `secret_files_use_one_writer`
- `sha256_pins_normalized_for_every_kind`
- `cache_stem_is_injective`
- `one_loopback_rule`
- `set_settings_preserves_shell_owned_fields`
- `relaunch_waits_for_previous_stop`
- `host_grant_revoked_on_every_exit_path`

**Deploy:** the desktop release only. Old cache stems are swept on the first
reconcile, with no migration.

**Status (2026-10-12):** 6.4 partly landed (79ae59f): the stale
"at least one core" error (L3), file-naming errors in `write_owner_only`
(L4), and `GOFLAGS=-mod=readonly` for the sidecar build (L13, flag half).
Still open: `bridge_restart` (removing it needs the web's `restart()` to
call `bridge_start`), the previewer re-hash, `listen_addr` re-parsing, the
build script's arch table, and 6.1–6.3.

## 4. Enforcement tests (deduplicated)

| Test | Kind | From | Batch |
|---|---|---|---|
| `TestBnblocOwnedPathAgreesEverywhere` | behavioural table | bridge | 1A |
| `TestConfineDeniesBnblocCredentialPaths` | behavioural | bridge | 1A |
| `TestHookRewritesCommandsMerelyPrefixedByVMExec` (+ exit 2) | behavioural | bridge | 1A |
| `TestDevDiscoveryRefusedInProduction` | behavioural | gw | 1A |
| `uninstalled_while_closed_is_forgotten`, `orphan_requires_exe_evidence`, `sign_out_and_refresh_do_not_race` | Rust | desk | 1A |
| `TestRevocationClosesAccess` | behavioural | svc | 1B |
| `TestHumanOnlyRoutes` (visitor and agent-host rows, per service) | behavioural table | svc | 1B |
| `TestNoMembershipFailOpen` | static | svc | 1B |
| `TestEffectiveTenantSingleOwner` (no acting header, `tenantId`, `RoomTenant` or `roomTenantOr` outside the owner) | static | gw | 1B |
| `TestProxyRoomTenantEveryRoomPath` | behavioural | gw | 1B |
| `TestJobGrantHonoredOrRefused` | behavioural | gw | 1B |
| `TestRoomGrantDetailBundleDownloadable` | behavioural | gw | 1B |
| `TestRoomOwnedTablesDeletedWithRoom` | static (schema) | store | 1C |
| `TestNoIdentityFold` (new) | static | store, svc, bridge | 1C |
| `TestStoreErrorsNotConflict` | behavioural | svc | 1C |
| Contract cases: identical update, cross-tenant/room re-put, case-variant ids distinct, concurrent append | contract | store | 1C |
| `TestDriveRefusalsAreFinal` | table | bridge | 1D (extended in 3.1) |
| Web boundary rules 6 (`access_token=`) and 7 (acting tenant) | node gate | web | 1B/1E |
| `TestProtocolRangeMatchesRuntime`, `TestCoreCapabilitiesAcceptEveryProtocolCapability` | parity | bridge | 1F |
| `core_env_key_matches_go`, `catalog_tolerates_unknown_kind` | shared fixture | desk | 1F |
| `TestEveryGatewayRouteGated` | behavioural walk | gw | 2.1 |
| `TestSignedRequestsUseNewRequest` (merges gw 6 and svc 4) | static | gw, svc | 2.2 |
| `TestSubjectReadOnlyViaSvcauth` | static | svc | 2.2 |
| `TestNoSilent5xx` extended: `obs.ServerError` not `slog.Error`; non-constant status only via `obs.WriteStatus`; no `WriteHeader(resp.StatusCode)` | static | gw, svc | 2.3 |
| `TestEventKindsRegistered`, `TestNoticesCarryKind` | static | svc, bridge | 2.5 |
| `TestGrantLivenessOwned`, `TestVisibilityOwned` | static | svc | 2.6 |
| `TestWorkspaceWriterEnforcesPolicy` | behavioural | bridge | 3.2 |
| `TestNoBareCapabilityAssertion` | static | gw, bridge | 3.2 |
| `TestSecretsRedacted` | behavioural | bridge | 3.2 |
| `TestVMExecEnvNamesOnlyInProtocol` | static | bridge | 3.2 |
| `TestWireTypesEveryFieldTagged`, `TestWireMappersRoundTrip` | static, round-trip | bridge | 3.2 |
| `TestBundleLoadsArePinned`, `TestOnlyTarbundleReadsArchives` | behavioural, static | bridge | 3.3 |
| `TestNoHandRolledAgentRefs` (`"shared-agent:"`, core/provider joins, `"\x00"` keys) | static | bridge | 3.1/3.3 |
| `TestEveryStoreInterfaceHasContract` | static (`go/ast`) | store | 4.1 |
| `TestStoreReadsReturnErrors` (shrinking allowlist) | static | store | 1C → 4.2 |
| `TestTenantTablesFilterTenant` | static (SQL) | store | 4.3 |
| `TestBlobKeyColumnsReferenced` | static (schema) | store | 4.3 |
| Single-source checks: status literals, `BridgeOfflineAfterMs`, inline `limit <= 0`, `MySQLError` | static | store | 4.3 |
| `TestNoWriteOnlyColumns` | static (schema) | store | 4.5 |
| `wire.gen.ts` diff check, golden fixtures, RBAC JSON parity, event-kind parity | codegen, parity | web | 5.1/5.2 |
| `check-boundaries.mjs` rules 1–5, 8–12, and knip | node gate | web | 5.2/5.3 |
| `secret_files_use_one_writer`, `sha256_pins_normalized_for_every_kind`, `cache_stem_is_injective`, `one_loopback_rule`, `set_settings_preserves_shell_owned_fields`, `relaunch_waits_for_previous_stop`, `host_grant_revoked_on_every_exit_path` | Rust | desk | 6 |

Each owner named above is added to AGENTS.md in the batch that creates it,
with its test, following the effective-tenant entry.

## 5. Decisions

### Decided (product owner, 2026-10-12)

- **D1. Visitors are read-only.** They may not chat, type, drive or borrow
  agents, or create or run schedules. The rule is `membership.Member.MayChat()`
  (human and not a visitor), used in every service, mirrored in
  `lib/roomRoles.ts` and parity-tested. Lands in 1B, with the web side in
  5.2.
- **D2. Subjects are case-sensitive.**
  - Use exact `==` everywhere and never fold identities (no `EqualFold` or
    `ToLower`).
  - Identity columns move to `utf8mb4_bin` through a **new** migration id,
    with no backfill.
  - Memory and MySQL must agree, which a contract case enforces: case
    variants are distinct ids in both stores.
  - Lands in 1C.
- **D3. Pre-launch: no compatibility code.** All legacy and compatibility
  code is removed inside the batch that touches it:
  - dead endpoints;
  - legacy fallbacks;
  - pre-channels compatibility;
  - the legacy borrow authority;
  - write-only columns;
  - backfills such as `ReindexSearchText`;
  - dual reads.

  No data migrations or backfills. Schema changes still use new migration
  ids, and dropped tables go to `retiredTables`.
### Decided on recommendation (product owner, 2026-10-12: "按你建议实现")

- **O1. Room-history scope for agents** (bridge 16). `.room/` reads every
  channel, while `read_room` reads only the job's channel. *Decided:* both
  read exactly the channels `agentMayReadChannel` admits for the job, through
  one decoder.
- **O2. Unreadable sandbox policy** (bridge 18). AGENTS.md says to refuse;
  the code and the 2026-10-09 PRD fail open. *Decided:* fail closed for
  borrowed and platform drives, with a room notice, and update the PRD.
- **O3. Desktop secrets on member switch** (desk H3). The choice is
  per-member directories or wiping on owner change. *Decided:* per-member
  directories (planned in 1A). Member B never sees A's credentials, and A
  keeps their sign-ins.
- **O4. Job fields visible to room members** (web M27). *Decided:* the job
  projection carries status, agent, timing, failure reason and
  `messageText` (already in chat). It leaves out `tenantId`,
  `leaseBridgeId` and `diagnostics`; diagnostics stay owner- and admin-only.
- **O5. Tenant switch persistence** (gw H5). *Decided:* the switched
  tenant persists across refresh and applies to bridge and CLI sessions
  forked from it (planned in 1B).

- **O6. Which content handlers honor a job grant** (gw H4). *Decided:*
  honor it for the actions a room tool performs (save, import, prompt
  save/use, content-use list/put/delete) and refuse `?job=` with 400 for
  org-visible or destructive actions (delete content, ack, share, revoke,
  update copy). 1B builds the mechanism; this list sets each handler's mode.

## 6. Out of scope / deferred (value vs effort)

- **Declarative filter builder that generates both SQL and the Go predicate**
  (store M12): high effort. A parity contract per query (4.3) catches the
  same drift for much less.
- **Generic `withTx` helper** (store L5): saves a `defer Rollback` per site,
  with no drift risk. Not worth a sweep on its own.
- **Unified keyset-cursor helper** (store L6): four encodings, each with
  tests. The real defect, paging on a non-unique key, is fixed per site in
  4.3 and 5.1.
- **Cross-replica notification collapse** (svc L14): it needs shared state,
  and the duplicate is cosmetic.
- **Generic grace set for presence and roster** (svc L4): two small timers.
  The abstraction costs more than it saves.
- **CLI subcommand table** (bridge 49): the flag helpers in 3.4 remove the
  duplication.
- **Adapter-repo internals** (bridge 51): these belong to that repo's
  backlog. Only the `core.Provider` method removal (3) is coordinated here.
- **Standalone component splits** (web "proposed splits"): extract only when
  a fix touches the code.
- **Per-window Tauri command allowlist** (desk L14): every window loads the
  same trusted bundle, so the capability-file churn buys little.

## 7. Spot-check record

Every high was re-checked against `main` at 17dabf3 on 2026-10-12. None were
wrong. Some notes for whoever implements them:

- **store H5.** `JSON_UNQUOTE` yields a binary-collated string, so the MySQL
  owner comparison is already exact, which is consistent with D2. The real
  defect is summing raw, unclamped values.
- **bridge 6.** The fix depends on the harness's hook contract (exit 2
  blocks; other non-zero codes do not). Verify that against the harness
  docs when fixing.
- **web H6.** This needs a batch whose index-0 entry is skipped (empty
  text). The trigger is narrow, but the failure is real: `parseInt` returns
  0 for `10.001`.
- **desk H2.** Confirmed: `uninstalled` is computed from the in-memory
  sidecar map, which is empty after a restart.
