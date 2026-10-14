# LESSONS.md — the mistake book

bnbloc is built mostly by agents. This file is what they learned the hard
way, written by agents after reviewing their own work. Read it before you
start; add to it when you finish. AGENTS.md says where each kind of work is
documented; this file says what went wrong (and right) last time.

## How an entry looks

```
### <the rule, one sentence, imperative>

<why: the incident in two or three sentences — what was done, what broke,
what it cost.>

Evidence: <commits, docs/bugs/ records, PRDs>.
Seen 2× (2026-10-13, 2026-10-14).
```

- **Mistake** entries say what not to do again; **Done right** entries
  (under their own heading) say what to keep doing.
- **Seen** counts the distinct days the lesson was learned again. Found it
  again? Add the date and the evidence; do not add a second entry.
- **Promotion.** A lesson seen on **three distinct days** stops being a
  lesson and becomes a rule: move it to its owner and leave one line under
  "Promoted" saying where it went.
  - A design question goes to that lens of `docs/design-review.md` (its
    *Test* and *Seen*).
  - A workflow, verification or release rule goes to AGENTS.md.
  - A subsystem trap goes to AGENTS.md "Hard-won traps".
  - A lesson a test or CI check now enforces goes under "Promoted" with the
    name of the check.
- **Neutral names only.** This file is public. A lesson about an internal
  system goes to the internal addendum's own LESSONS.md, in the same format.

## When it is written (the review)

After each working day with commits, and after every release, the agent
runs `scripts/lessons-review.sh` (default: the last day; `--since
YYYY-MM-DD`). It prints the day's commits, the reworks (fixes that touch
what a commit of the last three days changed), the bug records written and
the lessons due for promotion. The agent then looks for three things:

1. What was fixed and then fixed again (rework).
2. What should not have landed as it did (scope, a skipped review, a leak).
3. What was verified too loosely (a claim with no check behind it, a check
   on the wrong input or the wrong surface).

Each finding becomes an entry, or a new date on an existing one. A bug
record's "Why it was not caught" is the same finding: write both, and link
them.

## Mistakes

### Prove an upgrade keeps what is in use: the same session, its files, its conversation and its tools, on a live session before and after

A core update lost the workspace bindings of sessions in use: agents came back
with empty workspaces. A later update brought sessions back without the MCP
tools their profile had given them, and every later turn ran without room
tools until the member gave up. Both updates passed their unit tests. A
session that comes back missing part of what it was created with is worse
than one that does not come back at all. "Restored" has to mean restored
whole, or not restored.

Evidence: docs/bugs/2026-10-13-agent-loses-its-workspace.md;
docs/bugs/2026-10-14-work-save-misses-new-files.md (cause 4). Now checked by
`bnbloc smoke --scenario long-task --between "<the update>"`: memory,
workspace file and room tools.
Seen 2× (2026-10-13, 2026-10-14).

### Reproduce the reporter's exact case, with the real input on the real surface, before you call it fixed or not reproducible

A coding agent was killed a few seconds into every room turn. Every manual
reproduction passed, because each one used a short prompt. The real room
prompt is about 3 KB, and endpoint security on the machine refuses to start
an interpreter whose argv carries an argument that long. Only the exact
argv, environment and prompt reproduced it.

Evidence: the desktop core's fix of 2026-10-14 (the prompt goes on stdin);
`bnbloc smoke --agent <core>/<provider>` reproduced it where manual runs
did not.
Seen 1× (2026-10-14).

### What an agent cannot see in this turn, it cannot act on: let a tool take what the agent has

An agent wrote two decks and was asked to save them as gallery works. The
save tool wanted a deliverable id, which an agent learns from the room
records, and those are written at turn start. The files had ids, but the
agent could not see them until the next turn, so it told the member to ask
again. The tool now takes the file's path and publishes it first if it has
to.

Evidence: docs/bugs/2026-10-14-work-save-misses-new-files.md (causes 1–2).
Seen 1× (2026-10-14).

### Put no fixed limits on a member's work: bound it by activity and by the product's own caps

A two-hour turn deadline ended real multi-hour tasks. Attachments the
product accepted were then refused one layer down: by a core's request-body
cap, and by a mirror cap smaller than the attachment cap. Each limit was
"reasonable" on its own, and each one broke real work the product had
promised to handle.

Evidence: docs/prd/2026-10-13-live-turns.md;
docs/bugs/2026-10-13-attachments-dropped-silently.md; commit 10b147ce (the
mirror cap follows the attachment cap).
Seen 2× (2026-10-13, 2026-10-14).

### Write public text with neutral names from the first draft, not at push time

Internal product names went into a public PRD and into test fixtures. The
pre-push leak scanner caught both, and both were fixed by rewriting
unpushed history: a fixup and autosquash for the PRD, a filter-branch over
the fixtures. Deciding which repository the content belongs to happens
before the first line, not when the hook fires.

Evidence: AGENTS.md "Public repo: neutral names only"; the pre-push hook's
refusals.
Seen 2× (2026-10-13, 2026-10-14).

### Delete only what you created

Cleaning up after a test, an agent deleted every directory under a shared
temporary workspace root, not just its own. The member's real workspace
survived only because it lived elsewhere at the time.

Evidence: session notes of 2026-10-13 (no commit: a local action).
Seen 1× (2026-10-13).

### Run CI stages one at a time in one working tree

The Go and web stages ran at the same time and raced on the embedded web
build. That was a false failure, and it cost a rerun and a wrong first
diagnosis.

Evidence: scripts/ci.sh (stages share `services/gateway/webui/static`).
Seen 1× (2026-10-13).

## Done right

### Replay a member's long task across a release, not just a smoke reply

`bnbloc smoke --scenario long-task --between "<publish>"` runs a real task
across an update: attachment, workspace write, the update, then memory,
files and room tools. It is how the 2026-10-14 core release showed that a
restart-safe session keeps its conversation and its tools. A plain smoke
reply would have passed even when those were lost.

Evidence: cmd/bnbloc/smoke_scenario.go; the 0.4.10 core release.
Seen 1× (2026-10-14).

### Answer every design-review lens before building, against the code

The multi-member design review found problems nobody had asked about before
any code existed. Examples: whose workspace a borrowed agent writes to,
whose context leaks across channels, what a deleted message must erase from
an agent's memory.

Evidence: docs/prd/2026-10-14-multi-member-rooms.md §2b; docs/design-review.md.
Seen 1× (2026-10-14).

## Promoted

- **Never skip, drop or truncate silently: say what was left out, to the
  person and to the agent.** Seen 3× (2026-10-08 a download truncated at a
  proxy deadline; 2026-10-13 attachments dropped; 2026-10-14 files under
  `out/` never published). Promoted 2026-10-14 → `docs/design-review.md`,
  lens 8 (Test and Seen).
