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

### Act on a thing by its key, never by a search that returns it first

"Ask again" on a suggestion sent the request to `roomAgents.find((a) => a.self &&
!a.platform)`: the first of the member's own agents. The member had chosen
nimbus's `claude-code`; the room also held another core's `claude`, which was
not signed in, so the resend failed with a spawn error and the member
reasonably blamed the feature they had just been told about (steering). The
same shape had already cost us a saved agent restored by (core, provider) onto
another member's agent (web H9), a turn matched to its sender by message text,
and a conversation adopted as "the latest of the workspace" before it was handed
over by name. The e2e room held one agent, so a wrong pick could not show.
What went wrong is not the line: it is that the action had no key. The card
knew the request and not who it went to, so the code searched.

Rule (AGENTS.md "One rule, one owner", design-review lens 7b): a thing is
referred to by its key (an agent by owner+core+provider, a session by
room+channel+agent, a job by id); code that acts on it again carries the key and
resolves by it, or says it is gone and does nothing. A search may only propose,
never act. Test with two candidates that differ only in what the search ignores.

The same day the owner found the next one in the code I had just said was
fixed: "Continue" resolved its agent with `(!live.core || !a.core || …)`, a
missing core matching any core. My check only knew the spellings I had already
met (`.find(… self …)`, `[0]`), so it did not see it. A check that lists
spellings is a fence, not a fix; what works is the gate before the code: a
choice the code invents (a target without a key, a fallback, a wildcard, a
number nobody gave) is shown to the owner as a Decision card before it is
written (AGENTS.md "Workflow: Decision gate"). I also said steering worked when
only a layer of it had been run; its end-to-end proof is now the `steer` smoke
scenario.

Evidence: web/scripts/which-one.test.mjs, services/bridge/which_one_test.go,
web/e2e/suggest-use.mjs (several agents in the room), web/src/lib/roomAgents.ts
(`agentByIdentity`), cmd/bnbloc/smoke_steer.go.
Seen 2× (2026-10-26 twice); the same class 2026-10-12, 2026-10-22.

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
commit 349926c7. On 2026-10-19 a browser tool refused a click on a control
hidden under a fixed bar with "the page changed", so the agent re-read the
page and tried the same click again
(docs/bugs/2026-10-19-covered-control-reported-as-changed-page.md).
Seen 2× (2026-10-14, 2026-10-19).

### Write public text with neutral names from the first draft, not at push time

Internal product names went into a public PRD and into test fixtures. The
pre-push leak scanner caught both, and both were fixed by rewriting
unpushed history: a fixup and autosquash for the PRD, a filter-branch over
the fixtures. Deciding which repository the content belongs to happens
before the first line, not when the hook fires.

Evidence: AGENTS.md "Public repo: neutral names only"; the pre-push hook's
refusals.
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

### Keep a sign-in's result where a restart cannot lose it, and make one at a time

A connection's sign-in was cached only in memory, in three processes (the
connections component, its provider, the member's bridge). Each redeploy
or desktop restart signed every member in again, and two requests at once
each signed in. For a system whose sign-in is an OAuth authorization on
another system, that was visible: within a day of releases a member hit
the code host's reauthorization prompt and the system stopped working.
Every new authentication path must say where its result survives a
restart, how long it is reused, and what serializes two sign-ins for one
person.

Evidence: bnbloc-connections v0.18.0 (kept authorization, waiting on a
sign-in under way), bnbloc 700d5ce4 (held sessions kept on disk).
Seen 1× (2026-10-28).

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
7497cecc. On 2026-10-19 a page that shrank in a background window got a
recovery (a one-pixel resize) in browser 0.5.5 while its cause was written
down as unknown; the cause, two scaled captures restoring each other's size,
was found that evening and fixed in 0.6.1
(docs/bugs/2026-10-19-run-window-page-shrinks.md).
Seen 2× (2026-10-16, 2026-10-19).

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

### Run `scripts/ci.sh go` before every push, not only before a release

