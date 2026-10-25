# Server egress

Where the platform's own server-side requests may go. Today that is the
documentation pages an AI assist reads from a link a person gives (the
`pages` context, docs/architecture/ai-assist.md); other server-side
fetches are to read the same policy when they move onto it.

It is not the agents' network. An agent's sandbox has its own egress
(docs/architecture/sandbox-protocol.md, per machine and space), and a
component's requests go through its own `permissions.network`
(`internal/extgov`). The rule here has the same shape as a sandbox's
`public` mode with `egressHosts`.

## The policy

A platform setting (`server.egress`), set by a super-admin in the
platform console (**Network**), `GET`/`PUT /v1/platform/server-egress`
(`wire.ServerEgress`), audited (`network`, `egress-set`):

| Mode | Unlisted hosts | Listed hosts |
|---|---|---|
| `public` (default) | public addresses only | any address but link-local and metadata ones |
| `allowlist` | never sent, never looked up | any address but link-local and metadata ones |

- **Public addresses only** keeps a link from reaching an internal
  service: a name that resolves to a private, loopback, shared or
  metadata address is refused at connect time (the resolved address is
  checked, so a name that rebinds is refused too).
- **List a host** when an external site answers on a private address in
  your network, as it does behind a DNS egress allowlist that sends
  allowed names to a private egress proxy. The platform cannot tell that
  proxy from an internal service; the list says which names are external.
- **`allowlist`** where an unlisted name must not even be looked up or
  dialled: a network whose blocked names resolve to a monitored address.
  An empty list there reaches nothing.

Hosts are exact lower-case names (no scheme, port, path or wildcard), at
most 200. Nothing to configure on a deployment with ordinary internet
egress.

## Code

`internal/egress` is the one owner: `Load`, `Save`, `Normalize` and
`Transport` (each request and redirect hop checked by host, then by the
dialled address). `assist.PagesClient` builds on it; the gateway reads the
policy per run, and an unreadable policy reaches nothing.

## Placement

| Part | Where |
|---|---|
| The policy, its enforcement, its audit | platform (network governance) |
| The console card | platform console |
| Which hosts a deployment lists | the operator's data |
