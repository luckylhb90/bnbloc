# Chat safety limits: message length + per-subject rate limiting

Status: done
Commits: c351eda, 4c7a668

## Intent

Bound abuse and cost on the two unbounded write paths: chat message size, and
the rate of cost-bearing writes (sending chat drives agents; borrowing consumes
another owner's quota).

## Design

- **Length (realtime `handleChat`):** reject empty (400) and over-16KB text
  (413); cap the request body at 64KB via `MaxBytesReader` as defense-in-depth.
- **Rate (gateway):** a per-authenticated-subject token bucket (~2/sec, burst
  10) applied only to cost-bearing writes — `POST .../chat` and the
  `/shared-agents` borrow endpoints. Reads, SSE, and history paging are never
  limited. The trusted daemon subject (the bridge) is exempt. Over-limit → 429.
- **Client:** map 429 → "sending too fast" and 413 → "message too long" toasts;
  other failures keep the generic failed+retry affordance.

## Acceptance

empty → 400, >16KB → 413, sustained burst → 429 after the bucket drains;
normal browsing/history never throttled; daemon-forwarded traffic never
throttled. Verified: empty→400, 20KB→413, 15-message burst → 429s; rate-limiter
unit tests cover burst/refill, per-key isolation, and limited targets.
