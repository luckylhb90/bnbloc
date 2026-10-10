# Component artifact upload usability

Date: 2026-10-01  
Status: implemented — `ComponentUploadFlow` stages binaries through
`POST /v1/platform/artifacts` before assembling the signed envelope, as
described below.

## Intent

Finish the Task 6 web usability gap for runtime packages by keeping the unified
Components Workbench upload flow, while letting super-admins attach real
platform binaries before publishing sidecar core or plugin packages.

## Requirements

- `ComponentUploadFlow` remains the single admin publish path for core and
  plugin packages.
- Endpoint and manifest runtime packages stay JSON-only.
- Sidecar runtime packages require each declared platform artifact to be backed
  by an uploaded binary before publish is enabled.
- Each selected binary uploads to the gateway's existing
  `POST /v1/platform/artifacts` endpoint; the returned URL/key and sha256 are
  used in the package envelope that is sent to canonical
  `POST /v1/platform/components`.
- The UI shows per-platform file selection, upload progress, success hashes, and
  errors inline; no placeholder URLs, fake hashes, or demo binary metadata are
  generated in the browser.
- The old separate sidecar publish panel is not restored.

## Acceptance

- Unit tests cover artifact requirement detection, platform upload completion,
  and payload rewriting with uploaded artifact metadata.
- Web typecheck, file-length gate, and production build pass.
