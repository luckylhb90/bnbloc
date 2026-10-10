# Zero-install onboarding: unsigned-DMG install guide + platform agents (2026-10-10)

Status: implemented (2026-10-10); awaiting deploy + live verification

## Why

Adoption check (2026-10-10): every human message in the most active room
came from one person. Trying bnbloc today needs the desktop app (unsigned:
macOS Gatekeeper blocks it, and the way through is not obvious) plus a local
agent core. A browser-only member can join rooms but has no agent
to talk to. Two changes remove that wall:

1. **Install guide for the unsigned desktop app.** The DMG window and the web
   download flow show how to get past Gatekeeper.
2. **Platform agents.** A super admin publishes some of their own agents
   platform-wide. Any member, browser-only included, can add one to a room
   and use it, with no install and no per-room approval.

Product decisions (2026-10-10): no model or run limits on platform agents;
platform-agent drives are always sandboxed (refused when the host's
sandbox is not ready); available in every organization. Revised the same
day: the intended scope was "every organization the hosting super admin
belongs to", but a super admin acts platform-wide and is typically a member
of none, which made the agents available nowhere.

## 1. Unsigned-DMG install guide

- **DMG window.** A background image laid out around the app icon and the
  Applications link: "1 Drag bnbloc to Applications · 2 Open it once:
  macOS blocks it (the app is not yet notarized) · 3 System Settings →
  Privacy & Security → Open Anyway". Tauri `bundle.macOS.dmg`
  (`background`, `windowSize`, `appPosition`, `applicationFolderPosition`).
  The image is generated from source in the repo (reproducible), 1x and 2x.
- **Web download.** After a download starts (`ReleaseDownloadButton`,
  Release hub), an "Install on macOS" guide shows the same three steps,
  with one illustration of the Privacy & Security "Open Anyway" row. It
  also offers the alternative for people comfortable with Terminal:
  `xattr -dr com.apple.quarantine /Applications/bnbloc.app`, with a copy
  button and a one-line explanation of what it does. The guide can be
  reopened from the hub ("Trouble opening the app?").
- In-app updates are unaffected (the updater's download carries no
  quarantine attribute).
- When the app is signed and notarized later, the guide is removed (a
  build/config switch is not added now: there is one release channel).

## 2. Platform agents

### Model

- A **platform agent** is a super admin's own agent (core + provider)
  published platform-wide. The super admin is its **host**; their bridge
  drives it, always in the host's sandbox VM.
- **Adding to a room** (any room member who may chat; not a visitor) gives
  the room a standing room authorization for that agent. This reuses the
  existing per-room borrow authorization (`roomauth_*` grant), with
  `source = platform`: no run quota, no cost allowance, no model
  restriction, no expiry while the agent stays published. It also gives
  the host a hidden **agent-host membership** in the room, so the host's
  bridge subscribes to the room and materializes its context exactly as for
  any room it is in.
