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
- Resumed after a long pause: is stale state still valid?

*Test:* run it long; restart each process in the middle; upgrade across
it; edit or delete its inputs afterwards.
*Seen:* a fixed 5-minute turn cap killing real work; a 4-hour token under
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

*Test:* inject each failure; retry after a partial success; read the user
facing text of every error path.
*Seen:* an attachment dropped silently; a session that failed on every
message after one overflow; an automatic retry that could repeat work; a
hint blaming environment variables for an unrelated error.

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

*Test:* hostile inputs; repeated triggers; look-alike names.
*Seen:* display names that could impersonate an owner; requests that could
spam an owner if not de-duplicated.

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

### 8. Defaults, unknowns and silent fallbacks

- What happens when a value is missing, empty, unknown, or still loading?
  Is "not loaded yet" treated as "does not exist"?
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

*Test:* a conformance check against the real component; a probe in a
throwaway environment; read the spec of the version actually negotiated.
*Seen:* a newer protocol version requiring fields we did not send (tools
silently dropped); a CLI reporting errors as normal text; a gateway
reporting zero token usage, so the client never compacted.

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

*Test:* the upgrade itself, with existing state, before and after; a mixed
deployment.
*Seen:* a destructive migration that broke a mixed deploy; an upgrade
that lost in-use workspace bindings; updates applied mid-task.

### 12. Observability

- When it fails in production, can someone see why within minutes —
  without attaching a debugger?
- Are failures logged at a level that is kept? Is the version of every
  running piece identifiable?
- Is there a check that exercises it end to end, the way a user does?

*Test:* break it on purpose and try to diagnose it from logs and status
alone.
*Seen:* a failure visible only in a debug log; a component without any
version identity; problems found only by a user.

## The process

### Before building — in the PRD

Every PRD (`docs/prd/`) for a change that alters behaviour has a
**Design review** section:

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
