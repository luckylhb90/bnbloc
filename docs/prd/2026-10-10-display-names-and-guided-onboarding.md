# Display names, privacy mode and a guided first-run tour (2026-10-10)

Status: implemented (2026-10-10); awaiting deploy + live verification

## Why

- **Privacy:** the UI shows user ids everywhere, and ids are e-mail
  addresses:
  - agent handles ("jane.d@example.com/claude-code");
  - "Borrowed from …";
  - member lists, platform hosts, admin tables.

  This leaks personal information in screenshots and demo videos (the README
  demo), and reads poorly.
- **Onboarding:** the welcome card gets a new member into a room, but then
  nothing guides them. A first-time user still does not know what to do
  next or what the product can do. The guidance should work like a game
  tutorial: step by step, each step advancing on the real action.

## 1. Display names (product)

- **Profile:** every user may set a display name (1–40 chars, trimmed, no
  control characters) in Settings → Profile. With none set, the display name
  defaults to the local part of the id (before `@`), or the id itself when
  it has no `@`.
- **Server:**
  - `PUT /v1/my/profile {displayName}` sets it; empty clears it.
  - `GET /v1/my/profile` reads it.
  - `GET /v1/users/profiles?ids=a,b,…` (any signed-in user, at most 200 ids)
    → `{profiles: [{userId, displayName}]}`, with the default applied.
    It answers for any id, but never with more than the display name.
  - Storage: a `user_profiles` table (user_id PK, display_name,
    updated_at_ms); migration `2026_10_user_profiles`.
- **Web:** one `UserName` component and one cached resolver (batched
  lookups) render every person: message authors, agent handles
  (`<display name> · <provider>`, e.g. "Maya · claude-code"), "Borrowed from
  <name>", member and people lists, typing/presence, platform hosts, job rows,
  notifications, usage tables, the share dialog and the public share page
  ("Shared by <name>").
  - The id stays the identity: @mentions, recipients, permissions and URLs
    are unchanged. The mention picker shows names and inserts the canonical
    handle.
  - E-mail addresses are shown only where the address itself is the point
    (inviting by e-mail; org/platform member administration, as secondary
    text).
- **Public share page:** "Shared by" uses the display name. The server
  resolves it, so the page needs no authenticated lookup; the room service's
  share resolve returns the display name.

## 2. Privacy mode (demos)

- Settings → Privacy: a local toggle "Hide email addresses" (this browser
  only, localStorage). When on:
  - every place that would show an e-mail address shows the display name
    instead (admin member tables, audit log, invites list, usage tables,
    API-token lists);
  - an address typed by the user stays visible while they type it (invite
    field) but is masked once shown back.
- It is a presentation setting only: nothing is hidden from the server.
- A small "Privacy mode" badge in the account menu shows it is on.

## 3. Guided first-run tour

- **Spotlight tour** for a new member: the page dims except the target
  element, and a coach card shows the step text, progress and Skip.
  - It starts by itself on a brand-new member's first screen (the welcome
    card, when a platform agent is available) at a **Start** step that
    spotlights "Start a room with <agent>"; opening any room moves on.
    "Take the tour" in the checklist restarts it at the room steps.
    (Revised 2026-10-10: it first started only after the room opened, which
    left a new member on the home view with just the checklist.)
  - Steps:
    0. **Start** (welcome card button): "One click creates your first room
       with an AI agent. Nothing to install." Advances when a room opens.
    1. **Room** (room header): "This is your room. People and agents work
       together here; everything stays in this conversation." [Next]
    2. **Agent** (agent picker): "This is the agent you're talking to. It runs
       on a teammate's machine, so there's nothing to install." [Next]
    3. **Ask** (starter prompts + composer): "Pick an example or write your
       own, then press Send." Advances when the member sends a message (real
       action).
    4. **Wait** (the feed): "The agent is working…". Advances on the first
       agent reply.
    5. **Results** (Files tab / workbench): "Anything the agent makes (pages,
       docs, images) shows up here. Open it to preview." Advances when a
       deliverable opens, or on Next.
    6. **Share** (share button, when a deliverable is open): "Share a public
       link to show your work." [Done]
  - Mobile: the same steps with bottom-sheet coach cards.
  - Esc or Skip ends the tour; it can be resumed from the checklist.
  - Keyboard accessible; respects reduced motion.
- **Getting-started checklist:** a collapsible panel above the account button
  with a progress bar. Items tick automatically from real events:
  1. Send your first message
  2. Get your first agent reply
  3. Ask the agent to make something (a deliverable appears)
  4. Open a preview
  5. Share a link
  6. Invite a teammate (room invite created or a member added)

  Each item has a "Show me" that deep-links (opens the first room / starts
  the matching tour step). When all six are done, a short celebration plays
  and the panel collapses into a "You're set" chip that can be dismissed.
