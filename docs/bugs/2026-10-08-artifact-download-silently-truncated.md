# Artifact downloads are silently truncated on slow links

Status: fixed

## Repro

`bnbloc updates publish` for a 31 MB desktop archive failed its read-back
check; repeated downloads of the same key over the production gateway:

```
http=200 size=31056149 time=145s sha=621fac06…   (ok)
http=200 size=20131148 time=99s  sha=1a9cec5a…   (truncated, still 200)
```

## Root cause

- The gateway proxied `GET /v1/artifacts/{key}` under a 2-minute deadline for
  the whole exchange, body included. A client that needs longer than that
  (slow link, large desktop update) had the upstream read cancelled mid-body.
- Neither realtime's blob read nor the gateway proxy sent `Content-Length`,
  and the proxy ignored the copy error, so the cut ended the response as an
  apparently complete `200`. Clients could only notice through a hash or
  signature check (the desktop updater and sidecar manager then fail with a
  misleading verification error; the CLI read-back caught it).

## Fix

- `blobstore.BlobStore.Get` returns the blob size (disk: file size; S3: the
  object's Content-Length), and realtime sets `Content-Length` on blob and
  attachment downloads.
- The gateway forwards `Content-Length`, bounds only the wait for upstream
  response headers (not the body), and aborts the connection when the copy
  fails, so a broken transfer is a transport error, never a short 200.

Regression tests: `TestArtifactDownloadForwardsLengthAndAbortsOnUpstreamCut`
(gateway), blob/attachment Content-Length tests (realtime), store size tests.
