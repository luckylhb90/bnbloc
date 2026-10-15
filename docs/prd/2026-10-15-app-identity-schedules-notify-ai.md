# Apps that know who you are, and run on time, notify and use AI (2026-10-15)

Status: proposed (2026-10-15). Builds on
`docs/prd/2026-10-12-interactive-apps.md` (the app base: a document store,
`me()`, the two platform rules), schedules (`internal/schedule`), notifier
components (`docs/notifications.md`) and assists
(`docs/architecture/ai-assist.md`).

## 1. Why

A team lead asks the App builder for a weekly report app:

- every Thursday morning it reminds the team in their group chat;
- each member signs in with the organization's SSO and fills in their log,
  without typing their name;
- one click turns the logs into a deliverable weekly report.

Today the agent cannot build that, and says so:

- **Identity.** `me()` returns a per-app pseudonym and, for a member, a
  display name. It has no email or login name. The display name is
  editable and may not match the login, so the agent has to ask people to
  type who they are.
- **Time, notify, AI.** An app's page can read and write its data, nothing
  else. A schedule only posts a chat message. A notification only follows
  platform events (mentions, agent replies). A model call needs a member's
  session and a fixed task.

## 2. Principles (unchanged, applied here)

- **A base, not a product.** The platform adds the few capabilities an app
  cannot have on its own, with the rules only the platform can enforce. A
  weekly report, a poll or a roster is the app's own code.
- **No new load on agents.** The agent writes an app the way it does today:
  the SDK grows by a few calls, documented in the interactive-apps skill
  with examples. It learns no new tool and no new rule.
- **Components, not built-ins.** Delivery is notifier components' (the
  organization's group-chat bot is one already). Model access is the assist
  model's, and what the app asks is the app's prompt. Nothing names a chat
  product, a model or a report.
- **One owner per rule.** Identity, schedules, notifications and model
  calls each keep their one owner. The app path is a new caller of each,
  not a second copy.

## 3. Identity: who is filling in

`me()` adds the caller's **verified account**, when the caller signed in:

```ts
me(): Promise<{
  id: string;          // per-app pseudonym (unchanged)
  name?: string;       // display name (unchanged)
  account?: {          // only for a signed-in caller (member, or org link)
    login: string;     // the SSO subject: what people sign in with
    email?: string;    // the IdP's email, when it asserts one
  };
}>
```

- **Where it comes from.** Sign-in already receives both. The auth
  provider's assertion carries the subject and email (`auth.Identity`), and
  the session and refresh family keep them (`store.RefreshFamily.Email`).
  Organization sync records them too (`store.OrgSyncSubject`). One store
  becomes their owner (`user_accounts`: subject → email, written at every
  sign-in), and `me()` reads it.
- **Who gets it.**
  - A member opening the app from bnbloc gets it.
  - So does a person opening an org or signed-in link: they signed in.
  - An anonymous visitor (an `anyone` link) has no account and gets none.
  - The caller only ever sees their own account. Others' accounts are
    visible to the app's owners only, in the data read-back (`_author`),
    as names are today.
- **Why it is safe to give the page.** The page is the caller's own page.
  An email the IdP asserted is no more private to its owner than their
  display name. The page has no network, so it can only store what it reads
  in the app's own data, under the platform's privacy rule.
- **The skill** says: when an app needs to know who someone is, use
  `me().account.login`, read-only; ask people to type a name only on an
  `anyone` link.

## 4. Schedules that act: a task in plain words, run by AI

Revised 2026-10-15 (the first draft had apps ship `on/<name>.js` handlers
run in a JS sandbox): schedules should be general and AI native, not a
code runtime per app.

A schedule is **when** plus **what**. "When" is `internal/schedule`, as
today. "What" gains a third kind next to `room` (post a message) and
`agent` (a message an agent works on): **`ai`**, a task in plain words
("remind whoever has not filled in this week's report, with the link")
that the platform's assist model carries out with a small set of
**actions**.

- **No code to write.** Nobody writes a handler: not the agent that built
  the app, not the member. A member says what should happen and when, in
  their own words, from the app's card ("Automate…") or the room's
  schedules; the `draft-schedule` assist turns it into the recurrence and
  the task. The agent's skill does not change.
- **Actions are a registry, each with its one owner.** An action is a name,
  a description, an input schema and a function; the run offers them to
  the model as tools (OpenAI-compatible `tools`). The first ones:
  - `app_data` (`list`, `find`, `get`): the room's apps' data, read as the
    schedule's owner (an app owner: every document, `_author`), through
    the room service's app API, the same one the room tool uses;
  - `notify`: section 5's one function (members, logins, `channel:`);
  - `post_message`: a message in the room, as the owner (PostChat);
  - `ask_agent`: a message to one of the room's agents, as the owner (the
    `agent` recipient's path: borrow rules at drive time), for work that
    needs files or judgement ("write the report").

  Adding a capability later is adding an action; delivery stays the
  notifier components', and a notifier that delivers to groups extends
  `notify` without a new action.
