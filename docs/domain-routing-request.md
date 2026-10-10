# Domain & Routing Request — bnbloc

For DevOps: one public domain fronting the bnbloc deployment on EKS. This
describes the domain, TLS, and the path-based routing the front proxy (ALB /
ingress-nginx) must apply. All backends run in-cluster; only this domain is
public.

## 1. Domain

- **One domain**, e.g. `bnbloc.example.com` (please assign the actual name).
- **TLS is mandatory on the whole domain** (see the security note under Relay —
  a sync token travels in cleartext and relies entirely on transport TLS).
- HTTP → HTTPS redirect.

The web client is served same-origin with the API, so the SPA, the API, and the
relay must all sit under this **one** domain (the browser calls the backend with
relative paths).

## 2. Path routing (front proxy → in-cluster Service)

Route by path prefix. Longest-prefix wins, so `/relay/` is matched before `/`.

| Path prefix | → Service (in-cluster) | Port | Notes |
| --- | --- | --- | --- |
| `/relay/` | `bnbloc-relay` | 8085 | **WebSocket** — forward the Upgrade; long idle timeout. Direct, NOT via gateway. |
| `/v1/`, `/api/`, `/sso` | `bnbloc-gateway` | 8080 | Authenticated API + SAML/OIDC endpoints. |
| everything else (`/`, `/assets/…`) | `bnbloc-gateway` | 8080 | Gateway serves the SPA itself (embedded). |

So in practice: **`/relay/` → relay; everything else → gateway.** The gateway
serves the web UI and the API from one origin; the relay is the only path that
must bypass it.

## 3. Timeouts and limits (these are not optional)

- **SSE / long-lived streams.** The room event stream is Server-Sent Events
  (`text/event-stream`) on `/v1/rooms/*/events`, held open for the session. The
  default 60s idle timeout **will cut it** and churn reconnects. Set the idle /
  read timeout to **≥ 30 minutes** on the routes to the gateway.
- **WebSocket on `/relay/`.** Must forward `Upgrade` / `Connection: upgrade`;
  set its idle timeout to **≥ 1 hour** (peer links are long-lived). Do not buffer.
- **Request body size.** Attachment uploads are up to **25 MiB**; allow at least
  **32 MB** request bodies (envelope overhead) on the gateway routes, and
  **256 MiB** on exactly `POST /v1/platform/artifacts` (super-admin staging of
  sidecar binaries; stream it, do not spool).
- **No response buffering** on `/v1/` and `/relay/` — buffering withholds SSE
  frames and the room appears frozen.

## 4. Health checks (for target groups / probes)

Every service exposes two probes:

| Probe | Path | Meaning |
| --- | --- | --- |
| Liveness | `GET /healthz` | 200 while the process is up (restart target). |
| Readiness | `GET /readyz` | 200 only when dependencies (DB) are healthy (traffic-shed target). |

- Gateway target group: liveness `/healthz`, readiness `/readyz` on port 8080.
- Relay target group: liveness `/healthz` on port 8085 (relay has no external
  dependency; `/readyz` is also 200).

## 5. Relay — please note (security)

`/relay/` carries the desktop file-sync stream. Its authorization token is sent
inside the stream in **cleartext**, so the relay entry **must be TLS**
(HTTPS/WSS). It must never be reachable over plain HTTP. The relay is also
currently a single replica (its pairing state is in-memory); if you scale it,
routing must be session-affine on the `/relay/stream/{sessionKey}` path segment,
or peers on different replicas will not pair — please check with us before
adding relay replicas.

## 6. Summary for the ticket

- 1 public domain, full-domain TLS, HTTP→HTTPS.
- Path routing: `/relay/` → bnbloc-relay:8085 (WebSocket); all else →
  bnbloc-gateway:8080.
- Gateway routes: 30-min idle timeout, 32 MB body (256 MiB on
  `/v1/platform/artifacts`), no response buffering.
- Relay route: WebSocket upgrade, 1-hour idle timeout, TLS required.
- Probes: liveness `/healthz`, readiness `/readyz`.
