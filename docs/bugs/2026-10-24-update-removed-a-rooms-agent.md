# An update removed the program a room's conversation continues with (2026-10-24)

Status: fixed (bnbloc-acp 0.2.12, agent-manager 0.1.10). Found by testing the
case the owner named as intolerable, before any member lost a conversation.

## What happened

The agent manager installs each version beside the previous one and, after an
update, removed every version but the current one and the one a rollback goes to.
The ACP core's sessions fix the program they were created with and continue with
exactly it after an idle stop. With a conversation on version 1 and two updates,
version 1 was removed, and the next message failed with
`command ".../1.18.35/.../opencode" not found`: the session was marked lost and
the conversation with it.

## Repro (real core, real model, free model; /tmp lab, scripts not kept)

1. Manager installs opencode 1.18.35, registers it; the core runs with a 15 s
   idle stop.
2. A session is told a code word; the next turn recalls it (control: passes).
3. After the idle stop (a 60 s sweep: the first run waited 22 s and tested
   nothing), the manager updates to 1.19.0 and then 1.19.4 (1.18.35 removed).
4. "What was the code word?" fails `agent_error`, command not found. Lost.

## Root cause

Two owners each assumed the other: the manager removed old versions without
knowing which sessions held them (it cannot see the core's sessions), and the core
held a path it had no way to protect. The session's launch is a copy taken at
creation (`session.agent`), so even a correct registration does not help.

## Fix

- Core: publishes what its sessions run from (`in-use.json`) and, when a launch's
  file is gone, continues with the agent's current registration (re-read first).
- Manager: removes only versions no live session holds, nothing at all with no
  word from the core, and refuses Remove while a room runs the agent.
- PRD §2.4a; the earlier claim that a running agent "keeps running from its old
  files" (never checked, false for a Node program) is corrected there.

Verified: the same scenario against the new core and manager keeps 1.18.35 and the
code word is recalled; with only the core's fallback (old manager) the session
continues on 1.19.4 and recalls it; unit tests in both repositories, and the
fallback test fails without the fix with the production error text.

## Why it was not caught

- Lens: 2, time and lifecycle ("updated afterwards: does the session keep its
  memory?" was there; it was answered "not verified" in the PRD and the feature
  shipped optin with the answer still open).
- Missing question: *who else holds the thing I am about to delete?* A cleanup of
  files another process may still run from needs that process's own list, not a
  count of versions.
- Missing test: a conversation across two updates with the real core. The test
  that mattered needed a sweep longer than the idle stop to reach the resume path
  at all; an experiment that does not reach the path it tests proves nothing.
- Added to docs/design-review.md (lens 2, "Seen" and the question).
