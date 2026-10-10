# Generic desktop sidecar manager

Status: implemented (99ae898)

## Intent

Internal desktop builds must run installed sidecar cores from gateway package
metadata instead of a hardcoded single-core adapter. The gateway is the source of
truth: `/v1/cores` identifies installed packages that require desktop-managed
sidecars, and `/v1/cores/{name}/artifact?platform=<goos-goarch>` supplies the
versioned binary, checksum, and runtime contract.

## Acceptance

- The desktop internal adapter manages zero or more sidecars keyed by core name.
- It downloads, checksum-verifies, caches, and supervises each sidecar binary.
- It injects every known managed sidecar into bridge env via `BNBLOC_CORES` and
  `BNBLOC_CORE_<NAME>_URL` before the bridge is started from the web flow.
- The web desktop status surface renders a generic managed-sidecar list rather
  than a single core-specific card.
- Public desktop builds stay free of the internal sidecar manager.

## Status

Implementation in progress in the desktop internal extension and Agents page.
