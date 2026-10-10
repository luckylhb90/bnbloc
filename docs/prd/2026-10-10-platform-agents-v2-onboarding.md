# First-run onboarding + platform agents v2 (2026-10-10)

Status: implemented (2026-10-10); awaiting deploy + live verification

Follow-up of `2026-10-10-zero-install-onboarding.md`. Platform agents work
end to end (verified on production 2026-10-10). This round makes a first
visit succeed, and makes the platform agents affordable, observable and
available. Product decisions (2026-10-10):
- do 1–5 below;
- each platform agent gets a default model chosen by the super admin (pick
  a free/trial model; members may still override, no model restriction);
- desktop signing/notarization is deferred (cost).

## 1. First-run onboarding (members)

Problem: a new member signing in from a browser lands on an empty room list
with nothing to do, and churns.

- **Welcome state:** the home (rooms) view of a member with no rooms, or one
  who has not dismissed it, shows a welcome card:
  - title "Try bnbloc with an AI agent";
  - one primary action "Start a room with <agent>". It creates a private room
    ("My first room", de-duplicated), adds the default platform agent (the
    first online published agent), opens the room and selects that agent in
    the composer;
  - a secondary "Install the desktop app to use your own agents" link (opens
    the release hub with the macOS install guide).
  - If platform agents are off or none is online, the card offers only the
    desktop path and explains that an admin can enable platform agents.
- **Starter prompts:** a room whose feed is empty and which has an agent
  available shows 3–4 clickable example prompts above the composer. They
  fill the composer, they do not send. They are chosen to show the product:
  - "Build a one-page landing page for a coffee shop" (web-app preview +
    share);
  - "Write a project plan for … as a Markdown doc" (deliverables);
  - "Explain this repository / this idea in 5 bullet points";
  - "Draw a simple diagram of …".
  They disappear after the first message.
- **Dismissal:** the welcome card can be dismissed (stored per user in
  localStorage). Starter prompts need no storage: they show only on an empty
  feed.
- **Done when:** a brand-new browser-only member goes from sign-in to an
  agent reply in at most 2 clicks plus typing (or 1 click plus a starter
  prompt). Measured once in a headless run against production.

## 2. Default model + usage dashboard (super admin)

- **Default model:** the policy gains a per-agent default model:
  `agents: [{core, provider, defaultModel?}]`. The platform card offers the
  host's own catalog for that agent (`GET /v1/my/agents` modelOptions).
  Authority is server-side:
  - the reserve answer for a platform grant includes `defaultModel`;
  - the bridge drives a platform turn with that model when the chat carried
    no explicit model (`agentOptions.model`), the same as if the member had
    picked it;
  - the composer shows it as the platform agent's default in the model
    picker.
- **Usage dashboard** (`GET /v1/platform/platform-agents/usage`), from the
  existing borrow accountability rows of platform grants:
  - `today` and `month` totals: `{runs, members, rooms, tokens, cost}`;
  - top members and top rooms (top 5, month);
  - the last 20 runs `{atMs, memberId, roomId, roomName, agent, model,
    cost}`.
  The platform card shows the totals and expandable tables. Cost is what
  the bridge reports (may be 0 for unpriced/trial models).

## 3. Offline and busy states

- `GET /v1/platform-agents` and the room listing report, per agent and per
  host, `online` and `queued` (the host's claimable jobs not yet started:
  pending jobs addressed to that host).
- **Composer:** a platform agent with no online host is shown as offline in
  the picker, and sending to it is blocked with "This platform agent is
  offline — its host's computer is not connected. Try again later." No job
  is created. When the chosen host is busy (`queued > 0`), the job row reads
  "Queued — N requests ahead".
- **Server guard:** a chat addressed to a platform agent whose host is
  offline is refused before a job exists (409 `platform_agent_offline`), so
  other clients cannot leave dead jobs behind either.

## 4. Availability: multiple hosts

