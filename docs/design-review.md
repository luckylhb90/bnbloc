# Design review: finding the problems before they ship

Most defects that reach users are not clever bugs. They are details nobody
looked at: a second person in the room, a restart halfway through, the
fiftieth attachment, a value that is still loading, an external tool that
answers differently than assumed. They are found reliably only by looking
at a design from fixed angles, every time, before building — not by
remembering to "think carefully".

This file is that set of angles (the **lenses**) and the process that
applies them. It is for every change that alters behaviour: a feature, a
fix that changes what users or agents see, a protocol or schema change, a
release. It grows from incidents: every bug record says which lens would
have caught it and adds the question that was missing.

## The lenses

For each lens: the questions to answer, the traps it catches, and how to
test it. "Not applicable" is a valid answer only with a reason.

### 1. Actors — who else touches this?

- List every actor: the requester, the owner, other members, a borrower, a
  visitor, an admin, another tenant, an agent acting for someone, a
  service, a scheduled job, a second device of the same person.
- For each: what they see, what they can do, what they must never see or
  do, and whose authority an action runs under.
- Does anything key state by "the thing" when it should key it by "the
  thing **and** who uses it" (sessions, caches, workspaces, preferences)?
- Is identity decided from data an actor controls (text, display names)?
- Does a sandboxed command get everything a host command gets for the same
  drive (environment, CA, placeholders), and does a test compare the two?

*Test:* two actors at once, with different rights, on the same object.
*Seen:* borrowers sharing the owner's session and workspace; the sender of
a turn matched by message text.

### 2. Time and lifecycle — what happens later, or halfway?

- What if it runs for seconds, hours, days? What bounds it, and is the
  bound a real limit of the domain or an arbitrary number?
- Restart, crash, upgrade, or reconnect in the middle: what survives,
  what resumes, what is lost, what is left half-done?
- Anything with a TTL (tokens, sessions, caches, leases): renewed while in
  use, or does a long task outlive it?
- Edited, deleted, revoked, renamed afterwards: do copies, caches and
  derived state follow?
- Updated afterwards: when something a session depends on gets a new
  version (a tool, a skill pack), does the session keep its memory, and
  if it must restart, is it handed what it knew and is the person told?
- Before removing a file (an old version, a cache, a copy): who else holds it?
  Another process may continue from exactly that path later (a conversation
  resumed after an idle stop), and the remover usually cannot see it. Ask the
  holder for its list; with no answer, remove nothing.
- Resumed after a long pause: is stale state still valid?
- What does each value belong to — the session, the turn, or the call —
  and is it set where that lifetime starts (a per-turn job id in a
  per-session env stays the first turn's)?
- A result that lands long after its request (a failure, a late reply):
  is it shown with the request it is about and does it name it, or as the
  newest event, beside newer work it will be read as being about?
- What is still running as this identity after a sign-out, a revocation or
  a switch (a forked session, a bridge, a stream, a sidecar), and who stops
  it? An identity's end must end everything it opened, server side (the
  fork's family) and where it runs.

*Test:* run it long; restart each process in the middle; upgrade across
it; edit or delete its inputs afterwards; let a result land after newer
requests.
*Seen:* an agent manager's cleanup of old versions that deleted the program a
room's idle conversation continues with, so its next message failed
(docs/bugs/2026-10-24-update-removed-a-rooms-agent.md);
a queued job's failure notice, posted minutes later, shown under
the newest request, which was running fine;
every release of a tool silently restarting the conversations
that used it; a signed-out member's bridge still claiming their jobs (the
logout revoked the sign-in, not the session forked from it); a fixed 5-minute turn cap killing real work; a 4-hour token under
an unbounded turn; sessions lost on every restart; a deleted message still
in an agent's memory; updates killing running tasks.

### 3. Scale, accumulation and cost

- 1 versus 50 versus 10,000 of everything: items, members, channels,
  files, versions, messages, bytes.
- Does one busy part starve the others (shared limits, first-come
  ordering)?
- What grows without bound over weeks of use (records, copies, context,
  disk, tokens, money)? Where is the cap, and what happens at it?
- Copy versus reference: is something duplicated per actor that should be
  indexed once and fetched on demand?
- Is any cap smaller than a promise made one layer up (an attachment the product
  accepts, a skill folder a member syncs, a turn the product starts)? A limit
  belongs to the layer that owns the promise; every layer below carries it or
  reports the real cause. Never bound a member's work by a fixed number: bound
  it by activity and by the product's own caps.

*Test:* seed realistic volume; the 31st item; the 201st message; the
largest allowed file; a month of use.
*Seen:* later channels starved by a busy one; every attachment copied to
every agent; deliverables past 30 unreachable; a conversation grown past
the model's context; a mirror cap below the attachment cap; an 8 MiB cap that
refused a skill folder a member needed to sync.

