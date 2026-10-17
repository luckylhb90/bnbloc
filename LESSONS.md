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

### A tool's result text is an instruction: change it with the tool

The save tool learned to take a work's request and description. Its result
note still told the agent to edit those "from its card", so the agent
relayed that to the member instead of using the new arguments. It also
trusted its own earlier refusal from before the update. Every hint a tool
returns is read as how to proceed: review them with the tool's parameters,
in the same change.

Evidence: docs/bugs/2026-10-14-work-save-misses-new-files.md (follow-up);
commit 349926c7.
Seen 1× (2026-10-14).

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

Evidence: session notes of 2026-10-13 (no commit: a local action). On
2026-10-14 a cleanup glob over release build clones (`/tmp/<prefix>-*`)
also removed the clone a build was still running in; it had to be redone.
Seen 2× (2026-10-13, 2026-10-14).

### Run CI stages one at a time in one working tree

The Go and web stages ran at the same time and raced on the embedded web
build. That was a false failure, and it cost a rerun and a wrong first
diagnosis.

Evidence: scripts/ci.sh (stages share `services/gateway/webui/static`).
Seen 1× (2026-10-13).

### Make a long command report its real exit code

A desktop release build ran in the background as `build > log; rc=$?; echo
…`, and a trailing `; echo` made the task report success. The build had
died with "No space left on device". The version bump was already pushed
when the publish step failed safely for want of a signature. Run the
command last, or `exit $rc`; check the artifact itself (its version, its
signature) before publishing.

Evidence: desktop 0.1.46 (commit 7dc36210), rebuilt with a real exit code.
Seen 1× (2026-10-14).

### Reach for the owner function before writing a check

A new file-name check was written by hand twice in one day. Both times
the architecture test that keeps `core.CleanRelPath` the one owner of
relative paths refused it (commits 4a3433b7 and 5f080187). Before writing
a check, search for the function that owns it; the archtest is right.

A day later the same checks refused three more hand-written copies: the
web boundary check (polling and a clipboard write written inline instead of
through the shared hooks), the store's single-source rule (an inline expiry
comparison) and the status-write check (a run-time status written without
the one function that logs it). Each time the owner already existed.

Evidence: commits 4a3433b7, 5f080187, fc80a90c; internal/archtest;
web/scripts/check-boundaries.test.mjs.
Seen 2× (2026-10-14, 2026-10-15).

### Make every new assertion fail once before you trust it

A ranking test asserted "an organization's pack before a built-in one". It
passed with the order reversed: built-in packs were tagged "library" among
the composer's choices, so the rule it named never applied. Only breaking
the code on purpose (a red check) showed it. The same day an overlap test
caught a real gap the check had (a window step that skipped shared runs).

Again the next day: a red check of a new capability gate printed "ok" and
the gate was nearly trusted. The run had been served from the test cache,
and one earlier attempt had not even compiled. Run a red check with
`-count=1` and read that the test failed for the reason you broke.

Evidence: commit 3fade890 (promptChoices tags built-in packs);
lib/editScope.ts (commit bb55bfc9); commit fc80a90c (group bind gate).
Seen 2× (2026-10-14, 2026-10-15).

### Check what you generate, not what you wrote

The PDF guest script is a template literal. An edit written through a
shell heredoc lost one level of escaping, so `"\n"` became a real line
break inside a JS string, and the guest stopped parsing. That broke PDF
Compare, and would have broken the PDF viewer. Type checks and unit tests
passed; only a browser run showed it. A test now parses the generated
script.

Evidence: commit 897f3c35 (pdfFrame.test.ts "the guest script parses").
Seen 1× (2026-10-14).

### Prove the harness ran what you meant before you read its result

Three browser checks reported on the wrong thing:
- a "Show" locator matched "Show 2 pages unchanged" and stayed on Compare;
- a stored session had expired, so the page was the sign-in screen;
- a build ran with `--dir web` from inside `web/` and failed, and the run
  went on serving the previous build.

Each looked like a product failure. Check the harness itself: exact
locators, a fresh session, the build's real exit status, a screenshot of
the page before the first assertion.

Evidence: session of 2026-10-14 (the select-edit-review checks).
Seen 1× (2026-10-14).

### A new value in a shared vocabulary must be harmless to an older reader

An older web host refused a previewer whose capabilities named one it did
not know, so publishing a previewer before the host was deployed would
have hidden it from every member. Installed packages re-parse strictly at
boot, so a manifest field cannot be removed in the same release that stops
using it. Readers now ignore unknown capabilities; removals go in two
deploys.

Evidence: commit 4cd35e6c (unknown capabilities ignored);
docs/prd/2026-10-14-file-prompts-and-compare.md rule 3 (F4).
Seen 1× (2026-10-14).