- Every super admin may publish agents from their own machine. The policy
  becomes per host: `hosts: [{hostUserId, enabled, agents: [{core, provider,
  defaultModel?}]}]`.
  - `PUT /v1/platform/platform-agents` writes the caller's entry only.
  - `GET` returns all hosts; each super admin edits their own.
  - This is a clean cutover of the setting shape; dev/prod stores are reset
    by re-saving.
- A **platform agent** is a `(core, provider)` published by one or more
  hosts. Members see one entry per agent with the online/queued state of
  each host.
- **Adding to a room** creates a platform grant and an agent-host membership
  for every host publishing that agent, so any of them can serve the room.
  Publishing later extends existing rooms when their platform agents are next
  listed (the same reconcile as unpublish).
- **Routing (client):** a send picks the host:
  1. the host this room used last for this agent, if online and not busier
     than others by 3+ jobs;
  2. otherwise the online host with the fewest queued jobs.

  The recipient stays `{ownerId: host, core, provider}`. The claim path
  (bridge claims by recipient) is unchanged. Switching host starts a new
  agent session on that machine; the room context (.room/) carries the
  conversation.
- Operational note: an always-on Mac signed in as a super admin is the
  recommended host. Two hosts give failover.

## 5. Transparency in the room

- A room with platform agents shows a small persistent indicator in its
  header: "Platform agent · can read this room". Its tooltip/popover names
  the agents and hosts and says runs happen in the host's sandbox. Every
  member sees it, including members who joined after the agent was added.

## Implementation notes

### Backend

Implemented 2026-10-10 (sections 2–4). Shapes live in
`internal/wire/platform_agents.go`; the one usability rule is
`internal/shareagent/platform.go`.

- **Policy (clean cutover).** Platform setting `platform_agents` is
  `{hosts: [{hostUserId, enabled, agents: [{core, provider,
  defaultModel?}]}]}` (`wire.PlatformAgentsPolicy`). The old single-host
  value reads as an empty policy: each super admin re-saves. `GET
  /v1/platform/platform-agents` returns all entries. `PUT` `{enabled,
  agents}` replaces the caller's own entry only (`WithHost`) and answers the
  whole policy. A blank core/provider or a `defaultModel` over 128
  characters is a 400; duplicates collapse (the first wins). A host
  publishes an agent while its entry is enabled and lists it
  (`Publishes`/`Agent`). Saves are a read-modify-write of one setting, so two
  super admins saving in the same instant can lose one entry (re-save).
