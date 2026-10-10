# Observability: metrics + DB-aware health

Status: done
Commits: (this change)

## Intent

The backend had only request logging — no metrics and a shallow `/healthz` that
always returned 200 regardless of database reachability. Make the services
operable.

## Design

Both live in the shared `internal/obs`, so every service gets them uniformly.

### Metrics (`/metrics`)

- Dependency-free Prometheus text exposition (bnbloc keeps a minimal dependency
  surface; no client library). A per-process `Metrics` collector records
  request counts and a latency histogram keyed by (method, route, status), plus
  named counters services can `Inc`/`Add` (e.g. rate-limit hits, borrow
  allow/deny).
- `routeLabel` collapses high-cardinality path segments — room id and member id
  (`/v1/rooms/:id/...`, `.../members/:userId`) and user id (`/v1/users/:id/...`)
  — so the label space stays bounded.
- `obs.Serve` now mounts `/metrics` and wraps the handler with metrics+logging
  middleware. `ServeWithMetrics` lets a service share the same registry for
  domain counters.

### Health (`/healthz`)

- `obs.ServeWithHealth(service, addr, handler, check)` returns 503 when the
  readiness check fails, 200 otherwise. DB-backed services (room, activity,
  shareagent, realtime, deviceauthority) pass `Backends.HealthCheck()`, which
  pings MySQL; in-memory mode always reports healthy.

## Acceptance

`/metrics` renders Prometheus text with bounded route labels; `/healthz` returns
503 when the DB check fails and 200 otherwise. Unit tests cover the exposition,
route collapsing, and the health 200/503 paths.