- **How a run goes.** Realtime's scheduler claims the slot (as today) and
  hands an `ai` slot to the gateway (`POST /internal/v1/schedules/run`,
  service-signed), because only the gateway calls the model. The gateway
  runs the loop: the task as the instruction, the room's apps and the
  clock as fenced context, tool calls answered by actions, until the model
  answers without a call. It records a short summary of what it did on the
  schedule ("notified 3 people", "nothing to do").
- **Bounds that are real, not clocks.** A run has a step cap (tool calls),
  a per-run notice cap, and the assist budget; no wall-clock cap. Each
  notice's dedup key is the schedule, the slot and the recipient, so a
  re-claimed slot (a crash) never notifies twice.
- **Try it first.** "Run now" in dry mode runs the loop with actions that
  read for real and only report what they would send or post. The member
  sees "would notify maya, jack: …" before turning it on.
- **Why not an agent turn.** An agent turn needs someone's desktop online
  and costs a full turn every time; an `ai` run is a few assist calls in
  the gateway. When the work does need an agent, `ask_agent` hands it on.
- **Who.** The schedule's owner is its creator, as today: they must still
  be a room member (else the schedule pauses), the run acts with their
  rights, spends their organization's assist budget, and is audited as
  `kind: assist` with the schedule id. An organization with
  `assistDisabled` cannot create or run `ai` schedules.

## 5. Notify from an app

The SDK adds `app.notify({to, title, text, url?})` from a page; an `ai`
schedule's `notify` action is the same function.

- **Recipients** are people, not addresses:
  - `to: "room"` means the room's members;
  - `to: ["login", …]` means those accounts in the organization;
  - `to: "channel:<name>"` means a group the organization's notifier knows
    (its group chat), when the notifier offers group delivery.
- **Delivery** goes through `notify.Emitter`, the one producer seam, as a new
  kind `app_notice`. Members' preferences apply: a member can mute an app.
  Notifier components deliver it: the organization's bot, a member's
  personal bot, desktop or web push, whichever the member has.
- **Group delivery is the notifier's.** A notifier package declares
  `groups: true` in its discover answer when it can post to a named group.
  A `channel:` notice is then handed to it. Without such a notifier, the
  call fails with a sentence the app shows.
- **Rules only the platform can enforce:**
  - who may notify: a page only for its caller's own link session, a
    schedule as its owner;
  - rate limits per app and per recipient;
  - every notice is signed with the app's name and links back to it.

## 6. AI from an app

The SDK adds `app.ai({prompt, input?, json?})`, from a page.

- **One model owner.** The call is an assist run: the organization's assist
  model (or the platform's), its key, its cost budget and its audit row
  (`kind: assist`, app id). It is not a new model config.
- **The prompt is the app's.** No new assist task component per app: the
  app sends its prompt, the platform fences the input as data (prompt-text
  rule: untrusted text is fenced), and returns text or JSON.
- **Who pays and who may.**
  - From a page, the call runs on behalf of the caller, and counts against
    their per-member assist rate. An anonymous visitor may not call it.
  - The organization's `assistDisabled` turns it off for apps too.
- **Why not the room's agent.** "Turn my log into a report paragraph" is a
  one-shot text transform, not an agent turn. A turn would need someone's
  desktop online and cost far more. When the report needs the agent
  (files, a deck, judgement across everyone's logs), the app posts a
  request to the room, an agent schedule, as today.

## 6a. Files from an app

An app can only store JSON documents of at most 64 KiB, so a form that
collects a photo, a screenshot or a receipt has nowhere to put it (base64
in a document fits only a few dozen KB). The SDK adds:

```ts
app.files.upload(file: File): Promise<{ id: string; name: string; type: string; size: number }>;
app.files.url(id: string): Promise<string>;   // a short-lived link to show or download it
```

- **One owner of what a file may be.** Uploads go through the chat
  attachments' type list and content checks (`wire.AttachmentTypes`), the
  same blob store, and the same serving: only sniff-verified raster images
  inline, every other file an opaque, sandboxed download.
- **The app's rules apply.** A file belongs to the app (its room and
  tenant), and its uploader is the caller's `_by`. It is readable by its
  uploader and the app's owners, unless uploaded `{public: true}`. An
  anonymous visitor may upload under stricter rate limits. Per-file size is
  the attachment cap (`wire.MaxAttachmentBytes`), and an app's files count
  against the app's total cap.
- **The app keeps the reference.** The app stores the returned id in its
  own document (`{receipt: id}`); deleting the document does not delete the
  file, and a wipe of the app's data deletes both.
- **Agents read them back** through `app_data` (a file id resolves to its
  name, type and a readable copy in the workspace), so the agent can look
  at what people submitted. No new tool.

## 7. The weekly report app, end to end

1. The lead asks the App builder. The agent builds `index.html`: a form
   whose name field is `me().account.login`, read-only; a "Polish with AI"
   button calling `app.ai`; a lead view listing who has filled in. Nothing
   about schedules.
2. On the app's card the lead clicks "Automate…" and types "every
   Thursday 09:00, remind in the team-weekly group whoever has not filled
   in this week, with the link; Friday 18:00 ask the agent to write the
   team report". The draft shows two schedules (an `ai` task and an
   `agent` message); "Try it" shows "would post in team-weekly: …"; the
   lead turns them on.
