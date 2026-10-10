# Sandboxed Claude Code commands with a single quote ran on the host (2026-10-09)

Status: fixed and verified — `bnbloc-vmexec` in aa30497, shipped in the
bnbloc-sandboxd 0.1.1 component (which bundles vmexec).

## Repro

Sandbox mode "All my agent runs", desktop 0.1.19, the installed Core Protocol sidecar core,
bnbloc-sandboxd 0.1.0. In a room, ask a Claude Code agent of that core:

- `uname -sm; pwd; id -un` → `Linux aarch64`, space user: routed to the VM.
- `ls -A /Users/<me>; ls ~/.ssh; cat ~/.zshrc | head -1; curl -s -w
  'public:%{http_code}\n' …` → the full host home listing, `Operation not
  permitted` (Seatbelt) for `~/.ssh`, and the first line of `~/.zshrc`:
  the command ran on the macOS HOST (confined), not in the VM. `~/.zshrc`
  exports an API key.

## Root cause

Claude Code passes every spawn through `CLAUDE_CODE_SHELL_PREFIX`; the
`claude-prefix` router sent a spawn to the sandbox only when it matched the
Bash-tool shape, and ran everything else on the host as "host tooling".
The eval pattern accepted an embedded single quote only as `'\''`, but
Claude writes it as `'"'"'` (captured with a logging prefix). So every Bash
command containing a single quote failed recognition and ran on the host:
classification failed OPEN.

Second gap: the host confine profile denies reads of credential stores but
not of shell startup files, so the host CLI (its Read tool, or any command
that reached the host) could read `~/.zshrc` and the keys it exports.

## Fix

- `cmd/bnbloc-vmexec/claudeprefix.go`: classification fails closed. Claude's
  cwd tail (`… && pwd -P >| <file>`) alone marks a Bash-tool command, and it
  always runs in the sandbox. The eval word is decoded by a real POSIX
  word parser (single/double quotes, backslash escapes, both quote-escape
  styles) only to decide passthrough; an unparsable word means no
  passthrough, never a host run. Tests use the exact captured string.
- `cmd/bnbloc-vmexec/confine.go`: shell startup and history files
  (`.zshrc`, `.zshenv`, `.zprofile`, `.bashrc`, `.profile`, histories, fish
  config, `~/.env`, `~/.envrc`) join the read-denied sensitive paths. A
  confined zsh skips an unreadable startup file silently (verified).

Not a sandbox bug, found on the way: the `-native` harnesses fail in every
mode on this machine. Native `claude` has no login (no keychain entry or
credentials file), and native `codex` reads `LITELLM_API_KEY`, which only
`~/.zshrc` exports. A desktop-launched core daemon never sources it. The
wrapper-launched harnesses carry their own auth and work.

## Landing

Commit aa30497; bnbloc-sandboxd 0.1.1 published the same day. Verified on
the real runtime path (room chat → bridge → core → vmexec) with Claude Code,
Codex and OpenCode agents: quoted commands report `Linux aarch64`, the host
home shows only the shared workspaces tree, `~/.zshrc` and `~/.ssh` do not
exist in the guest, public egress works, and Claude's (host-side) Read tool
gets `EPERM` on `~/.zshrc`.
