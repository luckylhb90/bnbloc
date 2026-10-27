# Content improves from use: a coach made of content (2026-10-27)

Status: **deferred 2026-10-27** by the owner. Not built, in any form.

Why deferred (the owner's question "is it worth it", answered the same day):
the day's own case was mostly not a content fault (a platform bug and a
missing connector); an agent can already improve a component when asked
(the `components` tool: get, try, save on confirmation); at this stage there
are few packs and their authors are at hand; and reflection without an
explicit correction is noisy and costs a turn per room. Reconsider when
two to four weeks of noted manual corrections show the same content fault
recurring across rooms (how often, and caused by content rather than
environment, platform or model). The design below is kept for that day.

## 1. Why

Experts, skill packs and prompts are written once and then meet real runs.
When one is wrong, the same correction is made by hand in every room. On
2026-10-27 a release skill told its agent to ask the person for a token and
run an interactive sign-in; the person corrected it in chat; nothing
reached the skill, so the next room would repeat it.

Two traps the design must avoid, both seen that same day:

- **The wrong culprit.** That run's real causes were a platform bug
  (sandboxed commands lacked the connectors' placeholders,
  docs/bugs/2026-10-27-sandboxed-commands-miss-connector-env.md) and a
  connector nobody had added. Rewriting the skill to work around them would
  have baked in a workaround that became wrong the day the bug was fixed.
- **Platform code for a behaviour.** `2026-10-16-room-memory-and-evolution.md`
  was withdrawn because it compiled notes, routes and a panel into the
  platform. Its domain rule stands: a change to a component is never made in
  place by a run; it is a proposal a person accepts.

## 2. What exists already (checked 2026-10-27)

The platform already lets an agent improve content for the person who
asked, with the consent rules this needs:

