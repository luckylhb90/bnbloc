# Demo app build: sandboxed agent without provider env, an agent error posted as a reply, seven versions of one site (2026-10-13)

Status: fixed (commits at the end)

Three defects seen together on a QA deployment while a member had their own
agent build a demo app (a static site) in a room, with the desktop's agent
sandbox mode set to `all`. The agent was an ACP-driven coding CLI whose model
provider reads an API key from the environment.

## 1. A sandboxed agent misses its provider environment

**Repro.** Settings → Agent sandbox → "All my runs". The member's provider key
lives in the environment the agent is launched with on the host
(`<PROVIDER>_API_KEY`). Ask the agent anything. The CLI runs in the sandbox VM
and fails at startup: `Missing environment variable: <PROVIDER>_API_KEY`.

**Root cause.** What crosses into the guest is one fixed list,
`protocol.VMExecForwardEnv` (terminal, locale, git identity), used by both
ends: the bridge's space `envAllowlist` (`services/bridge/sandbox.go`
`prepare`) and `bnbloc-vmexec run`'s `ForwardedEnv(environ)`. The list is
right as a default (never credentials), but a member had no way to let a
named variable through for their OWN runs, so a CLI that needs one cannot
run sandboxed at all.

**Do cores advertise the env they need?** No. Neither the Core Protocol
`Target` nor discovery carries a required-environment list, and the ACP
adapter's per-agent `env` is the adapter's own configuration. The bridge
therefore cannot pre-check; it can only explain (below).

**Fix (one owner per rule).**

- The member lists environment variable names, or `PREFIX*` patterns, in
  Settings → Agent sandbox → "Environment passed to sandboxed agents"
  (desktop setting `sandboxEnv`, shell-owned like the mode). Only names are
  stored; a value is read from the agent's own host environment at the
  moment a command crosses into the guest, so no secret value is written to
  bnbloc's settings, the bridge, the descriptor, the job record or a log.
- "Is this a pattern a member may forward" is `pkgmeta.ValidForwardEnv`
  (an env name, `ValidEnvName`, optionally ending in `*`; never a name the
  sandbox or bnbloc owns: `BNBLOC_*`, the guest's `HOME`/`USER`/`PATH`/…,
  loader variables), pinned to the desktop's copy by
  `core/pkgmeta/testdata/forward_env_patterns.json`. The desktop refuses an
  invalid entry when it is saved; the bridge drops one it is handed (logged,
  never fatal).
- The desktop hands the list to the bridge as `BNBLOC_SANDBOX_ENV`; a change
  restarts the bridge through the stabilizing watcher, like a mode change.
- "Does this drive get the owner's list" is `decideSandbox`
  (`sandboxDecision.ownerEnv`): only the owner's own drive wearing nothing
  from someone else that runs scripts. A borrowed drive, a platform drive
  and a drive forced into the sandbox by foreign content get the default
  list only, so the owner's secrets never reach someone else's prompt.
- The per-drive list travels in the execution descriptor
  (`SandboxExecution.Env`, wire `env`, vmexec `BNBLOC_VMEXEC_ENV`), and both
  ends build their allowlist from `protocol.ForwardEnvAllowlist(extra)`: the
  space `envAllowlist` the bridge ensures every turn (so a borrowed turn
  after an own one converges back to the default) and vmexec's
  `ForwardedEnv`. The space allowlist is the hard bound: the sandbox runtime
  drops any name it does not list, so even a CLI process a core kept alive
  from the owner's turn cannot pass the owner's names into a borrowed
  turn's commands.
- A sandboxed own drive whose agent reports an error gets a hint in its
  notice: variables the agent needs reach the sandbox only when listed in
  that setting.

## 2. An agent CLI's startup error was recorded as a normal reply

**Repro.** Same turn as above. The job ended `replied`; the room showed the
CLI's error text ("Warning: Model metadata… Missing environment variable…")
as the agent's message.

**Root cause.** `reflow` (`services/bridge/bridge.go`) posted every
completed assistant message as soon as it was read and, when the turn went
idle, honoured the core's `failed` only if nothing had been posted
(`ts.Failed && !posted`). A core that reports the failure on `PollTurn`
(`failed` + `failMessage`) AND leaves the agent's error output as the turn's
last message therefore produced a `replied` job whose reply was the error.

**Fix.**

- A failed turn fails the job, whatever was posted before: `reflow` returns
  `turnFailedError` whenever the idle status says `failed`, and `settle`
  reports it failed (never `replied` with a warning).
- The new final reason `agent_error` (`store.FailureAgentError`, a
  `VARCHAR(64)` column, no migration) is the agent or its core reporting the
  started turn failed; `runner_failed` stays for bridge-side failures and
  ambiguous starts. `driveFailureReason` is still the one classification.