- **State:** per user in localStorage (`bnbloc.onboarding.v1:<subject>`).
  Detection uses the events the client already sees: own chat echo, agent
  reply, `artifact_updated`, workbench open, share created, invite/member
  add. Members with existing activity are not forced into the tour; the
  checklist shows, pre-ticked where the client can tell.
- **Done when:** a headless run as a brand-new member completes the whole
  tour on production and ticks items 1–5 without guessing where to click.

## Implementation notes

### Display names + privacy (backend)

Status: implemented (server side of §1).

- **Owner: the room service.** It already is the user-facing directory
  (members, `GET /v1/users/{id}/rooms`), and its share resolve names the
  sharer for the public page, so that read stays a local store lookup with
  no new internal hop. The gateway routes `/v1/users/*` there already;
  `/v1/my/profile` is routed there too. All three routes need only the read
  permission (a viewer may set their own name) and are not rate limited.
- **One rule:** `store.DisplayName(userID, stored)` (internal/store
  `user_profiles.go`): the stored name, else the id's local part (before
  `@`), else the id. `store.NormalizeDisplayName` validates input: trimmed,
  at most 40 characters (code points; the column is `VARCHAR(40)`), valid
  UTF-8, no control characters (`unicode.IsControl`: C0, DEL, C1, so a tab
  inside is refused too); empty clears. The room service's profile routes
  and the share resolve both go through `Service.displayNames`, which
  applies that rule; the old local-part helper in share resolve is gone.
- **Routes** (wire shapes in `internal/wire/profiles.go`):
  - `GET /v1/my/profile` → `{userId, displayName, isDefault}`
    (`wire.MyProfile`; `displayName` has the default applied, `isDefault`
    is true when no name is set).
  - `PUT /v1/my/profile {displayName}` → the same shape after the write;
    400 `{error}` naming the rule for an invalid name (nothing changes);
    empty or all-space clears it.
  - `GET /v1/users/profiles?ids=a,b,…` → `{profiles: [{userId,
    displayName}]}` (`wire.UserProfile`): any signed-in caller, any ids
    (comma-separated; repeated `ids` params are joined), each distinct id
    once (case-insensitively) in request order, echoing the id as sent,
    default applied. No ids → 400; more than 200 distinct ids → 400.
  - No caller → 401; no profile store configured → 503.
- **Share resolve:** `POST /internal/v1/shares/resolve` returns
  `sharedBy.displayName` from the profile (default applied), so the public
  share page shows "Shared by <display name>" without an authenticated
  lookup.
- **Storage:** `user_profiles (user_id PK, display_name, updated_at_ms)`,
  global per user (not per tenant); no row = the default, clearing deletes
  the row. Migration `2026_10_user_profiles` creates it from the embedded
  schema (both `schema.sql` copies + `migrations.lock`). Ids match
  case-insensitively (MySQL collation; the memory store folds case).
  Room exports are unchanged (they carry ids, never names).
- **Observability:** `profile` is a route word, so `/v1/my/profile` labels
  verbatim; `/v1/users/profiles` labels `/v1/users/:id` (users is an id
  collection; it is the only three-segment users route).
- **Tests:** `internal/store/user_profiles_test.go` (rule, validation,
  memory + MySQL store), `services/room/profiles_test.go` (default, set,
  clear, validation, batch order/dedupe/cap, auth, 503, share resolve uses
  the name), `services/gateway/profile_route_test.go` (routing, read
  permission, no rate limit), `internal/obs` route labels.

### Display names + privacy (web)

Status: implemented (web only).

- **Resolver:** `web/src/lib/displayNames.ts` holds the pure parts:
  - the default rule (`defaultDisplayName`: local part, or the id);
  - `formatAgentHandle(name, provider)` → "Maya · claude-code", and
    `agentDisplayLabel` (core appended);
  - name validation (1–40, no control characters);
  - e-mail masking (`maskEmail`, `maskEmailsInText`);
  - `NameResolver`: one case-insensitive cache with batched, deduped
    lookups, chunked at 200 ids, one notification per batch. A failed lookup
    (e.g. a gateway without the endpoint) backs off 5 min.
  `peek()` never returns an address: it shows the client-side default until
  `GET /v1/users/profiles` answers, so the UI works before the backend is
  deployed. `lib/profilesApi.ts` holds the three endpoints.
  `lib/useDisplayName.ts` holds the app's single resolver, the
  `useDisplayName` / `useDisplayNames` hooks and `displayNameOf` (for
  toasts).
