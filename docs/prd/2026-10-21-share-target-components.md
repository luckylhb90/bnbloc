# Share targets: an item shared into a conversation of another app (2026-10-21)

Status: slice 1 built (the kind, the card owner, the gateway card build and
the signed call to the target, tests, docs); the client half was redesigned
after checking the real host client (see 2.4: a top-level page with a ticket,
not a frame), so the gateway's routes change from "send as the member's
session" to "start a session, then send by ticket" (slice 1b). Slices 2 and 3
open. Owner: platform. Builds on `docs/architecture/endpoint-components.md`
(the endpoint kind shape, the sealed credential, `GET /v1/component-settings`) and
`docs/prd/2026-10-19-component-settings.md`. The component that uses it first is
internal and recorded in the private overlay; this record names no product.

## 1. Why

Members work in another app's client (a chat or workspace app) and want to
send "this deliverable", "this room" or "this message" into a conversation
there, as themselves, with a card that opens the item. Today the only way out
is a copied link. The host app offers a card-delivery mechanism that needs
three things bnbloc has none of: a backend that holds the host app's
application secret and signs requests, a client half that runs only inside
the host app's embedded browser (its JS bridge), and a card that says what the
item is. The platform must not learn any host app: it keeps the capability
(an item becomes a card, the card goes through a signed component to a
conversation the member picked), and the host app's specifics ship as a
component.

## 2. What it is (the end state)

### 2.1 A share-target component

A package of kind `bnbloc.share-target.package/v1` (contract
`bnbloc.share-target.contract/v1`, runtime `endpoint`), an operator-run service
beside the gateway like a notifier or an auth provider: one sealed
credential created at install, platform and organization settings asked from
`GET /v1/component-settings`, calls held to `permissions.network`.

