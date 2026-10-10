# Large artifact downloads were cut after ~60 s (S3 client total timeout)

Status: fixed

## Repro

Production, the 249 MiB sandbox guest image
(`GET /v1/artifacts/sha256/c777b0…`): three downloads all failed with an
HTTP/2 stream reset (curl exit 92) after 14–178 MB, two of them at ~66 s;
`bnbloc updates publish` read-back checks also failed intermittently.

## Root cause

`internal/blobstore.S3Store` used `http.Client{Timeout: 60s}`. An
`http.Client` timeout covers the whole exchange **including reading the
response body**, so every S3 GET whose body took longer than 60 s to stream
(large blob, or a slow client applying back-pressure through the gateway)
was aborted mid-body. Since the 2026-10-08 truncation fix the gateway aborts
the client connection on a failed upstream copy, so the cut surfaced as a
stream reset instead of a short 200. Same defect class as
`2026-10-08-artifact-download-silently-truncated.md`, one hop further in.

## Fix

The S3 client no longer has an overall timeout. Its transport bounds only the
phases that must be quick — dial (10 s), TLS handshake (10 s), waiting for
response headers (60 s), 100-continue (1 s) — and the body streams for as
long as the transfer takes, cancelled by the caller's context (a client
disconnect cancels the gateway request, which cancels realtime's). Regression
test: a slow-body S3 stub streams for longer than the header timeout and the
read completes.
