# A sandboxed agent's commands never got the egress services' CLI placeholders (2026-10-27)

Status: fixed (see Fix).

## Repro

An organization enables a connector with `cliEnv` (e.g. a release CLI
whose token variable the connector declares, `FOO_TOKEN=connected`). An
agent in a sandboxed room runs the CLI: it says it is not signed in and
asks the person to paste a token, though the person connected the system.
The same CLI run by a host agent works.

## Root cause

The services' `EgressHosts.Env` joined the session environment only for a
host session (`roomToolsContributor.egressEnv`). A sandboxed session's
commands take their environment from the space (`sandboxEgressEnv`, set on
every turn's ensure), which carried the proxy and the CA files only: the
placeholders were asked for (`egressFor`) and dropped.

## Fix

The space's environment is built for the turn's job: `sandboxRuntime.spaceEnv`
gets the room and the job, and `sandboxEgressEnv` asks the egress services
for that job and puts their placeholders under the proxy's own variables
(which always win). One more `egress/hosts` question per sandboxed turn.

## Why it was not caught

Lens 1 (actors) and 11 (release): the sandbox was added after `cliEnv`, and
the sandbox test checked that the agent's own process gets nothing, not that
its commands get what a host command gets. The question added to
`docs/design-review.md` lens 1: "Does a sandboxed command get everything a
host command gets for the same drive (environment, CA, placeholders), and
does a test compare the two?"
