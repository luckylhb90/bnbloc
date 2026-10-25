# A request sent from a member's computer, outside a turn (2026-10-25)

Status: building.

## 1. Why

A service component's connector may send its requests from the member's
computer (`Send`, docs/architecture/service-components.md): a system the
component's network does not reach (an exchange behind the deployment's
egress allowlist, an internal system the members' computers do reach).
Today that path exists only inside an agent's turn: the bridge sends a
request only when an agent's command made it.

So whatever a person does on a component's page cannot go out from their
computer. The connections component's **Try it** (the connector's test
read with the admin's own connection) and the check after a member
connects are skipped for exactly the connectors where they matter most: a
wrong signature (OKX `50113`) shows up only when an agent hits it, and the
AI review never sees what the system said.

## 2. What it is

A platform primitive for service components that declare `egress`:
**send one request from this member's own computer, now**, without an
agent.

1. **Consent.** A member's action on a component's page (a `POST` page
   call, a card's call) carries `Member.Act`: a one-time grant, signed by
   the platform, for that member, organization and component, valid two
   minutes. A view (`GET`) carries none. The component cannot make a
   member's computer send anything without one.
2. **Ask.** The component posts `ComponentSendPath`
   (`ServiceSendRequest{act, method, url}`, signed): `GET` or `HEAD` of an
   https URL, no body. The platform checks the grant (once: a second
   send with it is refused), and that one of the member's own bridges is
   live (else `409`: "open the bnbloc desktop app"), queues the send and
   answers its id.
3. **Take.** The member's own bridge takes it with its next claim (the
   claim it already makes every two seconds; a bridge says it takes
   sends, so an older one never swallows one). A shared bridge serving
   other owners never takes a member's send. Untaken after two minutes,
   it is dropped.
4. **Send.** The bridge runs the egress protocol it runs for a turn,
   against the component's `ServiceSendPrefix + "egress"`, the send
   attested in `SendHeader` (`component.Send{org, member, id, origin}`),
   through the gateway's `SendProxyPath + {id}`: the component answers
   `Send` (the member's credentials for this one request), the bridge
   sends it from this computer and reports the answer, the component
   answers `Accept` (or a second `Send`). The platform forces the
   request's method and URL to the queued ones. Nothing goes to an agent
   or a room.
5. **Result.** The component learns it from the report, as in a turn.
   The page shows "trying from your computer…" until then; a send no
   bridge reported within two minutes says so.

Audit: the platform records the send (`send`, component, member, method,
host and path); the component records its own outcome.

## 3. Connections uses it

- **Try it** on a connector that sends from members' computers goes
  through the admin's own bridge; the result is the try the review is
  given (docs/prd/2026-10-25-ai-checks-its-work.md).
- **After a member connects**, the test runs from their computer; their
  Connections page shows "it answers" or what the system said.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | The member acts on a page; the component asks; the platform checks the grant and queues; the member's own bridge sends. |
| Abuse and untrusted input | Without a member's action there is no grant; one grant is one send; only `GET`/`HEAD`; the method and URL are the platform's, not the bridge's; only the member's own bridge, never a shared one. A component already reaches these hosts in the member's turns. |
| Boundaries and privacy | Credentials reach the bridge for one request, in memory, as in a turn; the answer goes to the component only, never an agent. |
| Failure and recovery | No live bridge: refused at once with what to do. Bridge gone mid-send: the component's pending try times out with a line. A gateway or activity restart loses nothing durable (the queue is in MySQL). |
| Scale and cost | No new poll: the claim carries sends. A row per send, deleted when taken or after two minutes. |
| Release and compatibility | Additive. An older bridge does not say it takes sends and is never given one; the page then says the desktop app did not pick it up (update it). |
| One owner | The egress exchange loop in the bridge is one function for turns and sends; the grant is one signer kind; the queue is one store. |
| Load on agents | None. |
| Component or platform | Platform: consent (the grant), the queue, attestation, the proxy, audit. Component: what to send and what the answer means. |

Pre-mortem: *a component drives members' computers into the intranet* —
it needs a member's click per request, only reads, audited; *a stale
desktop app swallows sends* — it must say it takes them; *a send waits
forever* — two minutes untaken, five minutes held.

## 5. Placement

| Part | Where |
|---|---|
| Grant, queue, proxy, attestation, audit | platform (gateway, activity, store) |
| Sending from the computer | the bridge (the member's) |
| Try it, the check after connecting, what an answer means | the connections component |
