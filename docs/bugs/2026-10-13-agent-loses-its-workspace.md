# An agent loses its workspace when its session changes (2026-10-13)

Status: recorded, fix in progress.

## Symptom

Seen on a QA deployment, in a room, with an agent on an ACP-adapter core
(provider `opencode`):

1. A member asked the agent, wearing the built-in `app-builder` expert, to
   build an interactive app. The agent built it in its workspace and the
   bridge published the site at the end of the turn.
2. Later the member sent a plain follow-up with **no expert** ("the page
   doesn't load"). The same agent answered that:
   - its workspace was empty apart from `.codegraph` and `.room`: the app's
     source was gone;
   - the `interactive-apps` skill was not loaded;
   - it had no `read_artifact` or any other room tool.

   So it could neither inspect nor fix its own app.

## Repro

1. Drive an agent on a core that creates a workspace per `POST
   /workspaces`, with an expert; have it write `index.html`.
2. Send a follow-up without the expert (or restart the bridge, or let the
   core drop the session so the bridge rebinds it).
3. The follow-up runs in a new, empty workspace.

## Likely causes (to verify, not assume)

- **A.** When the session's binding sum changes (an expert on one turn,
  none on the next), `ensureSession` unbinds the slot and creates a new
  session **in a new workspace** (`createWorkspace`). The old workspace,
  with the agent's files, is left behind. The same happens on a stale
  session rebind (the core restarted or dropped the session) and on every
  bridge restart (bindings live only in memory). A content change should
  start a new session but keep the agent's workspace.
- **B.** The room tools may be attached only when some profile or expert
  content is present, or a new session may not get them.
- **C.** Something else may clear or recreate a workspace on a session
  change: the sandbox fence's reset, the per-session reset of agent config,
  the adapter's workspace handling.

## Root cause

(filled in as each cause is verified)

## Fix

(filled in as it lands)
