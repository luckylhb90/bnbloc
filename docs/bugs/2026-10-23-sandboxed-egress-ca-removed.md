# A sandboxed session's commands could not use the egress proxy (2026-10-23)

Seen on QA with desktop 0.1.101: a member's own agent, moved into their
own sandbox, ran `curl https://<a connected system>/…` and got
`curl: (77) error setting certificate file: <workdir>/.room/egress/bundle.pem`
for every request. Requests from the host (the same agent before the
move) worked.

## Root cause

The space's environment points the commands at the proxy's CA files under
the workspace (`.room/egress/`), written when the space is prepared
(`sandboxEgressEnv`, in `prepare`). A fresh session then resets the
workspace's bnbloc-owned paths (`resetProtectedPaths`, `.room/` included)
before it is created, which removed the files the environment named.

## Fix

The CA files are written again when the session starts (the room tools
contributor, after the reset) and at every turn (its turn hook), and still
when the space is prepared: `writeSandboxCA`. Test:
`TestSandboxedSessionKeepsItsCAFiles` (the files are back after a reset, at
the session's start and at a turn).

## Why it was not caught

The e2e's sandboxed leg (`TestE2EConnections/sandboxed`) built the bundle
inside the guest itself, so it never exercised the files the bridge writes
into a workspace, and the bridge's own test checked the environment, not
that the files it names exist when a command runs. Lens: "a file another
step owns" — `.room/` is reset by the session's start, after `prepare`.
