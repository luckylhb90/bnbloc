# Desktop agents miss the member's shell environment (2026-10-13)

Status: fixed (4908134).

## Symptom

On macOS, with the desktop app started from Finder or the Dock, an agent
CLI driven through a core sidecar (for example an ACP adapter, which spawns
the agent CLI) fails with errors like `Missing environment variable:
EXAMPLE_API_KEY`, although the member exports `EXAMPLE_API_KEY` in
`~/.zshrc` or `~/.zprofile` and the same CLI works in a terminal. CLIs
installed by version managers (mise, asdf, nvm, …) are not found either.
The only workaround was copying each secret into every core's own config
file.

## Repro

1. `export EXAMPLE_API_KEY=…` in `~/.zshrc`.
2. Quit the desktop app, start it from the Dock.
3. Run an agent whose CLI needs `EXAMPLE_API_KEY`: the turn fails with the
   missing-variable error.
4. Start the app from a terminal (`open -a` does not help, the binary
   directly does): the same turn works.

## Root cause

A GUI app on macOS is started by launchd with the user session's minimal
environment, never a login shell: nothing in `~/.zshrc`/`~/.zprofile` runs.
Linux desktop sessions behave the same for rc files. The desktop spawns
the bridge and every sidecar (cores, plugins, the sandbox daemon) with its
own process environment, and they spawn the agent CLIs and MCP stdio tools
with theirs, so the member's exported variables and PATH never reach any of
them. `sidecar_path_env` patched PATH for sidecars alone with a hardcoded
list of directories, which is a guess at the symptom and misses every
other variable.

## Fix

One owner in the shell, `apps/desktop/src-tauri/src/shell_env.rs`:

- Once per app launch (first child spawn waits for it), run the member's
  `$SHELL` (`/bin/zsh` on macOS, `/bin/sh` elsewhere, when unset or not
  absolute) as an interactive login shell, `-ilc`, printing the environment
  NUL-separated (`env -0`) between a per-run random marker; stdin
  `/dev/null`, its own session (no controlling terminal), bounded by a
  timeout after which its process group is killed. Output outside the
  markers (rc banners) is ignored. A failure or timeout is logged without
  any value and the children get the app's own environment.
- Never imported: `BNBLOC_*` and shell-session names (`PWD`, `OLDPWD`,
  `SHLVL`, `_`, `TERM_SESSION_ID`, `SHELL_SESSION_*`). PATH is merged: the
  shell's entries first, then the app's entries not already there.
- `child_process::spawn(cmd, &ShellEnv)` applies it beneath the command's
  explicit env: anything the desktop sets for a child (`BNBLOC_*`, tokens,
  ports) wins, and a child's own config (a core's per-agent env) applies
  later inside the child, so it wins too. The hardcoded `sidecar_path_env`
  is deleted. Only children get it; nothing the app sends over the network
  carries it.
- Logs carry the imported count only (names in debug builds), never a
  value.
- "Reload shell environment" (Settings → Agent sandbox, Tauri command
  `shell_env_reload`) re-resolves and restarts the bridge and the sidecars.
- Windows: a no-op (GUI apps inherit the user's variables there).
- The sandbox's "Environment passed to sandboxed agents" reads values from
  the bridge's environment, which now includes the shell's.

## Exposure

The shell's values now sit in the environment of the bridge, the cores and
the agent CLIs they start, as they would for a terminal-launched agent. A
sandboxed turn's guest still gets only `protocol.ForwardEnvAllowlist`
(the member's listed names only on their own drives), so a borrowed or
platform drive's commands in the VM never see them. An unsandboxed
borrowed drive runs on the host as before: it could already read
`~/.zshrc` there.

## Verification

- Every test of the change was broken once and seen to fail (parse start,
  the `BNBLOC_*` filter, PATH order, apply over explicit env, the timeout,
  `Debug` and log lines printing values, the owner test with a caller-built
  env, `spawn` without `apply`, the shell fallback, the failure outcome).
- A real `/bin/zsh -ilc` on a developer Mac: 144 names in 0.9 s, count
  only in the log.
- `scripts/ci.sh desktop` and `scripts/ci.sh web` pass; no new clippy
  warning on macOS or the Windows type-check.

Landing commits: 4908134 (shell_env, `child_process::spawn(cmd, &ShellEnv)`,
the reload command and button, tests, AGENTS.md and the desktop README).
