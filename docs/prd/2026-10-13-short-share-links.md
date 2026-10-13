# Short share links (2026-10-13)

Status: accepted by the product owner (2026-10-13, final); being built.

Amends `2026-10-09-artifact-shares-and-site-previews.md` (the token) and
`2026-10-13-share-link-sign-in.md` (the share page it opens).

## Problem

A share link reads `https://<host>/#/share/bsh_<32 hex id>_<43 base64url
secret>`: about 100 characters, too long to paste into a chat line or read
out.

## Decision

A link is `https://<host>/s/<token>`, about 50 characters under a typical
host.

- **Token:** 22 base62 characters from a CSPRNG (131 bits), nothing else:
  no id part, no prefix. The room service stores only its sha256
  (`artifact_shares.token_hash`, already unique: `uq_artifact_shares_token`)
  and finds the link by that hash. No migration.
- **One owner** mints, checks and spells it: `internal/sharelink`
  (`New`, `Valid`, `Hash`, `Path`, `URL`, `PagePath`). The share id stays
  the row's primary key (share tickets, the API, the view count name it),
  minted by `ids.New("")` (32 hex, the column's width).
- **Path:** `GET /s/{token}` is a gateway route-table row (`open`, the
  token as its credential). A well-formed token answers 302 to the SPA's
  share page, `/#/share/<token>`, which works as before (the sign-in return
  path keeps that hash, the share ticket replaces the token in the API
  calls). It resolves nothing itself, so a redirect reveals nothing and
  needs no guess budget; a malformed one is a 404. With a sites origin the
  route is served on the main origin only (it is not a site path). The
  token is redacted from request logs like every capability path.
  Redirect rather than serving the SPA at `/s/…`: the bundle's assets are
  relative to the document, so the SPA is only ever served at the root.
- **The link's one URL owner** is the gateway: the share create response
  (`wire.ShareCreated`) carries `url`, `sharelink.URL(publicOrigin, token)`,
  filled in by the gateway's proxy on the way out; the web shows and copies
  it and never builds a share URL. The token is shown once, so no list
  (share dialog, Share links tab, Library My apps) carries a link; nothing
  else (notifications, room tools) hands one out.

## Old links

Pre-launch: no compatibility. A `bsh_…` link (QA) is no longer a token: the
gateway answers it 404 `share not found` without a backend call (spending
the guess budget like any miss), and the share page shows "This link is no
longer available". Never a 500.

## Tests

- token round trip (minted tokens are valid, unique, hash stably);
- a link is at most a fixed length under a typical host;
- `/s/<token>` opens the share for every access mode (anyone, signed_in,
  org), on the main origin with a sites origin configured;
- an unknown or old-format token: `/s/…` and the share API answer a clean
  404, never a 500, and the share page shows its gone state;
- an architecture test: no code outside `internal/sharelink` spells the
  share link's path or page route, and the web builds no share URL.
