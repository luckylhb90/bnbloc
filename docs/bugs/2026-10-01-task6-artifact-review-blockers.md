# Task 6 artifact review blockers

Status: fixed (4bb7475 server-side artifact provenance + the Components Workbench upload-then-sign flow; recorded in 9db86a8)

## Repro

The Components Workbench lets a super-admin load a sidecar/plugin package JSON,
upload platform binaries afterward, and then publish with a detached signature
that may have been produced for the original pre-upload JSON. A malicious or
confused client can also submit sidecar/plugin artifact URLs that point at an
arbitrary external host as long as the URL is absolute and the hash is shaped like
sha256.

## Root cause

The publish boundary treats the `package` request field as the signed body, but
the browser mutates artifact entries after parsing by replacing placeholders with
gateway upload results. The server validates URL shape only, not provenance, so a
signed package can pin an externally hosted artifact URL instead of a blob staged
through `/v1/platform/artifacts` and later served from `/v1/artifacts/<key>`.
The local parser also accepts ambiguous artifact placeholder objects whose keys
are neither the explicit `{url:"", sha256:""}` slot nor a complete real
artifact.

## Fix

Component publish now uses a clean upload-then-sign contract: sidecar/plugin
binaries are uploaded first, the client constructs a final package JSON string
with the returned `/v1/artifacts/sha256/<hash>` URLs, and signed production
publishes verify the detached signature over that exact final JSON string.
Unsigned dev publishes use the same final JSON after upload when the gateway is
explicitly configured to allow unsigned installs.

The gateway validates every sidecar/plugin artifact URL against its own artifact
download origin and `/v1/artifacts/sha256/<hash>` key shape, checks the URL key
matches the declared sha256, and probes realtime's blob store before install.
Endpoint and manifest packages remain JSON-only and cannot carry artifacts.
Package parsers accept only an explicit `{url:"", sha256:""}` upload slot (or an
omitted artifact entry) before upload; garbage or partial artifact objects are
rejected.

## Status

Fixed in the working tree. Targeted Go package tests and the component artifact
TypeScript regression script pass; full verification results are recorded in the
task handoff.
