# Copy a share link again (2026-10-16)

Status: accepted by the product owner (2026-10-16); **landed** 2026-10-16.

Amends `2026-10-13-short-share-links.md` ("the token is shown once, so no list
carries a link") and `2026-10-09-artifact-shares-and-site-previews.md` (only the
token's hash is stored).

## Problem

A link is shown once, at create. A creator who did not copy it, or who lost
it, can only create another link and revoke the first: everyone who already
has the first one is cut off. The share dialog lists the link (views, access,
expiry) but offers no way to get it back.

## Decision

The creator can copy their own active link again, from the share dialog's
list, for as long as it works.

- **Stored sealed, not in clear.** `artifact_shares.token_sealed` holds the
  token sealed with the deployment's secret store key (`internal/sealbox`,
  AES-256-GCM, `BNBLOC_SECRET_STORE_KEY`, which the room service already loads
  for app data), bound to the tenant and the share id (a value copied to
  another row does not open). `token_hash` stays: the link is still found and
  verified by its hash, and a database read alone yields no usable link.
  Without the key, or when sealing fails, the link is created as before and is
  not copyable (its `copyable` is false); the create never fails for it.
- **Only the creator reads it.** `GET /v1/rooms/{roomId}/artifacts/{artifactId}/shares/{shareId}/link`
  answers the link's creator, for an active link that has a sealed token,
  and nobody else: not a room owner or admin, not an org admin (they may
  revoke and change access, which `managedShare` allows, but a capability is
  not handed to a manager who did not create it). Anything else is 404 (not
  the creator, revoked, expired, or no sealed token): the route does not say
  which. It is a read of the caller's own creation, so it is not audited (an
  export of other people's data is; this is not).
- **One URL owner stays the gateway.** The room service answers
  `wire.ShareLink{token}`; the gateway's proxy replaces it with
  `{url}` (`sharelink.URL(publicOrigin, token)`) exactly as it does for the
  create answer, with `Cache-Control: no-store`. The web never builds a link.
- **The list says what can be copied.** `ShareView.copyable` is true only in
  the creator's own listing, for an active link with a sealed token; the
  org-admin list and everyone else's view never carry it. The create answer's
  `share.copyable` tells the dialog whether to say "you can copy it again".
- **Revoking removes it.** A revoked link's sealed token is cleared in the
  same statement as the revoke, so a dead link leaves nothing to recover. An
  expired link keeps its (dead) sealed token with its row; the route refuses it.

## Old links

Pre-launch: no backfill. Links created before this have no sealed token (only
their hash exists) and cannot be recovered. The dialog marks the creator's own
active links that cannot be copied ("The link can't be shown again"), so the
list explains itself.

## UI

The share dialog's list gets a "Copy link" button on the creator's own active
links with `copyable`; it fetches the link and copies it (the one `useCopy`
path), with a toast on failure. The create answer's hint reads "Copy it now"
when the link cannot be copied again and "You can copy it again from the list
below" when it can.

## Design review

| Lens | Answer |
|---|---|
| Actors | The creator reads; a room owner/admin or org admin who can revoke cannot read; a visitor and another member never do. The gateway signs the creator's identity; the room service compares it to `created_by` (never the request body). |
| Time and lifecycle | Revoke clears the token with the revoke; expiry makes the route 404; an access change leaves the token (the link is the same). A created link whose sealing failed is simply not copyable. |
| Scale and cost | One nullable `VARCHAR(128)` per link (a sealed 22-character token is 70 characters); no new table, no list change in cost. |
| Failure and recovery | A key missing at create: not copyable, create unaffected. A key rotated or missing later: `Open` fails, the route answers 404 and logs (never a 500 with the token). |
| Security | The token is a capability. Sealed under a key held outside the database, bound to tenant and share id, readable through one creator-only route that answers `no-store`. A database-only leak reveals no link. A leak of both the database and the key reveals the active links; those are the links a reader of the database could already see by hash and could not use, so the exposure window is a link's own lifetime, and revoke closes it. |
| Compatibility | Additive column (`2026_10_share_link_copy`), nullable, no default; an old room service ignores it, a new one treats NULL as not copyable. |
| External contracts | `wire.ShareView.copyable` (optional), `wire.ShareLink`, the new GET route. Nothing removed. |

## Tests

- create seals the token when a box is set and leaves it empty when not; the
  create answer's `share.copyable` follows;
- the creator reads the link; another member, a room admin and an org admin get
  404; a revoked and an expired link get 404;
- a sealed token moved to another share id does not open;
- the creator's list marks `copyable`, the org-admin list never does;
- revoke clears the sealed token;
- the gateway turns the route's answer into `{url}` on its public origin, with
  `no-store`;
- the web's list shows "Copy link" only where `copyable`, and the architecture
  test that no web code builds a share URL still passes.