### Show the result of an action on the control that did it

"Save to <file>" wrote the file and said so only in a four-second toast in
the corner. The member saw nothing and reported that saving did nothing.
A write's outcome now stays on the chip ("Saved v2 to deck.pptx") until
another version is shown.

Evidence: commit 2bef7603.
Seen 1× (2026-10-14).

### Put a new control where its empty state still reaches it

The owner's data line of an interactive app showed only once the app held a
document. "Automate…" and "Group chats" were added to that line, so a new
app, the one that most needs a reminder to fill it in, had neither. Unit
tests and the design review passed; running the UI against real data on QA
showed it. Ask of every new control: which states of its container hide it?

Evidence: commit 912edfc1.
Seen 1× (2026-10-15).

### After a shared type changes, run every consumer's checks, not the edited package's

Adding fields to a shared wire type broke a test of another package (an
attachment type that a test still counted as refused) and a web test's type
check; both showed only at the end, once the whole suite and
`typecheck:tests` ran. Run all Go packages and the web test type check
before the first commit that changes a shared type.

Evidence: commits 14ea7234, 587da432, 5e2d1b2a.
Seen 1× (2026-10-15).

### Two senders of one notice share one builder

A notice an app sends and the same notice an AI task sends were written as
two functions. The first named its app and opened it; the second carried
only the room, and a member saw the difference in the chat before any test
did. The fix added the missing field to the second copy. The copies still
duplicate the link, the title prefix and the dedup key: build a notice in
one place, and give both senders the same inputs.

A notice schedule's run reused the sending function but not its inputs: the
schedule path skipped nobody-but-the-sender differently (it excluded the
schedule's creator as a "sender" who had not just acted) and, with no public
origin configured, built a relative link, because the app path took the
origin from a request the run does not have. Same builder, same inputs.

Evidence: commits 5e2d1b2a, e2df2b3a, 669ed853.
Seen 2× (2026-10-15, 2026-10-16).

### Say what the error showed, not the cause you guessed

An install failed with "no such host" for the component's address. The
reply told the owner the component's service was probably down. It had not
been checked: the address was simply wrong. Report what the error said and
what you checked; name a cause only after checking it.

Evidence: session of 2026-10-15 (component upgrade).
Seen 1× (2026-10-15).

### Take a package's deployment values from the installed record, not its source defaults

A component package was materialized from its source manifest, which holds
the placeholder address of the compose layout. The installed record held
the deployment's real address and network permission. The install probe
refused the wrong address, which is the only reason it did not replace the
right one. Read the installed record first; the endpoint and permissions
are deployment data the publisher supplies.

Evidence: session of 2026-10-15 (component upgrade).
Seen 1× (2026-10-15).

### Establish the cause on the live system before you ship a fix; a prompt sentence added on a guess is a patch

Room guidance and tools reached kept sessions late or never. Four bridge
releases in an afternoon each added a mechanism (a one-time note, a tool
digest, an epoch, a re-brief) for a symptom whose cause nobody had read off
the core: it fixes its instruction prefix at a conversation's first turn and
ignores a changed one on resume. Once that was read, the fix was the
instructions file the core re-reads at every start, and every briefing
mechanism was deleted. Separately, an AI schedule run was given a sentence
saying when it runs, on the guess that it misread its task; nothing had shown
the sentence changed what it did, and it was removed twenty-five minutes
later. Ask first which component owns the behavior and what it does on the
failing input, read it from its code or a run, write it down, and change that
component.

Evidence: bridge commits aca462fc, 034e273d, f9347809, 0fc730a4 and their
removal 2c7f511c; docs/architecture/core-providers.md (c3381b26); 031265d7,
7497cecc.
Seen 1× (2026-10-16).

### Report what happened, not what was attempted

A notice schedule's run answered "notified N people" and the dialog toasted
"Sent now" when the emitter had queued nothing: the creator was excluded as
the sender and the other member had app notices off. A failed run toasted
success too, because a run's failure is a reply, not a rejected call. The
member saw "sent" for a notice nobody received. An action's confirmation
comes from its result (queued or not, ok or error), never from the call
having returned.

Evidence: commit 669ed853.
Seen 1× (2026-10-16).

### Write what a repository or a release keeps in the project's language, not the conversation's

Two desktop releases went out with release notes in Chinese because the
conversation was in Chinese. The product owner had to point it out; the rule
is now in AGENTS.md ("Language: English unless told otherwise"). Before
publishing, committing or writing a doc, check the text is the project's
language.

Evidence: commit 4d9625b1 (the rule); releases 0.1.63 and 0.1.64.
Seen 1× (2026-10-16).

### Before you read a failure as yours, look for edits in the tree that you did not make

A full Go run failed `TestPromptBudgets` once; the package passed alone and
on a rerun. `git status` then showed uncommitted edits to the bridge's room
tools, brief and prompt test that were not mine: another agent shares the
checkout. The failure was plausibly theirs mid-edit, though that was not
proven. Look at the tree first, then decide whose failure it is.

Evidence: session of 2026-10-17 (no commit: a local observation).
Seen 1× (2026-10-17).

### Ask "component or platform?" before the first line, and say which in the PRD

A room-memory feature went in as platform code: note types, a ranking rule, the
wording of `.room/` files, a store, routes and a panel across four services, with
only the AI prompt as content. The owner had wanted an installable component and
said so again after stage 1 had shipped in two desktop releases. The same project
had just shown the right shape (local skills as a component behind two small
contracts). The redesign is a generic room-extension framework plus the memory
component, and the shipped code is removed. Decide the shape first; the platform
keeps only what a component cannot do.

Evidence: docs/prd/2026-10-17-room-knowledge-component.md (supersedes
2026-10-16-room-memory-and-evolution.md); commits fe416c71, cf2ae7df.. (stage 1);
design-review lens 14. The same day the media room tools went the same way:
built in, then moved to an installable MCP component (de4f5bae).
Seen 1× (2026-10-17).

### Before a component asks a person for a setting, check what the platform already holds

The media MCP component first shipped asking each member to connect it to
their own gateway with an API token in Settings → Tools. An hour later it was
rebuilt so the bridge injects its gateway address, a session token and the
drive's room and job: nothing for anyone to configure, and the call acts as
the requester under the job grant. Every setting is a step a member can get
wrong or skip; ask only for what the platform cannot know.

Evidence: commits de4f5bae, e8ee14e3 (and private 499b177).
Seen 1× (2026-10-17).

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

### Check the UI of a change against real data before it ships

A local web build pointed at the QA gateway, with the new pieces routed
in (a content pack, previewer bundles), showed problems no unit test
could: a file panel offering the room's generic starters, and a selection
action cut off at the panel's edge. Both were fixed before anyone saw them.

Evidence: commits aedd52f2, a2bc64af, 912edfc1.
Seen 2× (2026-10-14, 2026-10-15): the owner line shown only with data was
found the same way.

### Mark an assumption unverified in the doc, then confirm it before the live test

The group chat feature rested on two guesses about a messenger's API (the
shape of a group destination, and whether a bot receives group messages).
The docs said so ("unverified against the API"), and before the live test
the shape was checked against the vendor SDK a sibling project already
used. The live test then only had to prove the rest.

