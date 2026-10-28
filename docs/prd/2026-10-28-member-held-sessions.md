# A system's session kept on the member's computer (2026-10-28)

Status: proposed 2026-10-28 (option A of three; the owner chose it).

## 1. Why

A connector that sends from members' computers
(docs/prd/2026-10-25-member-sends.md) reaches a system the component's own
network does not. Its credential still comes from the component: a key, a
token, or a provider's sign-in. Some systems sign people in with a web
flow whose last step must reach the system itself, and answers with a
session cookie (`Set-Cookie`), not with something the component can carry:

- the component's network cannot make that last step (the system is out of
  its reach, which is why the connector sends from the member);
- the member's computer can, but the bridge drops `Set-Cookie` from every
  answer it sends (a command must never see a credential), and reports
  only the status, Location and body to the component.

Seen 2026-10-28: an internal CI system signs people in with the
organization's code host (OAuth). The component reaches the code host and
gets the authorization code, but the code's exchange happens on the CI
system, which only members' computers reach.

Options weighed (the owner chose A):

- **A. The session stays on the member's computer.** The bridge keeps the
  cookies the system sets on a send the component marks as a sign-in, per
  member, component and host, and adds them to that component's later
  sends to that host. The component never holds the session.
- B. The bridge reports the cookies to the component, which keeps and
  hands them back like any credential. Smaller, but the session lives in
  two places and crosses the network twice per renewal.
- C. Open the component's network to the system (an operator's request).
  No code; depends on another team per system.

## 2. What it is

Contract (`component`, additive; docs/architecture/service-components.md):

- `EgressSend.Session` (a name the component chooses, e.g. per member or
  digital worker): the bridge sends the request with the cookies its host
  set on this component's earlier sends naming that session, and keeps the
  cookies the answer sets. A send without it neither reads nor writes one.
- `EgressSend.Steps` (at most `MaxEgressSteps`, `GET`/`HEAD` of the
  request's own host, https): a sign-in's requests the bridge sends first,
  in order, with the session, redirects not followed; their answers come
  back in `EgressReport.Steps` (status, Location, the body's first 64 KiB).
  `StepsOnly`: the request itself is not sent yet, the component is asked
  again with the steps' report and then answers the real `Send` (at most
  `MaxEgressSignIns` per request, not counted among `MaxEgressSends`).
- `EgressSend.EndSession`: the session is dropped first (a fresh sign-in).

The bridge (`roomtools`):

1. keeps sessions in memory only, one cookie jar per (component, host,
   session name), at most 64 jars (the oldest is dropped); a jar is used
   only for its own host, so a cookie set for a parent domain never
   reaches another host; a restart forgets them (the system then refuses
   the next request; the component signs in again);
2. never gives the command a cookie (`Set-Cookie` stays removed from the
   answer relayed; the command's own `Cookie` is in `Remove`), never sends
   a session's cookies on a send that does not name it;
3. uses the same sessions for sends outside a turn (`SendFromHere`, which
   now knows the send's component).

The component (connections) and its providers (`bnbloc-connections/provider`):

- `Answer.Session`: the request carries the member's held session of that
  name, with `Answer.Header` beside it (a CSRF token).
- `Answer.SignIn`: steps the member's computer must send with the session
  first (a code's exchange on the system); connections answers
  `Send{Steps, StepsOnly, EndSession}` and, with the report, asks the
  provider's `authorize` again with `Call.SignedIn` (the steps' answers);
  the provider answers the headers (read from a step's body). Cached like
  any credential; the system refusing it (`expiredWhen`) signs in again,
  once.
- A connector whose provider uses sessions must send from members'
  computers.

A provider for the CI system (in the organization's own components): the
code host's sign-in and its OAuth authorize on the server (both reach the
code host) up to the CI system's code, then `SignIn: [GET <ci>/authorize?code=…,
GET <ci>/web-config.js]` for the member's computer; the second answer's
CSRF token becomes a header.

## 3. Decisions

1. Sessions are kept in memory only (a desktop restart signs in again,
   invisibly: one SSO round trip). Proposed; to confirm.
2. A session's name is the component's choice; connections names it per
   principal (member or digital worker), so two owners of one shared
   bridge never share one.

## 4. Placement

| Part | Where | Why |
|---|---|---|
| Keeping a system's cookies on the member's computer, sending them only on its component's marked sends | platform (bridge egress, the contract) | confinement: only the platform terminates the command's TLS and holds what it must never see |
| When to sign in, which requests make the session, how to tell it took | component (connections, providers) | a system's sign-in is the component's |
| The CI system's sign-in through the code host | component (the organization's provider) | per system |

Name test: no system, cookie name or host in the platform. Fork: a
component that wants its sessions elsewhere keeps doing B itself. Replace:
any egress component may mark sends with `Session`.

## 5. Design review

| Lens | Answer |
|---|---|
| 1 Actors | The command never sees a cookie. The component decides when a session is used and never reads it. Another component never gets it (key). A borrowed drive's request carries no member session of the lender (the bridge's owner is the member; a borrowed drive is the owner's bridge: connections already never sends a person's credentials on a borrowed drive). |
| 2 Time | A session lives as long as its cookies or the bridge process; ended by `EndSession`, disconnect, or the system's own expiry (the component sees `Session: false` or a refusal and signs in again). |
| 3 Scale | Bounded in memory (64 × 50 cookies). One sign-in per member and system per expiry. |
| 4 Failure | The system refuses the session: the component's refusal handling (expiredWhen) ends it and signs in again, once. The bridge restarted: `Session: false` on the first send, sign in again. |
| 5 Privacy | The session never leaves the member's computer; logs carry the host and the number of cookies, never values. |
| 6 Abuse | A component can only attach a session to its own sends to its claimed hosts; a cookie's Domain is never wider than the request's host; redirects are never followed outside the sign-in's hosts. A malicious system can set cookies only for itself. |
| 7 One owner | `sendHere` stays the one sender; the session store is one type in `roomtools`. |
| 8 Defaults | No session unless a send names one. |
| 9 External contracts | RFC 6265 cookies (Go's `net/http/cookiejar` semantics, with a public-suffix list: none, so a cookie is host-only unless its Domain is the host or a parent within it). |
| 10 Load on agents | None. |
| 11 Release | Contract first (additive), then the bridge (desktop release), then connections, then the provider. An older bridge ignores `Session`: the sign-in's report says no session, the component says the desktop must be updated. |
| 12 Observability | Bridge log: session used/updated (component, host, cookie count). Component audit as today. |
| 13 AI capability | Not applicable. |
| 14 Component or platform | Section 4. |

Pre-mortem: *a command reads the session through the proxy* — `Set-Cookie`
is removed from every answer and the request's own `Cookie` is in
`Remove`; *a component steals another's session* — keyed by component;
*sign-in loops* — at most one sign-in per refused request, as today.