- The notice carries the core's message verbatim as its `detail`
  (`wire.JobNotice.Detail`, cut by `roomlog.Truncate`), which the feed
  renders fenced (monospace, apart from bnbloc's sentence), never as
  Markdown or as a reply.
- Messages read in the same poll that found the turn failed are withheld:
  they completed together with the failure, so they are the failure's output,
  not a reply. Messages read while the turn was still running were already
  posted and stay.
- A turn that wrote files and then failed still publishes them at settle.

**Adapter follow-up (bnbloc-acp, another repository).** The bridge can only
honour what the core reports. The adapter should (a) keep reporting a
JSON-RPC error or an agent exit as `failed` with the error text (it does);
(b) not serve text the agent streamed during a turn that ended in an error
as a completed assistant message, or end such a turn `failed` when the agent
ends it with `end_turn` after printing an error (an agent that reports a
startup error only as `agent_message_chunk` text is indistinguishable from a
reply: that is the adapter's or the agent's bug); (c) optionally declare the
environment each configured agent needs (a per-agent `requiredEnv`, served
as a target field), so the bridge could warn before a sandboxed drive
instead of after it.

## 3. One turn published seven versions of the same site

**Repro.** Ask the agent to build a small static app. While it iterates on
`index.html`/`app.js` the room's Docs show the site at v1, v2, … v7, all from
the one turn.

**Root cause.** `reflow` ran `publishDeliverables` every 10 seconds while the
turn ran ("deliverables surface while the turn runs"), and `settle` ran it
again after the turn. Each pass that saw the site's files changed packed and
published a new site version, so every rewrite the agent made mid-turn
became a version. Individual files had the same defect.

**Fix.** Publishing runs once per turn, at settle, after `reflow` returned
(the turn is idle, or the drive gave up on it): each site (and file) changed
in the turn gets exactly one new version, an unchanged one none (the
snapshot diff, then the room's "unchanged" no-op). The mid-turn pass is
removed rather than turned into a preview channel: nothing reads a
non-versioned preview today, the mid-turn pass was the only writer of
intermediate versions, and a live preview of a half-written site is a
separate feature (it would need a preview surface that never mints
versions). The deliverable cards now appear when the turn ends, together
with the reply.

## Tests

Each was broken once on purpose (the fix reverted or weakened) and failed.

- `TestSandbox_ownDriveForwardsTheOwnersEnv_borrowedNever` (bridge): the
  own sandboxed drive ensures the space with the owner's names and hands
  them in the descriptor; a borrowed turn of the same agent right after
  converges back to the default list and carries no names.
  `TestDecideSandbox`: `ownerEnv` only for an own drive, never a borrowed
  one or one forced by foreign content. Broken by `ownerEnv: !forced`.
- `TestRun_forwardsTheDescriptorsEnvNames` (vmexec): the listed names cross
  with their host values, nothing else does. Broken by forwarding the
  default list only.
- `TestSandbox_agentErrorHintsAtTheEnvSetting_onOwnRunsOnly` (bridge): the
  setting hint on an own run's agent error, never on a borrowed run's.
- `TestValidForwardEnvMatchesFixture` (Go) and
  `forward_env_pattern_matches_go` (desktop): the rule and its pin;
  `TestParseSandboxEnv`, `forward_env_list_splits_dedupes_and_refuses_whole`,
  `TestForwardEnvAllowlistAndForwardedEnv`; `TestVMExecEnvRoundTrip` and
  `TestExecutionWireShape` cover the new descriptor field; the desktop's
  bridge env and fingerprint tests cover `BNBLOC_SANDBOX_ENV` and the
  restart on a changed list.
- `TestFailedTurnIsNeverAReply` (bridge): a core that reports `failed` and
  leaves its error as the turn's last message ends the job `agent_error`
  with the error in the notice's detail, with and without progress posted
  before; the error never reaches the room as a reply. Broken by the old
  `ts.Failed && !posted` rule (the job ended `replied` with the error as the
  reply: the QA symptom) and by dropping `agentFailed` from settle.
- `testNoticeCarriesTheAgentErrorApart` (web) over the golden
  `internal/wire/testdata/job_notice.json`.
- `TestOneTurnPublishesOneSiteVersion` (bridge): a turn that rewrites its
  site on every poll publishes exactly one site version, the final one.
  Broken by publishing on every poll of the running turn: 7 versions.

## Landing

- `bb38150` bridge: an agent's error fails its job; deliverables publish
  once per turn (2, 3).
- `819f6b4` sandbox: the member's own runs may pass named environment into
  the VM (1).
- Adapter follow-up (bnbloc-acp): see 2, "Adapter follow-up"; not landed
  here.
