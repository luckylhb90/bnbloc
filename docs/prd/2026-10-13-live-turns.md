# Live turns: progress while an agent works, one owner of how long it may (2026-10-13)

Status: accepted (product owner, 2026-10-13).

Status (2026-10-13): bnbloc's side built (§2, §3's protocol, relay and
event, §4 but the cores' own changes and the web strip). The reference is
`docs/architecture/core-providers.md`, "A turn's length and its
progress". Decided while building:
- **The event**: `agent_job_progress` (transient, channel-scoped, not
  replayed) carries `wire.AgentJobProgress {jobId, startedAtMs, activity,
  atMs}`; the bridge posts `wire.JobProgressInput` to `POST
  /v1/bridge/jobs/{jobId}/progress` (activity: only the bridge holding the
  live job, the room from the stored job; at most every 2 seconds, only
  when it changed). A job leaving the live statuses ends its progress.
- **A deadline fails `reply_timeout`** (final, "ran out of time after
  N"); the bridge's own deadline has a minute's grace so the core's
  report arrives first.
- **The credential hint** matches 401, "unauthorized", an API key, "not
  logged in", "login required", "authentication", "credential"; a 403 is
  not one (the key was there and refused).
- **A forgotten workspace** was already recreated on a 404 at the session
  create (at the same workdir for a sandboxed slot): the contract now says
  a core answers 404 `workspace_not_found`, never `bad_request`, for a
  sandboxed workspace whose directory it lost.
- **Host git hardening** neutralizes hooks and fsmonitor; a filter or
  diff driver a guest writes is not, and is documented.

Status (2026-10-14): §2 as revised built on bnbloc's side — no fixed turn
length, the renewal of a sandboxed turn's command access, the long-quiet
note. Decided while building:
- **No bound unless set.** `BNBLOC_BRIDGE_MAX_TURN` defaults to none;
  without it no `deadlineMs` is sent and only the stall window ends a turn.
  A core's `deadline` failure without a bridge bound reports the core's own
  message.
- **Renewal** is `executionRefresh` (`PUT …/sessions/{sid}/execution`,
  `core.ExecutionRefresher`); the first token lives 1 hour on a core that
  takes renewals (renewed every 30 minutes until the turn ends), 4 hours on
  one that cannot. A failed renewal is logged and retried.
- **A long-lived exec** (a CLI's exec server) keeps working: vmexec reads
  `BNBLOC_VMEXEC_TOKEN_FILE` for every request, controls an exec with its
  starting token and, once that expired, the renewed one; the Sandbox
  Protocol now lets any exec token of a space control the execs of that
  space (a sandbox implementation must follow; the conformance step checks
  it).
- **Long quiet**: after 10 minutes without a new step the strip says "No
  new step for N min — still running" instead of rotating notes.
- **A long step says how long (added 2026-10-19).** A step the agent keeps
  streaming into (reasoning, writing) refreshes its time, so the turn is
  never quiet while it lasts. Once the current step has lasted a minute,
  its line shows how long ("Thinking · 5m 12s"), counted from when the
  step began, not from its latest chunk. QA: a model reasoned for minutes
  on one step of a browser quiz and the member saw only "Thinking".
  - No automatic retry: rerunning a job that already acted could repeat
    what it did (a click, a submit). The member stops it and sends
    "continue" in the same session, which resumes from where it was.

## 1. Why

A member asked an agent on a desktop core to build an expert from a
brand template. For eight minutes the room showed one chip, "collecting
reply", with no output; then the turn failed with a JSON dump and advice
to add environment variables to the sandbox.

- **Nothing to watch.** A core returns a turn's text only as completed
  messages, and some cores (a CLI driven one-shot) only at the end. The
  member cannot tell a working agent from a stuck one. Other products show
  a continuous trail under the request: the step being run, the files
  read, the commands executed, gentle notes while it thinks.
- **Two owners of a turn's length.** The bridge ends a turn after 30
  minutes; a core killed its CLI after 5 (a constant in the core,
  `SIGKILL`), taking down the CLI's local model proxy, so the agent's
  remaining calls failed with "connection refused" and the room got a
  misleading reason.
- **Misleading failure text.** Every failed sandboxed turn got the
  "add environment variables" hint, whatever the cause.

## 2. One owner of a turn's length — and no fixed length

Revised 2026-10-14 (product owner: development tasks run for hours; a
fixed length is the wrong rule). What ends a turn is the agent stopping
work, never the clock.

- **No total bound by default.** A turn runs as long as the core reports
  it busy or it produces output; only a genuine stall (the core reports
  the turn neither busy nor producing anything for the sliding window,
  2 minutes) ends it. A deployment may still set a bound
  (`BNBLOC_BRIDGE_MAX_TURN`, default 0 = none); only then does the bridge
  send the turn's deadline to the core.
- **Core Protocol:** `CreateSessionRequest` and `SendInputRequest` carry
  `deadlineMs` (Unix ms) only when the caller bounds the turn. A core ends
  a turn by then — failed, fail code `deadline` — never earlier for time
  reasons, and with no deadline it imposes none of its own.
- **Long quiet is shown, not killed.** A single command can run for an
  hour without a new step (a build, a test suite); the progress strip says
  "no new step for N minutes — still running" and the member decides with
  Stop.
- **A sandboxed turn's command access renews itself.** Its exec token is
  short-lived (1 hour) and the bridge renews it every 30 minutes while
  the turn runs: Core Protocol `PUT
  /core/v1/workspaces/{ws}/sessions/{sid}/execution` (capability
  `executionRefresh`) hands the running turn a fresh execution
  descriptor; the core writes the token to a file only the member can read
  (`BNBLOC_VMEXEC_TOKEN_FILE`) that vmexec reads at every command. A turn
  that ended stops being renewed, so its token lapses within the hour.
- **A stopped turn stops everything it started**: a core kills the whole
  process group of a turn it ends (deadline, interrupt), so no child keeps
  calling a dead proxy.
- **Failures say what happened.** A deadline is "ran out of time after
  N minutes". The sandbox environment hint is added only when the error
  reads as missing credentials (401 from the model, "API key",
  "not logged in", "authentication"), never to every sandboxed failure.

## 3. Progress while it works

- **Core Protocol:** `PollTurnResponse` gains, for a running turn,
  `startedAtMs` and `activity`: the latest steps (at most 8, oldest first),
  each `{kind, title, atMs}` with kind `tool` (a command run, a file read
  or written, a search: a short title such as `Bash: python extract.py`
  or `Read: template.pptx`), `thinking` (the agent is reasoning) or
  `text` (it is writing). Titles are cut to 120 characters and never hold
  file contents or secrets. Optional capability `turnActivity`.
- **Narration is a message.** A core completes each assistant text block
  as its own message as soon as the agent moves on (a tool call, a new
  block), so narration ("Meanwhile, extracting per-slide structure...")
  reaches the room during the turn and is never glued to the final answer.
- **The bridge relays activity** to the room as an ephemeral update of
  the job (realtime, not history): never stored as messages, never fed
  back to an agent, gone when the turn ends.
- **The web** shows under a live job: the elapsed time, the current step
  and the last few, and — after 20 quiet seconds — a rotating line of
  plain notes ("Complex tasks can take several minutes", "You can leave
  this room; you will be notified when it is done", the agent's model and
  core). The "collecting reply" phase label becomes "working".

## 4. Also in this round

- **Room tools for every core**: a core that drives CLIs one-shot advertises the MCP
  transports its CLIs accept, so its agents get room tools.
- **A core that forgot a workspace** (restart, crash, upgrade): when a
  session create or write is refused because the core no longer knows the
  workspace, the bridge creates it again at the same directory and keeps
  going, once per turn.
- **Host git never runs what an agent wrote into `.git/`**: every CLI a
  core starts on the host for a sandboxed turn gets
  `core.fsmonitor=false` and `core.hooksPath=/dev/null` through
  `GIT_CONFIG_COUNT`, so a hook or fsmonitor command the guest wrote is
  never executed on the host by the agent's own git calls.
- **Release checks**: `scripts/release-check.sh` runs `bnbloc smoke
  --agent` for each of the operator's agents; core, desktop and service
  releases run it before and after (see AGENTS.md).

## 5. Tests

- Protocol: `deadlineMs` and `activity` round-trip; a core without
  `turnActivity` still works.
- Bridge: the deadline is sent; the bound is the bridge's; activity is
  relayed and cleared; a forgotten workspace is recreated once; the
  environment hint only for credential errors.
- Cores: a turn runs past 5 minutes; a deadline fails the
  turn with `deadline` and kills the process group; activity from tool
  calls; narration completes as its own message; every core advertises MCP
  transports.
- Web: the progress strip (elapsed, steps, quiet notes), hidden when the
  job ends.