- **Members' list** `GET /v1/platform-agents` → `{agents: [{core, provider,
  online, hosts: [{hostUserId, online, queued, targetId, defaultModel?}]}]}`.
  There is one entry per published (core, provider), in policy order (first
  appearance across hosts). The agent's `online` is true when any host is.
  Per host: `online`/`targetId` come from that host's live bridge
  advertisement (`targetId` is `""` while offline), and `defaultModel` is
  that host's default. `queued` is the host's jobs for that agent not
  started yet: status `queued` or `claimed`, across every organization
  (`BridgeJobStore.PendingAgentJobs`, on the new index
  `idx_agent_jobs_owner_status`; migration `2026_10_agent_jobs_owner_status`).
- **Room** `GET /v1/rooms/{roomId}/platform-agents` uses the same entry
  shape, plus `addedBy`/`addedAtMs` (from the agent's earliest grant). The
  `hosts` are the hosts serving this room. Oldest added first.
  - **Reconcile first.** An agent added to the room (it has a live grant)
    is extended to every host that publishes it now and has no live grant
    there. The new grant keeps the original adder. Then each host's
    agent-host membership converges on "has an available grant here".
  - **Add.** `POST` creates a membership + grant for every publishing host
    that does not serve the room yet: 201 when one was created, 200 when
    all already served it, 404 when no host publishes it. The response is
    `{agent}` in the room entry shape.
  - **Remove.** `DELETE` revokes every host's grant of that agent. A member
    who may chat or any serving host may remove it. A host left with no
    grant loses its membership. A removed agent is never re-extended.
- **Default model.** The reserve 200 for a platform grant adds
  `defaultModel` (the host's, only when set). When the job named no model,
  the run's audit row records it. The bridge (`runBridgeJob`, after
  `authorizeBorrow`, which now returns a `borrowReservation{ID, DenyReason,
  DefaultModel}`) sets `job.Model` from it when the chat carried no
  `agentOptions.model`. Session options, the run report and the settle then
  all use it. A member's explicit model always wins.
- **Offline guard.** `realtime.PostChat` is the one chat publish path
  (typed and scheduled messages). After the people-chat check and before
  anything is persisted, published or queued, it asks
  `shareagent.ReadPlatformDrive`. The chat is refused when the room holds
  an available platform grant for the recipient owner's (core, provider),
  the author is not that owner, and the owner's bridge does not advertise
  the agent. The answer is 409 `{"error": "platform_agent_offline",
  "message": "This platform agent is offline — its host's computer is not
  connected. Try again later.", "hostUserId", "core", "provider"}`. No
  message and no job exist. Own agents and owner-issued borrows are never
  refused. An unreadable policy skips the guard: reserve still fails
  closed. Realtime gets `PlatformAgentGuard{Grants, Settings, Bridges}`
  from the shared store (no new env, no new service hop).
- **Usage** `GET /v1/platform/platform-agents/usage[?tzOffsetMin=]`
  (manage_platform) reads `store.BorrowTurns` with the new `PlatformOnly`
  (joins the grant, `source = platform`) and `AllTenants` filters.
  - Answer: `{today, month: {runs, members, rooms, tokens, cost}, topMembers:
    [{memberId, runs, tokens, cost}] (5), topRooms: [{roomId, roomName,
    tenantId, runs, tokens, cost}] (5), recent: [{atMs, memberId, roomId,
    roomName, tenantId, agent: "core/provider", hostUserId, model, cost}]
    (20, newest first), truncated}`.
  - Today and month start in the caller's offset (default UTC). Top lists
    rank by runs, then cost. `tokens` = reported input + output. `cost` is
    what the bridge reported (0 when unreported). Room names are resolved
    in each room's tenant.
- **Gateway.** The usage route is under `/v1/platform/*` (shareagent,
  manage_platform). `platform-agents` is no longer a `routeLabel` id
  collection, so the label is `/v1/platform/platform-agents/usage`; a room's
  `/platform-agents/{provider}` still collapses to `:id`.
- **Tests:**
  - shareagent: PUT touches only the caller's entry; multi-host list with
    per-host online, queued and default model; multi-host add, reconcile
    extension, unpublish and remove; room host state; reserve
    `defaultModel` and audit model; usage aggregation.
  - realtime: offline 409 with no job and no event; own agent, ordinary
    borrow and online host go through.
  - bridge: default model end to end (reserve answer → session model;
    explicit pick wins).
  - activity: two hosts' catalogs.
  - store (memory + MySQL): `PendingAgentJobs`, platform/all-tenant
    `BorrowTurns`, the policy.
  - Also gateway routes and `routeLabel`.

### Web

Implemented 2026-10-10 (sections 1–5). The platform-agent wire shapes live
in one file, `web/src/lib/platformAgents.ts` (policy `hosts[]`, member/room
agents with `hosts: [{hostUserId, online, queued, targetId, defaultModel?}]`),
the usage shapes in `web/src/lib/platformUsage.ts`, the endpoints in
`web/src/lib/platformAgentsApi.ts` (usage is read with
`?tzOffsetMin=<-getTimezoneOffset()>`).

- **Welcome card** (`views/WelcomeCard.tsx`, logic in `lib/onboarding.ts`) on
  Plaza (the landing view) and My rooms. A member with no rooms always sees
  it; a member with rooms sees it until they dismiss it
  (`bnbloc.welcomeDismissed.<subject>`); nothing renders until the room list
  is known. "Start a room with <agent>" picks the first offered agent with an
  online host that is not the member themselves, creates a private room named
  "My first room" (de-duplicated against the member's room names: "My first
  room 2", …) under the global id `my-first-room-<6 random>` (retried on a
  409), refreshes the session's room scope, adds the agent, queues a
  `composerAgentPicks` hand-off (`lib/team.ts`) and opens the room; the
  composer (`useInjectedAgent`) selects the agent once the room lists it.
  The secondary action opens the release hub with the macOS install guide
  (hidden in the desktop shell). With no available agent the card offers the
  desktop path only and says an admin can enable platform agents.
- **Starter prompts** (`views/composer/StarterPrompts.tsx`): four chips above
  the room composer while the feed is empty, nothing is working, and an agent
  is available (a member's agent, a local target, or a platform agent with an
  online host). A chip fills the composer, never sends. The empty feed then
  reads "This room is ready" instead of the agent-setup guidance.
- **Host routing** (`lib/platformRouting.ts`, `pickHost`): the room's last
  host for the agent if online and fewer than 3 jobs busier than the least
  busy online host, else the online host with the fewest queued jobs (ties
  keep the server's order). The last host is remembered per (member, room,
  agent) in localStorage (`bnbloc.platformHosts`, bounded to 200 entries) and
  recorded only after an accepted send. A room's serving hosts come from the
  room listing; their online/queued state from the fresher organization
  offer. `useRoomPlatformAgents` re-reads both every 20 s while visible.
  The picker shows one row per (core, provider) with each host's state
  ("hosted by admin (3 queued), rose", or "offline" + an Offline badge); the
  trigger shows a cloud (not one host's initial) and warns while offline.
- **Offline / busy:** sending to a platform agent with no online host is
  refused in the composer with the PRD sentence (no request, draft kept); a
  409 `platform_agent_offline` from the gateway shows the same toast, keeps
  the draft and re-reads the hosts. A queued platform job's row (feed and
  activity rail) reads "Queued — N requests ahead", N = the serving host's
  `queued` minus the job itself (`jobQueueAhead`).
- **Default model:** the routed host's `defaultModel` rides on the recipient
  (`RoomAgent.defaultModel`); `resolveModelCatalog` shows it as the model
  picker's default (added to the list if the catalog lacks it). The send
  carries no model unless the member picked one, so the server applies it.
- **Platform card** (`views/org/PlatformAgentsCard.tsx`): edits the caller's
  entry only (on/off, publish checkboxes, per-agent default-model select
  from the caller's own `GET /v1/my/agents` catalog with the free
  trial-channel models — ids starting `x-` — grouped first and labelled
  "trial channel · free"); other hosts are listed read-only. Below it the
  usage dashboard (`views/org/PlatformAgentsUsage.tsx`): today / this month
  tiles, expandable top members, top rooms and recent runs (with host and
  model), and a note when the server reports `truncated`.
- **Transparency** (`views/room/PlatformAgentsIndicator.tsx`): the room
  header shows "Platform agent · can read this room" (short label on a
  phone) for every member; its popover lists each agent, its hosts with
  online/queued state, who added it, and that runs happen in the host's
  sandbox. The Members panel lists hosts per agent.
- Tests: `lib/platformAgents.test.ts`, `lib/platformRouting.test.ts`,
  `lib/onboarding.test.ts`, `lib/platformUsage.test.ts`,
  `lib/agentCatalogs.test.ts`, `views/room/agentJobLabels.test.ts`. Verified
  against a mock gateway in Chromium (desktop and 390 px): welcome → create
  body `{roomId, name: "My first room", visibility: "private"}` → add → room
  opens with the agent selected; chip fills without sending; offline refusal
  sends nothing; 409 toast keeps the draft; routing picks the least busy host
  and remembers it; console PUT body `{enabled, agents: [{core, provider,
  defaultModel}]}`; usage dashboard; header popover.