- An agent-host membership is not a human membership:
  - it is listed only with an explicit `agentHost` flag; the web hides it
    from people lists and names it under "Platform agents";
  - it is excluded from the host's own room list (`GET
    /v1/users/{me}/rooms`) unless `includeAgentHost=1` is passed (the
    bridge passes it);
  - it never receives notifications, unread counts or presence;
  - it cannot post chat as a human (the host's agent replies go through
    activity as usual).
- **Removing** a platform agent (any member who may chat, the room owner,
  or the host) revokes that grant. The agent-host membership goes when the
  room has no platform agent of that host left.
- **Unpublishing** (the super admin removes an agent or switches platform
  agents off) takes effect at read time. Reserve refuses a platform grant
  whose agent is no longer published (`denyReason: platform_agent_unavailable`),
  and the picker stops offering it. Stale memberships are removed when the
  room's platform agents are next listed or changed.
- **Scope:** every organization. The host is the super admin who
  published the agents (the policy route is super-admin-only), and acts
  platform-wide rather than as an organization member, so the host's
  organization memberships play no part. Removing someone from
  `BNBLOC_SUPER_ADMINS` does not unpublish their agents: another super admin
  switches platform agents off (or republishes as the new host).
- **Sandbox:** a reserve for a platform grant answers `sandboxRequired:
  true`. The bridge then refuses the drive, before anything runs, when its
  sandbox is not ready. This is the same refusal path, notice and job
  failure (`sandbox_required`) as the tenant's `requireSandboxForBorrow`.
- **Availability:** the agent works while the host's desktop (bridge + core +
  sandbox) is online. The picker shows online/offline. Recommended operation:
  an always-on Mac signed in as the hosting super admin.

### Data (MySQL; both schema files + `migrations.go` + `migrations.lock`)

- Migration `2026_10_platform_agents`:
  - `room_members.agent_host TINYINT(1) NOT NULL DEFAULT 0`;
  - `grants.source VARCHAR(16) NOT NULL DEFAULT ''` (`''` = owner-issued,
    `platform`).
- Policy: `platform_settings` key `platform_agents` → `{enabled, hostUserId,
  agents: [{core, provider}]}` (one host: the super admin who last saved it).
  Superseded by the per-host policy, per-host list entries and the offline
  guard of `2026-10-10-platform-agents-v2-onboarding.md` (Backend notes).

### Routes

- Super admin (shareagent, platform role check like the other
  `/v1/platform/*`):
  - `GET /v1/platform/platform-agents` → `{enabled, hostUserId, agents:
    [{core, provider}]}`.
  - `PUT /v1/platform/platform-agents` `{enabled, agents: [{core,
    provider}]}`. The caller becomes the host. Each agent must be a
    non-empty core/provider pair.
- Members:
  - `GET /v1/platform-agents` → `{agents: [{hostUserId, core, provider,
    targetId, online}]}` for the caller's session tenant. The list is empty when platform agents are off.
    `targetId` is core-qualified as in `GET /v1/my/agents`; `online` comes
    from the host's live bridge presence.
  - `GET /v1/rooms/{roomId}/platform-agents` → `{agents: [{hostUserId, core,
    provider, targetId, online, addedBy, addedAtMs}]}` (room members).
  - `POST /v1/rooms/{roomId}/platform-agents` `{core, provider}` → 201
    `{agent}`. It is idempotent: it adds the agent-host membership through
    the room service's internal route and puts the platform grant.
    - 403 when the caller is a visitor, an agent host or not a member.
    - 404 when the agent is not published.
  - `DELETE /v1/rooms/{roomId}/platform-agents/{provider}?core=` → 204.
  - `GET /v1/rooms/{roomId}/agent-catalogs` (activity, room members) →
    `{agents: [{ownerUserId, core, provider, online, modelOptions}]}`: the
    owner-keyed model catalogs of the agents addressable in the room. An
    agent-host member contributes only its platform agents added to this
    room and still published (`internal/shareagent.RoomPlatformGrants`,
    the rule the room listing and reserve use), never its other agents.
    See `docs/bugs/2026-10-10-composer-model-picker-own-catalog-only.md`.
- Room service internal (svcauth, shareagent only):
  `PUT|DELETE /internal/v1/rooms/{roomId}/agent-hosts/{userId}`.
- Reserve (`…/authorization/reserve`) response gains `sandboxRequired`
  (true for platform grants). Its 403 `denyReason` may be
  `platform_agent_unavailable`.

### Web

- **Platform console:** a "Platform agents" card with an on/off switch and
  the super admin's own agents (from `GET /v1/my/agents`) as checkboxes.
  Help text: "Members of your organizations can add these agents to their
  rooms and use them without installing anything. Runs use your agent core
  and always run in your sandbox."
- **Composer agent picker:** a "Platform agents" group (online dot). If the
  chosen agent is not in the room yet, a confirm dialog appears first: "Add
  <provider> (hosted by <host>) to this room? It will read this room's
  conversation to answer, and runs in the host's sandbox." Once added, it
  is driven like a borrowed agent (`recipient {ownerId: host, provider,
  core}`, borrow flag). Its model picker shows the HOST's catalog (from
  `GET /v1/rooms/{roomId}/agent-catalogs`, keyed by owner), unrestricted.
- **Members dialog:** a "Platform agents" section listing the added agents,
  each with a Remove button. Agent hosts never appear among people.
- **Zero-core members:** the "no local core" guidance points to platform
  agents when any are available ("Try a platform agent — no install
  needed").

## Implementation notes

### Install guide (desktop + web)

- **DMG window.** Source: `apps/desktop/src-tauri/dmg/background.html`.
  `swift apps/desktop/scripts/render-dmg-background.swift` (macOS only:
  AppKit + WebKit + `tiffutil`, no package dependency) reads
  `bundle.macOS.dmg` from `tauri.conf.json`, injects the window size and the
  two icon positions into the page as CSS variables (one source of truth for
  the layout), snapshots it at 1x and 2x, and writes
  `dmg/background.png` (660x450), `dmg/background@2x.png` (1320x900) and
  `dmg/background.tiff` (both, via `tiffutil -cathidpicheck`). The config
  points at the TIFF so Finder picks the sharp image on Retina (the bundler's
  create-dmg copies the file as is). Re-run the script after editing the HTML
  or the dmg layout and commit the three outputs.
- Layout: `windowSize` 660x450, `appPosition` (165, 160),
  `applicationFolderPosition` (495, 160). The window size includes Finder's
  28 pt title bar and, when the user shows it, the 28 pt path bar, so all
  content stays above y = 394. Finder draws the icon labels itself (black in
  Light Mode, white in Dark Mode); each label sits on a mid-tone pad
  (luminance ~0.18, about 4.6:1 for both).
- Verified by running the bundler's own `bundle_dmg` script (tauri-bundler
  2.9.4) with the exact arguments Tauri passes against the built universal
  `bnbloc.app`, mounting the DMG and capturing the Finder window: icons sit in
  their slots, labels on their pads, nothing clipped. `cargo check` parses the
  new `tauri.conf.json` (`DmgConfig` denies unknown fields). A full
  `cargo tauri build --bundles dmg` was not run.
- **Web.** `web/src/lib/macInstall.ts` decides what a macOS installer is
  (`isMacInstaller`: a macOS/darwin platform or target, or a `.dmg`; never an
  updater archive) and holds the one guide's open state.
  `afterReleaseDownload(file)` opens it once a macOS download has started,
  from both download paths: `ReleaseDownloadButton` (Release Hub, Agents,
  Run agent, core empty state) and the desktop-presence download (room hint,
  sidebar "Desktop app not connected"). `MacInstallGuideHost` (mounted once
  in `App`) lazy-loads `views/MacInstallGuide.tsx`: the three steps, a drawn
  React mock of System Settings → Privacy & Security with the Open Anyway
  row (sidebar folds away on a phone), and the Terminal alternative
  (`xattr -dr com.apple.quarantine /Applications/bnbloc.app`, Copy button,
  one-line explanation). The Release Hub shows "Trouble opening the app?"
  when any release has a macOS installer. Also fixed on the way: the room's
  "Download … app" button labelled a `macOS` hub file "Windows" (it compared
  the platform to `macos` case-sensitively); it now uses `isMacInstaller`.

### Platform agents — backend

- **Data.** Migration `2026_10_platform_agents`: `room_members.agent_host`,
  and on `shared_agent_grants` (the grants table) `source` plus
  `created_by` (who issued the grant: the owner, or the member who added the
  platform agent; it backs `addedBy`). Policy: `store.LoadPlatformAgents` /
  `SavePlatformAgents` over platform setting `platform_agents`
  (`wire.PlatformAgentsPolicy`; DTOs in `internal/wire/platform_agents.go`).
- **Agent-host membership** (`store.Membership.AgentHost`, role `visitor`):
  `AddAgentHost` never downgrades a human member, `RemoveAgentHost` never
  removes one, and any human add (join, invite, add member) converts the row.
  Excluded from `RoomsForUser*`, `RoomsForUserDetailed`, member counts (room
  detail, plazas), handoff exports and template members; flagged
  `agentHost` in `GET /v1/rooms/{id}/members[/{user}]` and the membership
  probe (`{member, role, agentHost}`). `GET /v1/users/{me}/rooms?allTenants=1
  &includeAgentHost=1` appends them to `rooms` and lists them in
  `agentHostRooms` (only with `allTenants=1`, the bridge's shape). Room
  internal route `PUT|DELETE /internal/v1/rooms/{roomId}/agent-hosts/{userId}`
  accepts only `svcauth.ServiceSubject("shareagent")`
  (`membership.RoomClient.PutAgentHost/DeleteAgentHost`).
- **Human-only surfaces** use `IsHumanMember` (membership client,
  realtime/notify ports): chat, typing, schedules (create and fire), presence,
  roster advertisement (`?agents=` is ignored), the people chat (stream,
  history, and the room service's people-chat/channel-create routes), plugin
  events, @-mentions, and borrow-request notifications to the agent's owner.
  Everything the host's bridge needs stays on `IsMember`: the room stream,
  channels, history, attachments, artifacts, agent-activity replies.
- **shareagent** (`services/shareagent/platform_agents.go`): the policy and
  member routes as specified; `online`/`targetId` come from the host's live
  bridge advertisements (`ListBridgeAgentsForOwner`, the `GET /v1/my/agents`
  source; `targetId` is empty while offline). Add puts the membership, then
  the grant (`roomauth` id with discriminator `<core>:<provider>`, the
  bridge's borrow target; no quota, allowance, models or expiry); an
  existing live platform grant answers 200 with the original adder, a new
  one 201. Remove revokes the room's platform grant for that agent and drops
  the membership when the host has none left. The room listing reconciles
  each host's membership both ways (drops it when no listed agent remains,
  restores it for an available one). `liveAuthorization` — the borrower's
  check-on-send, quota requests — treats an unpublished platform grant as
  absent. Platform grants are left
  out of `GET .../shared-agents` and of the tenant borrow ceilings
  (`TenantUsage`). Policy saves and room adds/removes are audited (kind
  `platform-agent`).
- **Reserve.** Request body gains `sandboxed` (the bridge's plan). For a
  platform grant it denies `platform_agent_unavailable` (read-time policy and
  tenant check) or `sandbox_required` (unsandboxed drive) before reserving,
  skips the org ceilings, and answers `sandboxRequired: true` in both the 200
  and the 403; runs are reserved and settled as any borrow (audit, usage).
- **Bridge.** Discovery passes `includeAgentHost=1` and never live-syncs an
  `agentHostRooms` room. `decideSandbox` now also reports why a borrowed drive
  is not sandboxed; a `sandbox_required` answer (or a reservation with
  `sandboxRequired` for an unsandboxed drive, settled at once) goes through
  `refuseUnsandboxed`, the same notice and `sandbox_required` job failure as
  the tenant policy. Serve-all (testing) drives use the read-only check and
  are not sandbox-gated.
- **Gateway.** `/v1/platform-agents` and room sub-resource `platform-agents`
  route to shareagent (`/v1/platform/*` keeps manage_platform; member writes
  need drive_agent); `routeLabel` knows `platform-agents` and `agent-hosts`.

### Platform agents — web

- Wire shapes and pure logic: `web/src/lib/platformAgents.ts` (unit-tested in
  `platformAgents.test.ts`); endpoints: `web/src/lib/platformAgentsApi.ts`;
  the organization's offer: `usePlatformAgents()` (one cached request per
  session tenant, revalidated after 30 s, read by the picker and the
  zero-core hints).
- **Agent hosts are never people.** `api.members()` splits `agentHost`
  members off (`splitAgentHosts`) and returns `{members, agentHosts}`, so
  every people list (Members panel, mentions, role lookup, people chat)
  shows humans only. `composeRoomAgents` drops roster agents and borrow
  grants of agent-host-only members (their bridge may advertise more than
  the published agents, and the platform grant must not reappear as an
  ordinary borrow row) and adds the room's platform agents
  (`GET /v1/rooms/{id}/platform-agents`) as recipients
  `{ownerId: host, provider, core, self: false, sandboxed, platform}`.
- **Driving** reuses the borrow path unchanged: the recipient is another
  member's agent, so `borrow` is set, the send checks
  `GET …/shared-agents/shared-agent:<core>:<provider>/authorization?owner=<host>`
  first, then posts the chat with `recipient {ownerId: host, provider,
  core}`. No owner-approval step: the add created the grant.
- **Model picker:** `resolveModelCatalog` (`web/src/lib/agentCatalogs.ts`)
  resolves the picked recipient's catalog by (owner, core, provider) from
  the room's catalogs (`useRoomData` re-reads them when the room's agent set
  changes). My own agents use the local desktop target first; anyone else's
  agent uses only the owner's room catalog — never the caller's own catalog
  for a matching (core, provider). A borrowed agent's catalog is narrowed to
  the owner-issued grant's `allowedModels` when the owner restricted them;
  a platform agent's is not (platform grants carry no model range).
- **Composer picker** (`AgentPicker` + `agentRows`): a last "Platform agents"
  group (every agent offered to the organization plus any already added,
  online dot, "hosted by <host>", "Add" when not in the room yet; the host
  is not offered their own agents, and a platform identity never repeats in
  another group). Picking one not in the room opens the confirm ("Add
  <provider> (hosted by <host>) to this room? It will read this room's
  conversation to answer, and runs in the host's sandbox."), portalled to
  `body` because the chat column is a CSS container. On confirm,
  `useRoomPlatformAgents.add` POSTs, re-reads the room's list and selects the
  agent. Failures toast: 403 "Only room members who can chat may add
  agents to this room"; 404 "<provider> is no longer offered as a platform
  agent" (and the offer is re-read).
- **Members panel:** a "Platform agents" section (`PlatformAgentsSection`):
  provider, host, who added it, online dot, and Remove for a member who may
  chat (not a visitor) or the host; the "Agents" section no longer lists
  them.
- **Platform console:** `PlatformAgentsCard` on the super admin's Overview:
  on/off switch, the admin's own agents from `GET /v1/my/agents` as
  checkboxes (a published agent the bridge does not advertise right now
  stays listed so it can be unpublished; when another super admin hosts, a
  note says saving makes the caller the host), the PRD help text, Save →
  `PUT /v1/platform/platform-agents`.
- **Zero-core guidance:** `PlatformAgentsHint` ("Try a platform agent — no
  install needed") in the Agents page (no-core banner, else the browser
  bridge banner), the Run agent dialog's "No agent core connected" block,
  the room's "Run your own agent on desktop" card, and the in-room desktop
  banner, shown only when the organization is offered at least one.
- Verified against a mock gateway (Chromium, desktop and 390 px): picker
  group, confirm, add → selected recipient, send (authorization check then
  chat body with the host recipient), 403/404 toasts, Members panel without
  the host, console save body `{enabled, agents:[{core, provider}]}`.