Evidence: docs/notifications.md (Groups); services/gateway/notifier_group_binds.go.
Seen 1× (2026-10-15).

### Take the model out of a step that has no decision in it

An AI task whose text said "Thursday 9:00, send this title and this body to
this group" was misread at run time ("it is not Thursday 9:00, so I did not
send"). Rewording it would have been another patch. The fix removed the model
from the step: a notice schedule carries its destination, title and body as
data and sends them with no model, and an optional compose mode lets the
model write only the body. Ask of every AI step what the model decides there;
if nothing, it is a record.

Evidence: commit e8589cdc; docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §4a.
Seen 1× (2026-10-16).

### Repeat a new test under -race before it lands

A new gateway test passed alone and in the first full run, and failed once in
four repetitions under `-race`: it assumed the outbox lists notices in send
order, and it lists by id. The fix looked a title up instead of indexing. A
test that leans on an order its code never promised shows only when repeated.

Evidence: commit e8589cdc (the test as landed).
Seen 1× (2026-10-16).

## Promoted

- **Never skip, drop or truncate silently: say what was left out, to the
  person and to the agent.** Seen 3× (2026-10-08 a download truncated at a
  proxy deadline; 2026-10-13 attachments dropped; 2026-10-14 files under
  `out/` never published). Promoted 2026-10-14 → `docs/design-review.md`,
  lens 8 (Test and Seen).
- **Put no fixed limits on a member's work: bound it by activity and by the
  product's own caps.** Seen 3× (2026-10-13 a turn deadline ended real
  tasks; 2026-10-14 a mirror cap below the attachment cap; 2026-10-16 an 8 MiB
  cap refused a skill folder the member needed to sync). Promoted 2026-10-17
  → `docs/design-review.md`, lens 3 (a cap below a promise made one layer up).
- **Prove an upgrade keeps what is in use: the same session, its files, its
  conversation and its tools, on a live session before and after.** Seen 3×
  (2026-10-13 workspace bindings lost; 2026-10-14 room tools lost on
  restore; 2026-10-16 new guidance and tools never reached kept sessions,
  proven only on new ones). Promoted 2026-10-17 → AGENTS.md "Releases" (a
  change to what an agent is told or given is proven on a resumed session).
