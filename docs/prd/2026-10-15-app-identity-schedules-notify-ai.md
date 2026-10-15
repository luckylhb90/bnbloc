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

## 4. Schedules that act on the app

A schedule gains a third recipient, next to `room` and `agent`: **`app`**.

- **What it does.** At its time, it calls the app's own handler, with no
  agent turn and no page open: a small server-side function the app ships,
  `on/<name>.js`, run sandboxed with the SDK only.
  - It reads and writes the app's data as an owner.
  - It may call `notify` and `ai` (sections 5 and 6).
  - It has a time and memory cap per run, and no network.
- **Why not an agent turn.** "Remind the team on Thursday" must not
  depend on someone's desktop being online, and must not cost a model turn
  each week. An agent schedule stays the way to ask for judgement ("write
  the report"); an app schedule is the app's own logic.
- **Who creates it.** The app's owners. Schedules are created as today
  (`/v1/rooms/{room}/schedules`), with `recipient: {kind: "app", appId,
  handler}`, and owned by the member who created them, so pausing,
  limits and "the creator left" work as today. The agent creates none: it
  writes the handler, and the member turns the schedule on from the app's
  card ("Run every Thursday 09:00"), or the app's manifest proposes it.
- **One engine.** `internal/schedule` stays the one owner of when.
  Realtime's scheduler gains the new recipient's dispatch, nothing else.

## 5. Notify from an app

The SDK adds `app.notify({to, title, text, url?})`, from a page or a
handler.

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
    handler as its schedule's owner;
  - rate limits per app and per recipient;
  - every notice is signed with the app's name and links back to it.

## 6. AI from an app

The SDK adds `app.ai({prompt, input?, json?})`, from a page or a handler.

- **One model owner.** The call is an assist run: the organization's assist
  model (or the platform's), its key, its cost budget and its audit row
  (`kind: assist`, app id). It is not a new model config.
- **The prompt is the app's.** No new assist task component per app: the
  app sends its prompt, the platform fences the input as data (prompt-text
  rule: untrusted text is fenced), and returns text or JSON.
- **Who pays and who may.**
  - From a page, the call runs on behalf of the caller, and counts against
    their per-member assist rate. An anonymous visitor may not call it.
  - From a handler, it runs on behalf of the schedule's owner.
  - The organization's `assistDisabled` turns it off for apps too.
- **Why not the room's agent.** "Turn my log into a report paragraph" is a
  one-shot text transform, not an agent turn. A turn would need someone's
  desktop online and cost far more. When the report needs the agent
  (files, a deck, judgement across everyone's logs), the app posts a
  request to the room, an agent schedule, as today.

## 7. The weekly report app, end to end

1. The lead asks the App builder. The agent builds `index.html`: a form
   whose name field is `me().account.login`, read-only; a "Polish with AI"
   button calling `app.ai`; a lead view listing who has filled in. It also
   builds `on/thursday.js`: it reads who has not filled in this week, and
   calls `app.notify({to: "channel:team-weekly", …})` with the link.
2. The app's card offers "Run on/thursday every Thursday 09:00". The lead
   turns it on: a schedule, the lead as owner.
3. Thursday 09:00, the handler runs and the organization's bot posts in
   the group. Members sign in, the form knows them, and they write their
   log and polish it.
4. Friday, the lead asks the agent "write the team report from this week's
   logs". The agent reads them with `app_data`, as today.

## 8. Design review

| Lens | Answer |
|---|---|
| Actors | An app's owners create its schedules; a schedule acts as its creator. A page acts as its caller; an anonymous visitor gets no account, no AI and may notify no one. |
| Time and lifecycle | A handler run is bounded (time, memory) and stateless between runs; its state is the app's data. A schedule pauses when its creator leaves (as today) or the app is deleted. |
| Scale and cost | Notices are rate-limited per app and recipient; AI calls spend the assist budget and the per-member rate; handlers have a per-run CPU cap and a per-app daily run cap. |
| Failure and recovery | A failed handler run is recorded on the schedule ("last run failed: …") and visible on the app's card; it does not retry endlessly. Notification delivery has the outbox's retries. |
| Boundaries and privacy | A caller sees only their own account. Handlers run without network; a notice carries only what the app wrote into it. |
| Abuse and untrusted input | App input to the model is fenced as data. A notice's text is plain text and links to the app; notifiers render it as text. |
| One owner | Accounts: the new store, written at sign-in. When: `internal/schedule`. Delivery: `notify.Emitter` and notifier components. Model: `internal/assist`. |
| Defaults and fallbacks | No IdP email: `account.email` absent. No group-capable notifier: `channel:` fails with a sentence, member notices still work. Assist off: `app.ai` fails with a sentence. |
| External contracts | SDK: `me().account`, `notify`, `ai`. Schedules: recipient `app`. Notifier discover: optional `groups`. Wire notice kind `app_notice`. All additive. |
| Load on users and agents | The member turns a schedule on from the app's card. The agent learns three SDK calls and a handler folder from the skill's examples. |
| Release and compatibility | Server first (accounts, SDK, notify, ai, the scheduler's dispatch), then the skill update, then notifier packages that add group delivery. Older apps keep working. |
| Observability | Every notice and AI call is audited with the app id; a handler run records its outcome and duration on the schedule. |

**Pre-mortem.**

1. **"Everyone got pinged twice."** Two schedules, or a handler that both
   pinged the group and each member. Each notice carries the app and the
   run's slot, and the emitter collapses the same app's notice to a
   recipient within a run.
2. **"The bot spammed the group."** A bug in a handler looping notify. The
   per-app rate limit and per-run notice cap stop it; the schedule pauses
   after repeated failures.
3. **"It used the wrong person's AI budget."** A page's call is always the
   caller's; a handler's is the owner's, and the audit row says whose.

## 9. Milestones

- **A1:** accounts store, `me().account`, the skill update. This is
  enough for "no need to type the login".
- **A2:** `app.ai` from pages.
- **A3:** `app.notify` to members, with the `app_notice` kind and mutes.
- **A4:** app handlers and the `app` schedule recipient; `notify` and `ai`
  from handlers.
- **A5:** group delivery in the notifier contract (`groups`), and the
  organization's group-chat notifier package adding it.