### 4. Failure and recovery

- Every call that crosses a process, network or third party fails
  sometimes. For each: what does the user see, in words they understand?
- Does it recover by itself, does it need a person, or does it stay broken
  ("poisoned" state that fails every later attempt)?
- Retries: are they safe? Would a retry repeat a side effect (a write, a
  message, a publish, a payment)?
- Partial success: what is true after half of it happened?
- Is any failure silent — swallowed, logged only at debug, or turned into
  a misleading message?
- Does a recovery path (a reset, a fallback, a fresh start) fire silently?
  What would show it firing on every attempt instead of once in a while?
  A self-healing path that heals every turn is a broken path nobody sees.

*Test:* inject each failure; retry after a partial success; read the user
facing text of every error path.
*Seen:* an attachment dropped silently; a session that failed on every
message after one overflow; an automatic retry that could repeat work; a
hint blaming environment variables for an unrelated error; a sandboxed
agent's session dropped and its files reset after every turn, without a
word, for days (docs/bugs/2026-10-21-sandboxed-sessions-reset-every-turn.md).

### 5. Boundaries and privacy

- What crosses a boundary: process, machine, user, room, tenant, public
  repository, logs, broadcasts, third-party APIs?
- Can an actor reach data through a side door (a shared directory, a
  listing, a progress line, a cache, an index)?
- Does the derived copy keep the original's access rules (deleted, private,
  revoked)?
- Is anything secret ever echoed: into logs, URLs, command lines, progress,
  errors, prompts?

*Test:* as each actor, try to reach what they must not; grep outputs for
secrets.
*Seen:* command lines with tokens broadcast as progress; deleted content
kept in records; internal names nearly pushed to a public repository.

### 6. Abuse and untrusted input

- Who could misuse it: spam, impersonation, resource exhaustion, prompt
  injection through text an agent reads?
- Is untrusted text kept apart from instructions (fenced, never trusted for
  identity or permissions)?
- Can one actor trigger unbounded work or notifications for another?
- Does the server refuse every shape its own client never sends (an empty
  id, a blank field), or only what the client happens to check? Any API
  caller can send it.

*Test:* hostile inputs; repeated triggers; look-alike names; each required
field empty and blank, sent straight to the API.
*Seen:* display names that could impersonate an owner; requests that could
spam an owner if not de-duplicated; a chat to an agent with an empty owner
accepted and queued as a job no bridge would ever claim.

### 7. One rule, one owner

- Is the rule implemented in exactly one place? Search for a second copy
  (client and server, two services, a constant duplicated in a test).
- Do two components each impose their own version of the same limit or
  policy?
- Is the owner the component that actually has the knowledge?

*Test:* an architecture test or a single source the others are generated
from.
*Seen:* the file picker and the upload check each with their own list of
types; two components each deciding how long a turn may run.

### 7b. Identity — which one, exactly?

Asked of every change that acts on "the same X again": ask again, retry,
continue, resume, reply, undo, confirm, restore, deliver, notify.

- **What is X's key?** Name it (an agent: owner+core+provider; a session:
  room+channel+agent; a job, a version, a message: its id). If you cannot
  name it, the design is missing a field.
- **Does the action carry the key of the thing it was about, or does it search
  for the thing again** by a role (mine, platform, online, owner), a position
  (first, last, latest, default, current), a name, a label or a text? A search
  finds *a* match; the action needs *the* one.
- **Can two candidates exist that differ only in what the search ignores?**
  Two agents of one provider under two cores, two channels, two sessions of
  one workspace, a member and a borrower of one agent. Today there is one;
  what makes there be two?
- **What happens when the thing is gone?** The only right answer is "say so
  and do nothing". A fallback to "some other one" acts on the wrong thing.
- **Where does a search remain, and who confirms it?** A default may fill an
  empty choice that a person then confirms or changes. It is never the target
  of an action.

*Test:* a world with two candidates that differ only in the part the search
ignores; do the first action, then the "again" action, and assert it reached
**the same one**; then remove it and assert nothing was sent. A test world
with one candidate cannot fail this way.
*Seen:* "ask again" sent to the first of a member's own agents (another
core's, not signed in) instead of the one the request went to (2026-10-26);
a saved agent restored by (core, provider) onto another member's agent,
starting a borrow (web H9, 2026-10-12); a turn matched to its sender by
message text; a conversation adopted by "the latest of its workspace" before
it was handed over by name (a core, 2026-10-22).

### 8. Defaults, unknowns and silent fallbacks

- What happens when a value is missing, empty, unknown, or still loading?
  Is "not loaded yet" treated as "does not exist"? Is "not visible to me"
  (a 403, a 404, missing from a list) treated as "deleted"? Anything that
  destroys data acts on positive evidence of the deletion, never on an
  absence.