A store change added tables to `internal/store/schema.sql` without the
`deploy/schema.sql` copy, and two files went in without gofmt. Each push
passed its own package's tests; the CI stage that compares the copies and
checks formatting failed, and a separate commit had to clean up a day later.
AGENTS.md already says to run it before pushing.

Evidence: commit 82584bdf (after fe416c71).
Seen 1× (2026-10-18).

### Give a list's test more rows than the reader keeps

An assist's room context kept "the last 10 messages" of a 30-row history
page, reversed on a comment's word that the history answers newest first.
It answers oldest first, so the assist read the page's oldest messages,
backwards. The only test stored one message, where every order and every
cut is right.

Evidence: docs/bugs/2026-10-20-assist-recent-messages-oldest.md (design
review lens 9 now asks).
Seen 1× (2026-10-20).

### Pin a consent to what was consented to: the audience of its output, the person, the words

The room sources' grant was the room's: what `read_records` read landed
in a room any member (or, once shared, anyone) read; its owner could make
anyone a room admin whose tasks then read; the task's words could change
the day after the admin approved them. Each was a review finding before
release. A grant now needs a private room, is pinned to its owner and
watches its members, its owner and its tasks.

Evidence: docs/prd/2026-10-20-rooms-as-components.md §7 ("A grant pinned
to a person"); `services/gateway/room_sources.go` `watchGrant`.
Seen 1× (2026-10-20).

### Show a late result with the thing it is about

A queued job failed two minutes after its request, once the member had sent
three newer requests. Its notice carried the request it was about, but the
feed placed it by arrival, under the newest request and its live "working"
strip, and the member read "the agent is offline" about work that was
running fine. The job was dead because the server accepted a recipient with
no owner that only an API client sends. Place a late result by what it is
about and name it; refuse on the server what the client never sends.

Evidence: docs/bugs/2026-10-21-late-failure-notice-and-ownerless-recipient.md
(design review lenses 2 and 6 now ask).
Seen 1× (2026-10-21).

### Make every self-healing path loud, and assert the property, not a proxy

The sandbox turn fence dropped a sandboxed agent's session and reset its
files after nearly every turn for five days: a core in another repository
had started writing its instructions file after answering the session's
create, and the fence read that as a guest's write. The reset posted
nothing and logged one warning nobody read; the smoke's memory check kept
passing because a new session reads the codeword back from the room's
history. A member found it: the agent forgot what it had just done, and
its installed tools were gone. A reset, fallback or fresh start says so
where people look; a check asserts the property (one session id), not
something another path also satisfies; a timing a caller relies on (files
in place when a call returns) is written in the contract's owner doc.

Evidence: docs/bugs/2026-10-21-sandboxed-sessions-reset-every-turn.md
(design review lenses 4, 9 and 12 now ask; the smoke's `session` line).
Seen 1× (2026-10-21).

### Prove a protection by asking the real agent to cross it, through every consumer of the field

A new field of a descriptor several cores read (`hostConfine.denyRead`) was built,
unit-tested and checked with the real OS sandbox, and a confined agent still read
the protected directory: one core kept its own copy of the descriptor's structure
and dropped the field. Only a real agent, asked to read a file in that directory,
showed it. Before calling a protection done, make a real agent through the real
path try what it forbids, and when the field crosses repositories, search every
consumer for its own copy of the shape.

Evidence: docs/prd/2026-10-22-host-run-mcp-tools.md; the descriptor copy in an
internal core (fixed in its repository).
Seen 1× (2026-10-22).

### Run the real-agent tests on the member's defaults, not on the options that make them pass

Tests that ran real agents through the sandbox set the permission list to "allow
everything", so they passed while the member's own configuration, which has no
list, refused every command the agent tried (the default policy refuses command
execution, and the sandbox is for running commands). A test of a feature in use
takes the configuration a member has, and a second one shows the refusal a
setting would have caused.

Evidence: docs/prd/2026-10-22-acp-sandbox.md; the ACP core's
`TestSandboxedSessionApprovesCommandsByDefault`.
Seen 1× (2026-10-22).

### Build the web whenever `wire.gen.ts` changes, even for a Go-only contract

A type added to `component/` (`AssistRequest`, `MaxAssistText`) was generated
into `wire.gen.ts` beside wire's own of the same names. `wiregen -check` and
every Go test passed, the web build did not, and the gateway image builds the
web: three pushed commits would not have deployed. A contract change that
regenerates `wire.gen.ts` runs `pnpm --dir web build` before it is pushed.

Evidence: 4c4f9c70 (the collision), 8a3bada8 (the rename).
Seen 1× (2026-10-23).
### Never write `break-words` and `[overflow-wrap:anywhere]` on one element: the first wins and a long token stops wrapping.

A pasted token (one 300-character word) made a member's own message bubble
as wide as the word, so its left half was clipped by the column and the
bubble was a wall of green. `break-words` (`overflow-wrap: break-word`)
does not shrink an element's min-content width, and it beat the
`anywhere` beside it; only `anywhere` lets a flex or fit-content box
narrow. Found by measuring the bubble (1630 px in a 784 px parent), not by
reading the classes. Own bubbles also had no "Show more" while everyone
else's text did.

Evidence: `web/e2e/long-message.mjs`, `MessageBody.onAccent`.
Seen 1× (2026-10-23).


### Run a new AI task once against the deployment's real model before calling it done

The connections review, its fixes and the connector draft passed every test
with a fake model that answered the shapes the code expected. The real model
then ran into four things no fake did: the 2000-token reply cap (a review
with findings was cut off), an object where a text field was asked (a
patch), a fence inside a string (a guide's curl example cut the reply), and
the 2000-character field bound (a draft cut into broken JSON). Each took a
gateway release. A new task runs once on the real model, its audit row read
(tokens, failure class), before it ships.

Evidence: c96f2b57, 1d225764, b5c0480f, edd948f3; the QA audit log of
connections-review-connector and connections-draft-connector.
Seen 1× (2026-10-25).
### Test a feature the way a person uses it, not the way the test finds it convenient: if the member does not pause, the test must not pause.

A suggestion that appeared while the member was typing passed every browser test and
never showed to a real user. The tests typed, then waited several seconds and looked;
people type and press Enter at once, and the answer (a model call, about 2.4 s) arrived after
the message was gone. The design, not the speed, was wrong: any result that must exist
before the send loses to the Enter key. The rewrite moved the one useful moment to after the
send (the request is whole, and someone is waiting on the agent anyway), and the test now
presses Enter at once.

Evidence: docs/prd/2026-10-25-suggest-what-to-use.md, `web/e2e/suggest-use.mjs`.
Seen 1× (2026-10-25).


### Load-test a room at the size the PRD promises, not at the size the last run used

Every load run had used rooms of 10 to 50 members. The first run at the
channels PRD's 1,024 members in one room stormed before a message was sent:
each arrival broadcast a presence snapshot of every member to every stream
(n² per arrival), the streams overflowed and reconnected, and each reconnect
was another arrival. Nothing at 50 members hinted at it. A room-wide event
whose payload grows with the room is the shape to look for.

Evidence: docs/bugs/2026-10-30-presence-snapshot-storm.md,
docs/load-testing.md ("One large room over channels").
Seen 1× (2026-10-30).

### Drill a failover under load and count every message by id before calling a service highly available

Every service "supported" replicas: the code paths existed and the unit
tests passed. The first drill (one realtime replica drained, one killed,
both brought back, 50 msgs/s) failed 34 sends, lost 49 deliveries and left
25 streams closed: a request forwarded to a dead owner, two owners for a
moment, a resume from a high-water mark that hid a lower sequence, a
30 s backoff, no drain. None of it shows without load and a tracker that
matches each message to each stream.

Evidence: docs/bugs/2026-10-30-owner-change-loses-messages.md,
`scripts/failover-check.sh`, docs/load-testing.md ("Failover");
docs/bugs/2026-10-30-replay-across-a-hole.md (the same drill, repeated after
an unrelated change, found a message lost when a replica died between its
commit and its publish: three earlier passes had been lucky).
Seen 1× (2026-10-30).

### A replay buffer that can skip an event must start again after the skip

The reorder hold delivers past a sequence that never arrived, so the
replay buffer went on with a hole in it. A stream reopening below the hole
was replayed from that buffer, which looked as if it reached back far
enough, and never told to read the difference: 2 deliveries lost in 19,976.
Anything that appends to a buffer used for resuming must keep it
contiguous, or restart it, at the point it skips.

Evidence: docs/bugs/2026-10-30-replay-across-a-hole.md,
`TestBusResumeBelowAHoleReadsTheDifference`.
Seen 1× (2026-10-30).

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

- **Make a test fake refuse what the real side refuses, and time things as
  it does.** Seen 3× (2026-10-18 a fake gateway took calls with no bearer;
  2026-10-19 a fake decision model answered a question the real API
  refuses; 2026-10-31 the in-memory bus delivered on the publisher's
  goroutine, so a member stream's forwarded scope, answered inside the
  Redis bus's one receive loop and waiting on a subscription that loop
  confirms, passed every test and stalled each replica's bus for 5 s in the
  first load run). Promoted 2026-10-31 → `docs/design-review.md` lens 9 and
  AGENTS.md "Hard-won traps" (a bus handler never waits on the bus).

- **Stop and delete only what you started, by its own handle, never by a
  pattern.** Seen 3× (2026-10-13 a cleanup removed every directory under a
  shared workspace root; 2026-10-14 a glob over build clones removed one in
  use; 2026-10-26 `pkill -f` by product name stopped the member's own sandbox
  service). Promoted 2026-10-26 → AGENTS.md "Stop and delete only what you
  started".

- **Reproduce the reporter's exact case, and test a change where it will
  really run, on what it will really meet.** Seen 3× (2026-10-14 a long
  argv killed only with the real room prompt; 2026-10-19 a browser restart
  tested only from an unconfined process closed the member's browser from
  the agent's sandbox; 2026-10-21 a page reader tried only on native-control
  fixtures could not click a component library's menu,
  docs/bugs/2026-10-21-browser-script-clickable-controls.md). Promoted
  2026-10-21 → AGENTS.md "Verification" (the first bullet); design-review
  lenses 9 and 11 ask the same.
- **Ship capabilities, not use cases: a use case is a member's to build,
  or a sample in its own repository, never hardcoded in the platform.**
  Seen 4× (2026-10-12 interactive apps with platform modes and lifecycle;
  2026-10-17 room memory compiled into four services; 2026-10-20 an
  outcome-review room planned as built-in content with fixed causes,
  columns and digest; 2026-10-24 a backends draft with a scanner, an
  ecosystem list, a viewer and a channel in the platform, caught in review
  by the product owner before any code). Promoted 2026-10-20 →
  `docs/design-review.md`, lens 14; hardened 2026-10-24 → AGENTS.md "Core
  rule: the platform provides only what it must" and lens 14's Placement
  table (content, component or platform, for every part, in every PRD). The
  recurrence was the draft not asking the question per part: it asked it for
  hosts, the headline, and not for the parts around them.
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
  Recurred 2026-10-19: the room relay forwarded on a 20 s client, so a
  relayed image or video call was cut at 20 s (fixed in e0c66127).
- **Prove an upgrade keeps what is in use: the same session, its files, its
  conversation and its tools, on a live session before and after.** Seen 3×
  (2026-10-13 workspace bindings lost; 2026-10-14 room tools lost on
  restore; 2026-10-16 new guidance and tools never reached kept sessions,
  proven only on new ones). Promoted 2026-10-17 → AGENTS.md "Releases" (a
  change to what an agent is told or given is proven on a resumed session).
  Recurred 2026-10-19: a tool named by its versioned binary changed every
  session's binding on each release and restarted the conversations using it
  (docs/bugs/2026-10-19-tool-update-resets-sessions.md; lens 2 now asks).