- **Components** (`web/src/views/UserName.tsx`):
  - `UserName`: the display name, never the id, not even as a hover title.
  - `AgentHandle` / `useAgentHandle`: an agent by its owner's name.
  - `ActorName`: a canonical actor, person or `owner/provider`.
  - `UserNames`: a list of names.
  - `UserWithEmail`: member administration, with the address as secondary
    text, dropped in privacy mode.
  - `Email`: an address only where the address is the point.
  - `EmailInput`: a field holding an address, masked in privacy mode while
    not focused, so a typed address stays visible while typing.
  `ActorLabel` (feed and activity) renders through these components. The
  duplicate `owner/provider` label helpers were collapsed into
  `agentDisplayLabel`: `teamDashboard.agentHandle` (unused) is deleted, and
  `runAgentLabel`, `hostsLabel` and `schedules.recipientLabel` take a name
  lookup.
- **Surfaces covered:**
  - **Room:** feed authors, agent replies and avatars; "Borrowed from"; the
    working indicator; the activity rail (jobs, working agents, artifact
    "by"); typing and presence.
  - **Members:** the members panel (people, agents, removal toasts and
    confirmations); the platform agents section, indicator, picker hosts and
    the add-agent confirmation.
  - **Composer:** the agent picker (owner, trigger initial, title); the
    @-mention list (shows names and matches on name or handle, inserts the
    canonical handle); reference chips and quotes.
  - **Room tools:** schedules (list, recipient options, borrow warning);
    tasks (strip and badge); handoff and template-start invites; room
    settings toasts (add member, transfer).
  - **Workbench:** the deliverables author filter, file cards, version
    labels, and the share dialog link list.
  - **Library and search:** prompt, template and shared-prompt authors;
    global search authors.
  - **Team:** the agents tab (agent, "for <requester>", runs link); runs
    browser (agent, requester, borrowed from); usage billing (owner,
    borrower); budgets usage; quota requests (list, toasts) and the
    request dialog.
  - **Organization / Platform:**
    - members, join requests, usage audit trail and the audit log actor
      (`UserWithEmail`);
    - audit subject and detail (masked in privacy mode);
    - public links created-by; org sync "saved by"; trusted keys "added
      by";
    - the platform card: other hosts, usage top members, recent runs
      member and host.
  - **Account menu:** name and initial. "Signed in as" shows the address
    outside privacy mode only.
  - **Borrow dialog:** recent borrowers.
  - **Public share page:** uses the server-resolved `sharedBy.displayName`
    unchanged.
- **Settings → Profile** (`views/settings/ProfileSettings.tsx`): the display
  name field. The placeholder shows the default; an empty value clears the
  name. The save is optimistic through `nameResolver.set` and reverts on
  failure.
- **Settings → Privacy:** "Hide email addresses" (`lib/privacyMode.ts`,
  localStorage `bnbloc.privacyMode`, synced across tabs). While it is on:
  - the account menu shows a "Privacy mode" badge;
  - `ErrorBox` masks addresses in plain-text errors;
  - the sidecar account line is masked;
  - `EmailInput` covers the budgets member field, room transfer and
    add-member, and the desktop Owner field.
- **Known limit:** an @-mention keeps the canonical handle in the message
  text, as this PRD specifies. A mention of an e-mail id therefore still
  shows the address inside a message body. Rendering mention tokens by name
  is a follow-up.
- **Verified:**
  - Unit tests (`node --test`): `lib/displayNames.test.ts` (default rule,
    handle formatting, validation, masking, batching, dedupe, chunking,
    back-off, mention labels and matching), plus updated
    `platformAgents`, `schedules`, `teamDashboard` and `agentRuns` tests.
    91/91 pass, and `pnpm build` passes.
  - Mock-gateway run (all ids are e-mails): screenshots of the room feed
    with an agent reply and a borrowed send, the agent picker, mention
    autocomplete, the members panel, the platform indicator, the platform
    card usage, admin members (privacy off and on), Settings and the
    account menu badge.
  - A DOM check of `innerText`, unfocused input values, and `title` /
    `aria-label` found no `@domain` on any surface. The exceptions are
    admin members with privacy off (secondary address, by design) and the
    focused field being typed in. The same check passed with the profiles
    endpoint returning 404 (client default rule).

### Guided tour + checklist (web)

Status: implemented (web only, no new dependency).