3. Thursday 09:00, the run reads the app's data, finds who is missing and
   calls `notify` to the group; the organization's bot posts. Members sign
   in, the form knows them, and they write and polish their log.
4. Friday 18:00 the agent schedule asks the agent for the report, which
   reads the logs with `app_data` as today.

## 8. Design review

| Lens | Answer |
|---|---|
| Actors | An app's owners create its schedules; a schedule acts as its creator. A page acts as its caller; an anonymous visitor gets no account, no AI and may notify no one. |
| Time and lifecycle | An `ai` run is bounded by steps and budget, not a clock, and is stateless between runs: its state is the app's data. A schedule pauses when its creator leaves (as today). A re-claimed slot re-runs with the same dedup keys. |
| Scale and cost | Notices are rate-limited per app and recipient and capped per run; AI calls spend the assist budget and the per-member rate; a run has a step cap; schedules keep their minimum spacing (5 min). |
| Failure and recovery | A failed run is recorded on the schedule ("last run failed: …") with its summary, visible in the schedules list; the slot is not retried past the misfire grace. Notification delivery has the outbox's retries. |
| Boundaries and privacy | A caller sees only their own account. An `ai` run has no network of its own: only its actions, each acting with the owner's rights; app data reaches the model fenced as data. |
| Abuse and untrusted input | App input to the model is fenced as data. A notice's text is plain text and links to the app; notifiers render it as text. |
| One owner | Accounts: the new store, written at sign-in. When: `internal/schedule`. Delivery: `notify.Emitter` and notifier components. Model: `internal/assist`. Actions: one registry, each action calling its owner. |
| Defaults and fallbacks | No IdP email: `account.email` absent. No group-capable notifier: `channel:` fails with a sentence, member notices still work. Assist off: `app.ai` fails with a sentence. |
| External contracts | SDK: `me().account`, `files`, `notify`, `ai`. Schedules: recipient `ai` (task text, actions). Notifier discover: optional `groups`. Wire notice kind `app_notice`. All additive. |
| Load on users and agents | The member says what and when in words, and tries it before turning it on. The agent learns three SDK calls; nothing about schedules. |
| Release and compatibility | Server first (accounts, SDK, notify, ai, the `ai` schedule run), then the skill update, then notifier packages that add group delivery. Older apps keep working. |
| Observability | Every notice and AI call is audited with the app or schedule id; a run records its outcome and a one-line summary on the schedule. |

**Pre-mortem.**

1. **"Everyone got pinged twice."** A crashed run re-claimed, or a task
   that both pinged the group and each member. A notice's dedup key is the
   schedule, the slot and the recipient, so a re-run is a no-op; the dry
   run shows both before anything is sent.
2. **"The bot spammed the group."** A model looping on `notify`. The step
   cap, the per-run notice cap and the per-app rate limit stop it; the
   schedule pauses after repeated failures.
3. **"It did something nobody asked."** The model only has the actions the
   registry offers, each acting with the owner's rights; the task text is
   the owner's own words, app data is fenced as data, and "Try it" shows
   what a run would do.
4. **"It used the wrong person's AI budget."** A page's call is always the
   caller's; a schedule's is its owner's, and the audit row says whose.

## 9. Milestones

- **A1:** accounts store, `me().account`, the skill update. This is
  enough for "no need to type the login". Done (ff7d6670).
- **A2:** `app.files` (upload, url), and `app_data` reading them.
- **A2b:** `app.ai` from pages.
- **A3:** `app.notify` to members, with the `app_notice` kind and mutes.
- **A4:** `ai` schedules: the action registry (`app_data`, `notify`,
  `post_message`, `ask_agent`), the gateway's run loop with tool calls,
  dry run, "Automate…" on the app card and `draft-schedule` drafting the
  task.
- **A5:** group delivery in the notifier contract (`groups`), and the
  organization's group-chat notifier package adding it.