- Does an automatic correction get saved over what the user chose?
- When the system falls back (default model, default setting, degraded
  mode), does the user know?

- Every skip, drop, cap or truncation: does the person (and the agent)
  learn what was left out, and why? A rule may only skip what it can name
  as noise.

*Test:* every input absent; slow loading; a list that is momentarily
empty; one input over each cap and each skip rule.
*Seen:* a saved model cleared because the list had not loaded yet, then
saved as empty; a missing choice silently running a different model; a
download cut at a proxy deadline; attachments over a core's body cap
dropped without a word; an agent's decks under `out/` never published
(LESSONS.md, promoted 2026-10-14).

### 9. External contracts

- Every assumption about something we don't own (a CLI, a protocol, an
  API version, a gateway, a model) — verified against the real thing, or
  assumed?
- Which version of the contract is negotiated, and what does that version
  require?
- What does it do on error — does it signal failure the way we expect?
- Does each test fake check what the real side checks (auth, conflicts,
  uniqueness)? A fake that accepts what production refuses proves nothing.
- When the external side dies (a browser tab crashes, a daemon exits), does
  the person see that, in words, or a frozen picture? Read its own crash
  reports before guessing at causes.
- Does a reader rely on an order (newest first, ascending) the source
  states, or one a comment remembers? Our own services count.
- Does a reader of pages or files others make (a site, a document) work
  on one built the common way for its kind (a component library's admin
  page, an exported spreadsheet), not only on the reference's fixture?
- Does our side rely on *when* the other side did something (a file in
  place when a call returns, an event before a reply)? Is that timing
  written in the contract's owner doc and checked against the real
  implementation, or does it live in a comment of the caller? A change on
  the other side (another repository) breaks it without touching our code.

*Test:* a conformance check against the real component; a probe in a
throwaway environment; read the spec of the version actually negotiated;
more rows than the reader keeps, so a wrong order or cut shows.
*Seen:* a newer protocol version requiring fields we did not send (tools
silently dropped); a CLI reporting errors as normal text; a gateway
reporting zero token usage, so the client never compacted; an assist
reading a history page as newest first, so it kept the oldest messages;
a browser snapshot that saw a framework's `li` menu items only as text;
a core that wrote its instructions file after answering a session's
create, which the sandbox fence read as a guest's write.

### 10. Load on users and agents

- Does this make users or agents read, remember or choose more?
- Can the platform do the work instead (correct data, isolation, ordering)
  so nobody has to be told?
- Does it add hardcoded text that will grow and rot?

*Test:* count what is added to prompts, tools, settings and screens; the
net should be flat or smaller.
*Seen:* new tools and reminder lines that platform-side data made
unnecessary.

### 11. Release and compatibility

- Old and new versions running side by side during a rollout: does each
  work with the other?
- Schema and data changes: additive first, removal later?
- Someone is using it at the moment of the upgrade: what happens to their
  work?
- Can it be rolled back, and rolled out to a few first?