The contract is two calls from the gateway, signed with the credential
(`notify.Sign`, the notifier's scheme):

- `GET {baseUrl}/discover` answers `{name, contract}` (the install probe).
- `POST {baseUrl}/v1/share` receives one share and answers 2xx when the
  target accepted it for delivery, any other status when not:

  ```json
  {
    "deliveryId": "opaque, from the client half; the idempotency key",
    "dest": [{ "id": "…", "type": "user" | "group" }],
    "card": { "kind": "deliverable|room|message", "title": "…",
              "summary": "≤ 100 characters, plain text", "url": "…" },
    "sharer": { "subject": "<the member's bnbloc subject>" },
    "hostToken": "the host app's credential for the member, passed through"
  }
  ```

  The target formats the card for its host app, signs and calls the host
  app's API, and answers `{status:"ok"}` or a failure with a member-readable
  `reason`. The platform adds nothing host-specific.

### 2.2 The gateway builds the card; the member authorizes it

Two routes, because the part of the flow that needs the host app's bridge runs
in a page that cannot hold the member's session (2.4):

1. `POST /v1/my/share-targets/{name}/sessions` (a member route, spends
   `costBudget`) takes `{item}`. The gateway, as the member, authorizes the
   item and builds the card (below), and answers `{url}`: the share page's
   address, carrying a **share ticket** (signed by the gateway, short-lived:
   it names the member, their organization, the target and the card).
2. `POST /v1/share-targets/{ticket}/send` (a public route whose credential is
   the ticket, answering CORS for a page of opaque origin without
   credentials, spending a budget per ticket and per client) takes
   `{deliveryId, dest, hostToken}` and sends the card the ticket names.

The gateway, at step 1, as the member:

1. resolves the target through `extpipe.TenantDistribution` (rollout and
   tenant scope, like a notifier: a share target is the organization's
   infrastructure and has no per-member install, so it reaches a tenant by
   the tenant scope, which is also how a first rollout is staged);
2. authorizes the item as the caller (a deliverable or message only in a room
   the member may read, through the routes' own owners, never a second check);
3. builds the neutral card: title, a plain-text summary cut to 100 runes with
   the repo's one rune-safe cut, and the member link back (`url`), spelled by
   the one owner of item links;
4. at step 2 (the ticket names the card and the member): signs and sends
   `POST /v1/share`, audits (`s.auditComponent`: who, which target, which
   item kind, the outcome; never the card text, never `hostToken`), and
   returns the target's answer.

`hostToken` lives in memory for the one call: it is never logged, stored,
audited, put in an error or echoed back
(`TestShareTargetNeverLogsHostToken`).

`GET /v1/my/share-targets` lists the targets the member may use (name,
version), through the same owner. The member catalog
(`GET /v1/components`) keeps hiding endpoint-only kinds.

### 2.3 Items and their links

Slice 1 items: a room, a message in a room, a deliverable. Their member links
exist (`notify.RoomURL`, `notify.MessageURL`) or get a builder beside them
(`notify.ArtifactURL`, the `#/room/<id>?artifact=<id>[&v=n]` form the web
already speaks); the web sends ids and never builds a card URL. Works and
Library items have no addressable route yet and are a later slice that adds
one first. The card links to a member route, so opening it signs the member in
like any link (a signed-out member who comes through SSO must land on the
item: `loginReturn` remembers room routes too, not only share pages).

### 2.4 The client half is the component's page, opened at the top level

The host app's JS bridge is internal to that app and must not enter the public
bundle. Checked on a real client (private PRD "Verified on the test client"):
the bridge exists in the **top frame** of a page opened in the host client and
**not** in a sandboxed child frame, and it needs no registration of the page's
domain. So the client half is a **page the component serves**
(`GET {baseUrl}/v1/share/page`, one self-contained document of at most
512 KiB), which the gateway serves at `GET /v1/share-targets/{name}/pages/{ticket}/`
with `Content-Security-Policy: sandbox allow-scripts` (an opaque origin: it
cannot read the SPA's session or storage) and which the web **navigates to**,
not frames.

The page does the host app's three steps and calls bnbloc once:
choose conversations with the host's picker, register the delivery, read the
host's token, `POST /v1/share-targets/{ticket}/send` (2.2), then commit; it
shows the result and a way back to bnbloc. The platform defines only the
contract (the page gets its ticket in its own URL path and the gateway's
`send` address relative to itself); the host app's calls are the component's.
If the host's bridge is not there the page says so and links back, so the share
control needs no host-specific probe in the web.

### 2.5 Buttons

The SPA gets one share control (the deliverable toolbar and file menu, a
message's actions, the room header) shown only when `GET /v1/my/share-targets`
is non-empty. A click starts a session (2.2) and navigates to its page. Nothing
in the web names a host app.

## 3. Design review

| Lens | Answer |
| --- | --- |
| Actors | Sharer: a member who can read the item; the route authorizes as them, never as the target. Recipient: a person in another app, outside bnbloc: they see title, summary and link, and signing in is the link's rule. Admin: installs, scopes and rotates the credential. A viewer (read-only role) may share: it sends a card for an item they already read, as copying its link does, and it spends the cost budget; a borrower, visitor or agent shares nothing (a member session route, a visitor token never reaches it). |
| Time and lifecycle | A delivery id is the idempotency key: a retried send is the target's dedupe. The host token is per call. A share ticket lives 15 minutes (long enough to pick and confirm); a re-send inside it is the same card and the same delivery id. Uninstall deletes the credential. A card outlives the item: opening it after deletion or removal from the room shows the usual not-found/forbidden page. |
| Scale, cost | One call per share, `costBudget` per member, a 10 s target timeout. Frame document is size-capped (512 KiB) and cached by version. No table grows. |
| Failure and recovery | Target down, host API refuses, token expired: the member gets the target's `reason` or "the share target is unavailable" and the client half never commits (the host app's flow commits only after a success). A failure is a 502 with a reason, never a silent 200. No automatic retry (the member re-clicks; the delivery id dedupes). |
| Boundaries, privacy | A card carries only what the member could already read, 100 runes of summary, and a link; never file bytes. The host token crosses gateway to target once, over the guarded client (`permissions.network`), and nowhere else. The page is opaque-origin (sandboxed by CSP), so it cannot read the SPA's session or storage. |
| Abuse, untrusted input | A ticket names the card, so a page can only send what the member started a session for, never other content; it cannot widen the item, the target or the member. A leaked ticket lets someone send that one card as that member to a conversation they choose while it lives (they also need the host's token for that member, which only the member's client holds). Item text is untrusted: markdown stripped, control characters removed, cut by runes. `dest` ids are the target's to validate against its host (the host app refuses a conversation the member cannot post to); the gateway caps the count (`wire.MaxShareDest`) and the id length. A member cannot target an item they cannot read, nor another member's visibility. Spam is bounded by `costBudget`. |
| One owner | Item authorization: the routes' own owners. Card shape and summary cut: `internal/sharecard`. Item links: `internal/notify` link builders. Target distribution: `extpipe.TenantDistribution`. Signed calls: `notify.Sign` through the endpoint client. Credential: `internal/componentcred`. Architecture tests: no link spelled elsewhere, no `hostToken` in a log call. |
| Defaults, fallbacks | No target installed: no button, no route works (404). No fallback to a copied link by the platform. A target with missing settings answers its own error. |
| External contracts | The host app's API and bridge are the component's to verify (its own PRD); the platform's side is the contract of §2.1, held by a conformance fake in the tests. |
| Load on users, agents | One click and the host app's own picker; no new text for agents (agents are not actors here, no room-tool, no guidance). |
| Release, compatibility | A new kind: additive. Staged by tenant scope first; older gateways ignore the kind. Members mid-flow during an upgrade retry the share. |
| Observability | An audit row per share (outcome, target, item kind); gateway logs the target name and outcome class, never tokens or card text; the install probe and `components test` show the target is reachable. |
| AI capability | None: a share is a person's explicit act. A later assist could draft the summary; the card's summary is a plain field the member could edit. |
| Component or platform | The platform keeps the capability, the consent (opt-in rollout, tenant scope), the signing, the budget and the audit; the host app, its bridge, its card format and its secret are one component. Grep of the platform for a host app's name stays empty. |

**Pre-mortem.** It shipped and a month later it failed badly. The four
most likely reasons:

1. *A card leaked something the sharer could read but the recipients cannot.*
   A card holds a title, 100 runes of summary and a link: that is a known,
   small disclosure, and the member chooses the conversation. The design says
   so in the control's label and never includes file bytes or message
   attachments; a test holds the card to the fields of §2.1.
2. *The host token ended up in a log or an audit row.* It is a bearer for the
   member in another system. It is one struct field with no `String()`, kept
   out of every log and audit call, held by `TestShareTargetNeverLogsHostToken`.
3. *A share ticket ended up in a log, a referrer or the page's history.* It is
   a path credential. The page's route sets `Referrer-Policy: no-referrer` and
   `Cache-Control: no-store`, the gateway's access logs record the route
   pattern and not the path, the ticket is 15 minutes, and it names one card
   (`TestShareTicketNotLogged`, `TestSharePageHeaders`).
4. *A recipient clicked the link signed out and landed on the home page.* SSO
   drops hash routes today. `loginReturn` is extended to room routes in the
   same change, with a web test for the round trip.

## 4. Build order (slices of §2)

0. Verify on a real client (done 2026-10-21, private PRD): the bridge is in
   the top frame only, needs no registration, the picker and register calls
   work, the token carries the member's account. Decided §2.4.
1. The kind: `pkgmeta` (kind, contract, shape rows, `AsksForSettings`),
   `extreg`, `componentcred`, `internal/sharetarget` (installer, `Send`),
   `internal/sharecard`, the card build and the signed call (built, tests, docs).
1b. Sessions and tickets: `POST …/sessions`, the ticket kind, the page route
   (sandboxed, no-store), the ticket-authenticated `send` with CORS and budgets,
   the contract's `GET /v1/share/page`.
2. The component (private overlay) against a fake host API.
3. The web: the share control, the navigation to the page, `loginReturn` for room routes (done).
4. Later: works and Library items (each first gets a route), more item kinds.
