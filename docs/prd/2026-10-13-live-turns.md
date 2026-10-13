# Live turns: progress while an agent works, one owner of how long it may (2026-10-13)

Status: accepted (product owner, 2026-10-13).

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

## 2. One owner of a turn's length

- **The bridge owns it.** A turn may run up to a deployment-wide bound
  (`BNBLOC_BRIDGE_MAX_TURN`, default 2 hours), with the existing sliding
  "no activity" window. The bridge sends the turn's deadline to the core.
- **Core Protocol:** `CreateSessionRequest` and `SendInputRequest` gain
  `deadlineMs` (Unix ms). A core ends a turn by then — failed, fail code
  `deadline` — and never ends one earlier for time reasons. A core without
  a deadline in the request uses its own generous bound.
- **A stopped turn stops everything it started**: a core kills the whole
  process group of a turn it ends (deadline, interrupt), so no child keeps
  calling a dead proxy.
- **Failures say what happened.** A deadline is "ran out of time after
  N minutes". The sandbox environment hint is added only when the error
  reads as missing credentials (401/403 from the model, "API key",
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