- The `components` room tool (`services/bridge/roomtools/components.go`):
  `list` the person's Library; `get` a component's manifest and files to
  improve it (another author's files come fenced as their data); `try` a
  draft in the workspace by dispatching a sample task to another agent
  wearing it (content drafts, never shown in the Library); `save` the next
  version (`id`) **only after the person confirmed**; `publish` only
  withdraws: taking a component to the organization is the person's own
  click (`errJobWidens`), and an organization copy goes through the
  organization's publish review.
- The `room_setup` tool: the room's own instructions and description, its
  quick prompts, its schedules.
- The conversation itself: the agent reads the room's history, its own runs
  there and the person's corrections.
- Assist contracts `review-skill` and `draft-skill-pack`.

## 3. What it is

**Coach**, a content package (an expert, its skill pack, a prompt pack, and
an optional room package with a schedule), published in a content
repository and installed through the Library like any content. It needs no
component of its own and no platform change.

How a member uses it:

1. In the room where something went wrong: "Improve what you used from
   this" (a quick prompt), or "this was wrong because …". The coach is the
   room's agent wearing the Coach expert, or a dispatch to it.
2. The coach reads the conversation and, with `components get`, the
   expert and skill packs the run wore.
3. **Triage before any change.** Each finding is classed: *content* (the
   instructions are wrong or missing), *environment* (a connector, a
   setting, an installed tool missing), *platform* (a bug), *tool* (an MCP
   tool or core misbehaving), *model* (one core or model only), or
   *one-off*. Only *content* gets a change; the others become a short
   report for whom it belongs to (the room's owner, an org admin, a bug
   record, the tool's maintainer), which the member sends themselves.
4. **The narrowest home**: a lesson about this room (its repo, its
   environment names, its people) goes into the room's instructions
   (`room_setup`); a person's preference into their own content; only a
   general lesson into the shared pack. Names, hosts, paths, people and
   anything that looks like a secret seen only in the evidence never go into
   a shared pack.
5. **A proposal, never a change**: the coach drafts the revision in
   `drafts/<name>/` (`components get` copies), shows the person a diff with,
   per change, its reason, the messages it rests on, and the runs that went
   well with the same pack (counter-evidence). **Risky changes are
   flagged**: a new host or URL, a command that downloads or runs code,
   anything about credentials, a new requirement or permission.
6. **Try before save**: `components try` runs the request that went wrong
   (or a realistic one) with the draft, on another agent, and shows the
   result beside the original.
7. **Save only on the person's confirmation**, as the next version
   (`components save` with the id). Someone else's pack: the person cannot
   save it; the coach writes the proposal as a message for its author, which
   the person reads, trims and sends (nothing from this room reaches the
   author without the person's eyes on it).
8. **Revert** is the same path: a new version from an older one's files.

Prompt packs, room quick prompts and room setups are improved the same way
(they are content the tools reach). MCP tools, cores and plugins
(executable, signed) are never changed: the coach writes a usage note into
the content that wears them and a maintainer report the person sends.
Connectors keep their own loop in the connections component (Suggest
rules, AI checks); the coach points there.

**On a schedule** (optional): the room package's scheduled task runs the
coach over that room's recent conversation; it acts as the schedule's
creator (room schedules do), so it reads only what they read, and it ends
with proposals in the room, never a save.

## 4. Placement

| Part | Where | Why |
|---|---|---|
| Coach: expert, skill pack (triage, homes, flags, diff, try, save), prompt pack, scheduled room package | content (a content repository, the Library) | guidance and a workflow: content first (AGENTS.md core rule) |
| Reading the conversation, getting, trying and saving components, setting room instructions | platform, **as built** (room tools) | nothing new |
| Evidence beyond the room (runs in other rooms that wore a pack), the packs a run wore, effect over time, an explicit feedback signal | not in stage A; section 7 | only when stage A shows the value and its limit |

Name test: the platform gains no noun. Fork: an organization edits or
replaces the Coach content. Replace: any other content package does it its
own way with the same tools.

## 5. Design review (stage A)

| Lens | Answer |
|---|---|
| 1 Actors | The person asking (decides, saves their own components, sends anything addressed to others); the coach (reads what the person's agent may read, proposes, never saves unconfirmed); another component's author (receives only what the person sends); the room's owner (room instruction changes go through `room_setup`'s own rules). |
| 2 Time | A proposal lives in the conversation; nothing is kept elsewhere. A saved version is permanent history; revert is a new version. |
| 3 Scale and cost | One coach turn per request, on the person's agent and model; a schedule is one turn per run of it. No platform cost. |
| 4 Failure | Triage may say "nothing to change in content", a normal answer. A failed try is shown; a save is not offered after a failed try without saying so. |
| 5 Boundaries and privacy | Evidence is the room's conversation the agent already reads; nothing from it reaches another person except what the person sends. Shared packs never receive room-only names. |
| 6 Abuse | Conversation text is untrusted: the coach treats it as data, flags risky changes, and the person confirms every save; an agent can never publish to the organization (`errJobWidens`); organization copies pass the organization's review. A member steering the coach can only propose to themselves. |
| 7 One owner | Saving, trying and publishing stay the room tools' (one path for every agent). |
| 8 Defaults | No automatic save, no automatic schedule (the room package is opt-in). |
| 9 External contracts | The room tools' actions as documented in `room_help components`. |
| 10 Load on agents | None for other agents; the coach's guidance is its own skill pack, loaded only when worn. |
| 11 Release | Content only: published and rolled back like any content; no deploy. |
| 12 Observability | Saves and publishes are audited as today; the coach's proposals are messages in the room. |
| 13 AI capability | The capability is the AI's; people decide. |
| 14 Component or platform | Content (section 4). |

Pre-mortem: *the coach rewrites a skill around a bug* — triage classes it
*platform* and writes a bug report instead; *a room's repo name lands in an
org-wide pack* — the home rule and the name check; *a poisoned message
teaches the skill to leak tokens* — flagged as a credential change, and only
the person saves; *nobody uses it* — it is content: cheap to change or drop.

## 6. Decisions (answered 2026-10-27, carried over)

1. Evidence: only what the person asking may read (stage A: the room's
   conversation).
2. When: when a person asks, or a schedule they install; never automatic.
3. A save is an ordinary version: latest followers get it, pinned uses stay
   pinned, organization copies go through review.
4. Someone else's component: a proposal goes to its author, sent by the
   person.
5. Experts and skill packs first; prompt packs and room setups next;
   executables get notes and reports, never code changes.
6. The behaviour is not platform code (stage A has none).

## 7. Later, only if stage A shows the need (each its own decision)

Measured against what stage A cannot do:

| Need | What it would take | Kind |
|---|---|---|
| Evidence from other rooms that wore the same pack | the run keeps the content versions it wore (indexed), and a `components` action that lists the runs the asker may read that wore a component | platform: events only the platform sees; identity |
| "Did the new version help" | the same, compared before and after a version | platform data, read by the coach |
| A one-click "this was wrong" on a reply | a feedback event on a message | platform (a quick prompt already does most of it as content) |
| Outcomes of MCP tool calls | recorded by the bridge like room tool calls (names and outcomes only) | platform |

None is built until stage A is used and its limit is the reason.