- **Pure logic** (node-tested, `web/src/lib/onboardingTour.test.ts`,
  `tourLayout.test.ts`):
  - `lib/onboardingEvents.ts` — the in-tab event bus and the room-event
    mapper. Events: `message_sent` (`seq`, `toAgent`), `agent_reply` (`seq`),
    `deliverable`, `artifact_opened`, `share_created`, `invite_created`.
    Each carries a source: `action` (the member's own request answered),
    `stream` (live, may replay on connect) or `history` (a persisted page or
    list).
  - `lib/onboardingTour.ts` — the steps, the step machine, event → checklist
    tick, the persisted state (`bnbloc.onboarding.v1:<subject>`, subject
    lower-cased, malformed fields fall back) and the start conditions.
  - `lib/tourLayout.ts` — spotlight and coach-card geometry.
- **Steps as implemented** (`data-tour` anchor → how the step advances):
  1. Room (`room-header`): Next.
  2. Agent (`agent-picker`): Next.
  3. Ask (`composer`, starter prompts included): the member's own send
     (`action`, addressed to an agent). Sending early, during step 1 or 2,
     jumps straight to Wait.
  4. Wait (`feed`): the first live agent reply whose sequence is after the
     sent message. A replayed or history reply never advances it.
  5. Results (newest `deliverable` card, else the `files` tab): opening a
     deliverable, or Next.
  6. Share (`share` in the open tab's toolbar, else `files` with the hint
     "Open a file from Files, then use Share…"): a created link, or Done.

  "Invite a teammate → Show me" runs a one-step spotlight on the room's
  Settings button (`invite`). It finishes on an invite or member add, or on
  Done.
- **Producers** (one emit line each, at the seam that already sees the event):
  - `useRoomFeed` maps the own echo and agent replies, from history pages
    and from the live stream.
  - `useRoomActivity` maps `artifact_updated`. The room's existing
    deliverables count as history.
  - `useRoomMessageActions` emits the send action, with the POST's sequence.
  - `useWorkbench.openArtifact` emits a deliverable opened.
  - `sharesApi.create` emits a link created.
  - `api.createInvite` and `api.addMember` emit an invite. This covers room
    settings, the handoff banner and the template start card.
- **Overlay** (`views/onboarding/TourOverlay.tsx`, mounted once in `App`):
  - It shows only inside a room, and steps aside while a dialog that does
    not hold the target is open. A layer that holds the target, such as the
    phone's preview overlay or the rail drawer, does not hide it.
  - The dim layer is a rounded box-shadow cutout that never takes pointer
    events, so the member always uses the real control. It follows the
    target every frame and is clipped to the target's scroll containers. A
    target that is scrolled away is scrolled into view once per step.
  - The coach card is placed below, above or beside the target, or inside a
    target that fills the screen. Below 640 px it is a bottom sheet, moved
    to the top only when the target would sit under it (the composer).
  - Next-only steps trap focus in the card, with Next focused. Steps that
    wait for an action leave the page usable from the keyboard; the Ask step
    puts the cursor in the composer (not on a phone).
  - Esc skips the tour unless something else handled the key.
  - Under reduced motion, transitions and the pulse are off.
- **Checklist** (`views/onboarding/GettingStarted.tsx`): it renders through
  Shell's `footerNotice`, above the account button. The callback now also
  receives `closeDrawer`, so Show me closes the phone drawer.
  - It shows a progress bar, the six items and "Take the tour" (which
    restarts the tour at step 1). It can be folded to a one-line progress
    bar.
  - On the collapsed rail it is a progress ring that opens the list beside
    it.
  - "Show me" starts the matching step: send, reply and make start step 3;
    preview starts step 5; share starts step 6; invite starts the invite
    spotlight. Show me then opens the current room, else the first room,
    else My rooms (the welcome card).
  - When all six are done, a CSS confetti burst and glow play once
    (reduced motion: a static highlight). The panel then becomes a
    dismissible "You're set" chip.
- **Start conditions**:
  - The tour starts by itself only from the welcome card's "Start a room",
    and only for a member with no rooms before, no checklist ticks and no
    earlier tour.
  - Members with activity get the checklist pre-ticked from history (own
    messages, agent replies, existing deliverables) and are never
    auto-started.
- **Verified**:
  - `pnpm build`, and `node --test` across all web tests.
  - A mock-gateway headless run (`/tmp/tour-e2e/run.mjs`, screenshots in
    `/tmp/tour-shots/`) at 1280 px and at 390 px (mobile). The mock streams
    the echo, an agent reply and an `artifact_updated`, and the run makes no
    guesses: Start a room → Next → Next (Enter) → starter prompt + Send →
    Wait → reply → Results → open card → Share → Create link → tour done
    with items 1–5 ticked. Then Show me (invite) → Settings → Create invite
    → 6/6 → celebration → chip → dismissed.
  - An existing member: no auto-start; send, reply and make pre-ticked;
    "Take the tour" starts it; Esc skips it.
  - Under reduced motion, no confetti renders.