- Does it run where it will really run? A component's process may run
  confined (an agent's sandbox denies writes outside the workspace). Never
  stop something (a browser, a daemon) that cannot be started again from
  the process that stops it.

*Test:* the upgrade itself, with existing state, before and after; a mixed
deployment; the change run under the confinement it meets in use.
*Seen:* a destructive migration that broke a mixed deploy; an upgrade
that lost in-use workspace bindings; updates applied mid-task; a browser
closed for an upgrade from inside the agent's sandbox, where no new one
could start.

### 12. Observability

- When it fails in production, can someone see why within minutes —
  without attaching a debugger?
- Are failures logged at a level that is kept? Is the version of every
  running piece identifiable?
- Is there a check that exercises it end to end, the way a user does?
  Does it assert the property itself, or a proxy another path can also
  satisfy (an agent that "remembers" by reading the room's history passes
  a memory question in a new session)?

*Test:* break it on purpose and try to diagnose it from logs and status
alone.
*Seen:* a failure visible only in a debug log; a component without any
version identity; problems found only by a user.

## The process

### 14. Content, component or platform

This lens is the core rule of AGENTS.md ("the platform provides only what it
must"). It is answered with a **Placement table** in the PRD, one row per part:

| Part | Content / component / platform | Why (for platform: the named reason) | What an organization would swap |
|---|---|---|---|

Order of the questions, for each part: (1) can an expert, skill pack, prompt
pack, assist or sample do it with what the platform already has? (2) else, can
a component behind a generic contract (its own repository)? (3) platform only
for consent, signing and confinement, identity, isolation, events only the
platform sees, the member-authenticated proxy, budgets and quotas, audit, the
kill switch, and the contracts with their conformance suites. The platform
ships a **mechanism** (a seam, a gate, a registry); the policy, provider,
scanner, viewer, prompt or example that plugs into it is not platform. Facts
about the outside world (which files mark an ecosystem, a credential's format,
a tool's name) are a component's or content's data, never platform code.

- Before any code: could this be an installable component behind generic
  contracts (a core, a plugin, a notifier, an extension, content), the way
  local skills became one? A feature is platform code only for what a component
  cannot do safely or at all: consent and enablement, signing and confinement,
  events only the platform sees, the member-authenticated proxy, identity,
  isolation, the budget and the audit.
- Does the platform name the feature anywhere (a type, a weight, a file's
  wording, a panel, a route)? Then it is not a contract, it is the feature.
- Would an organization that wants its own version have to fork the platform?
- **Capabilities, not use cases.** The platform ships the base capability (a
  kind, a consented data source, a tool, a route); a use case built on it (a
  review room, a report, a workflow, an app) is never written into the
  platform or its default content: no fixed categories, columns, formats,
  steps or prompts the member did not choose. Members build use cases
  themselves with AI. Where an example helps, it is a **sample**: made only
  with the public capabilities, kept in its own repository, published to the
  Library like any member's content, and copied and changed by whoever uses
  it.

*Test:* grep the platform for the feature's nouns; the only hits should be
the contract's. The PRD names the component, the contracts it uses, and what
the platform adds for it. For a use case, the PRD says which capabilities it
needs and that it ships as a sample, if at all.
*Seen:* a room-memory feature (note types, ranking, file wording, a panel)
built into the gateway, the room service, the bridge and the web app when the
owner wanted an installable component. Interactive apps first designed with
manifests, participant modes and a lifecycle the apps can own (2026-10-12).
An outcome-review room planned as built-in content with a fixed cause list,
table columns, digest and task flow, when the platform only had to provide
the room kind, the consented `runs` source and `read_records` (2026-10-20).
A deliverable-backends draft that put a credential scanner, a marker list of
language ecosystems, a project file-tree viewer and a realtime channel into the
platform, when the platform needed only the publish gate, the project bundle and
`appProxy`, and the rest were a check component, a previewer, a service
component and content (2026-10-24).

### 13. AI capability

- Every new feature gets an AI answer, the product owner's standing request
  (2026-10-17): where could the platform's AI make this easier (draft it,
  fill it in, explain it, summarize it, suggest the next step, curate it over
  time), and what is the entry point?
- Built on the one seam: an assist (`docs/architecture/ai-assist.md`) for a
  manual, in-browser step; a gateway schedule run for something on a clock;
  never a second model client, never a new tool or rule for room agents.
- Where AI is deliberately not used, say why: a step with no decision in it
  is a record, not a prompt (LESSONS.md "Take the model out of a step that has
  no decision in it"). AI proposes and a person decides whatever changes
  shared state, spends money or leaves the room.
- Cost and fallback: the member's own budget and rate, the organization's
  off switch, and the feature working without the model.

*Test:* the PRD names the AI entry point or the reason for none; with the
assist model off the feature still works and says why the AI part is gone.

### Before building — in the PRD

Every PRD (`docs/prd/`) for a change that alters behaviour first has a
**Placement table** (lens 14: every part of the change as content, component
or platform, with the reason), then a **Design review** section:

```markdown
## Design review

| Lens | Answer |
|---|---|
| Actors | … |
| Time and lifecycle | … |
| Scale and cost | … |
| Failure and recovery | … |
| Boundaries and privacy | … |
| Abuse and untrusted input | … |
| One owner | … |
| Defaults and fallbacks | … |
| External contracts | … |
| Load on users and agents | … |
| Release and compatibility | … |
| Observability | … |
| AI capability | … |

**Pre-mortem.** It shipped and a month later it failed badly. The three
most likely reasons, and what this design does about each: …
```

Small changes may answer several lenses in one line, but none is skipped
silently. Then review the answers against the code, not memory: the
most valuable findings come from checking an assumption ("this lock is per
session") and finding it false.

### While building — tests follow the lenses

For each lens that applies, at least one test that would fail if the
answer were wrong: two actors, a restart or upgrade in the middle, volume
past every limit, injected failures and retries, missing or loading
values, the real external component where it can be reached.

### Before releasing

- Run the release checks, including the end-to-end scenarios (a long task
  across an upgrade; two members with their own agents in one room).
- Verify the upgrade with existing state, before and after.
- Roll out to a few first when the change touches running work.

### After an incident

Every bug record (`docs/bugs/`) also answers:

```markdown
## Why it was not caught

- Lens: which lens would have caught it.
- Missing question or test: …
- Added to docs/design-review.md: … (or: already covered — why it was
  missed anyway)
```

and the question or example is added to the lens here in the same change.
The list is only as good as the incidents fed back into it.
