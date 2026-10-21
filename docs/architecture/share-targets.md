# Share targets

A share target is an installed endpoint component
(`bnbloc.share-target.package/v1`, contract `bnbloc.share-target.contract/v1`,
runtime `endpoint`) through which a member shares one item, as a card, into a
conversation of another app. The platform knows no such app: it decides what
the card says and who may send it, and the component speaks to the app.
Design record: `docs/prd/2026-10-21-share-target-components.md`.

It is an endpoint component like a notifier or an auth provider
([endpoint-components.md](endpoint-components.md)): one sealed credential
created at install and shown once, settings asked from
`GET /v1/component-settings`, every call held to `permissions.network`.
`bnbloc components rotate-secret <name> --kind share-target` rotates it.

## The contract

All calls come from the gateway, signed like a notifier's delivery:
`X-Bnbloc-Signature: t=<unix>,v1=hex(HMAC-SHA256(credential, "<t>." + body))`
(`notify.Sign`; the receiver checks it within 5 minutes).

| Call | Meaning |
| --- | --- |
| `GET {baseUrl}/discover` | `{name, contract}`; the install probe. |
| `POST {baseUrl}/v1/share` | One share (below). Any 2xx: accepted. Anything else: refused; the body may be `{"reason": "…"}`, which the member reads (cut to 100 characters). Redirects are never followed; the call times out at 10 s. |
| `GET {baseUrl}/v1/share/page` | The share page: one self-contained HTML document of at most 512 KiB, `Content-Type: text/html`. The member's client opens it (the host app's calls live in it). |

```json
{
  "deliveryId": "the client half's idempotency key",
  "dest": [{ "id": "…", "type": "user" }, { "id": "…", "type": "group" }],
  "card": { "kind": "room|message|deliverable", "title": "…",
            "summary": "plain text, at most 100 characters", "url": "member link" },
  "sharer": { "subject": "the member's bnbloc subject" },
  "hostToken": "the other app's credential for the member"
}
```

`hostToken` is a bearer in another system, passed through for this one call:
the component uses it and keeps nothing. The target validates `dest` against
its app.

## The flow

```
web ──POST /v1/my/share-targets/{name}/sessions {item}──▶ gateway (reads the item as the member, builds the card, signs a ticket)
web ──navigates to the page URL──────────────────────────▶ gateway serves the component's page (sandboxed), in the host client
page: host's conversation picker → register → GET ./session → host token → POST ./send {deliveryId, dest, hostToken} → commit
gateway ──signed POST /v1/share──▶ component ──▶ the host app
```

The host app's bridge exists only in the top frame of a page its client opens,
so the client half is a **page the web navigates to**, not a frame inside the
SPA. The page is sandboxed by CSP (an opaque origin: it cannot read the SPA's
session or storage) and holds no session: its credential is the **ticket** in
its address.

- `POST /v1/my/share-targets/{name}/sessions` (a member route, cost budget)
  checks the item, reads it as the member and builds the card, then issues the
  ticket (`auth.KindShareTarget`: the member, their organization, the target in
  `ID`, the card in `Data`; 15 minutes) and answers `{url}`.
- `GET /v1/share-targets/{name}/pages/{ticket}/` serves the component's page
  with `Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals`,
  `Referrer-Policy: no-referrer`, `Cache-Control: no-store`.
- `GET …/session` answers `wire.ShareSessionInfo`: the card, the `sendUrl` and the
  `backUrl` (the item's member link), so the page needs to know no path.
- `POST …/send` takes `wire.ShareSend` (`deliveryId`, `dest`, `hostToken`) and
  sends the card the ticket names. The three page routes answer CORS for any
  origin without credentials (the page's origin is opaque; the ticket is the
  capability) and spend the public budget; a ticket that is invalid, expired,
  of another kind or for another target is 401, and a target taken off in the
  meantime stops its tickets (404).

## What the gateway decides

- `GET /v1/my/share-targets` lists the installed targets distributed to the
  caller's organization (`extpipe.TenantDistribution`: an off rollout or a
  tenant outside the scope hides it).
- The card is built when the session starts, as the member (`shareCard`):
  - room: the room's name and description, through `memberRoom` (a room the
    member is not in, or a preview, is 404);
  - deliverable: its title, through `deliverableVersion`;
  - message: the message at `seq`, through `roomHistory` and
    `roomlog.DecodeRow` (the newest row below `seq+1` must be `seq` itself).
- The card's title and summary are untrusted text: `sharecard` makes them
  plain text (markdown links keep their text, markers and control characters
  go) within `wire.MaxShareTitleRunes` / `MaxShareSummaryRunes`. The link is
  the item's member link (`notify.RoomURL`, `notify.MessageURL`,
  `notify.ArtifactURL`), so opening it signs the viewer in and checks their
  access like any link.
- A send is audited `share` with the item's kind and the outcome
  (`auditComponent`: never the card text, never the token).

Owners: card shape and text rules `internal/sharecard`; the call and the
contract `internal/sharetarget`; item links `internal/notify`; distribution
`extpipe.TenantDistribution`; credential `internal/componentcred`. The
host token never appears in a log line, an audit row or a response
(`TestShareTargetNeverLogsHostToken`).

## Not built yet

Works and Library items (they have no route to link to yet).
