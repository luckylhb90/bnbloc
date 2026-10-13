# Share links require sign-in by default (2026-10-13)

Status: accepted by the product owner (2026-10-13, final); building.

Amends `2026-10-09-artifact-shares-and-site-previews.md` (public share
links) and `2026-10-12-interactive-apps.md` (apps on a public link). Both
carry a "Link access" subsection pointing here.

## Decision

Every share link (apps, sites, files and documents alike) **requires
sign-in to the room's organization by default**. Anonymous access is
something the link's creator opts into, per link. A share page and the
sites origin never let an unauthenticated person in unless the link is
explicitly anonymous.

## Design

### 1. Link access mode

- Each link has `access: "org" | "anyone"` (`wire.ShareAccess`), default
  `org`, stored on `artifact_shares.access` (migration
  `2026_10_share_link_access`; existing rows become `org`, which is fine
  pre-launch: no backfill).
- The creator picks it at create (`POST …/shares {access}`) and can change
  it later (`PATCH …/shares/{shareId} {access}`: the people who may revoke
  the link, minus the org-admin exemption). A change takes effect on every
  open page, frame and change feed within the re-check windows below.
- The organization switch is renamed **"anonymous links disabled"**
  (`tenants.anonymous_links_disabled`, `anonymousLinksDisabled` in
  `/v1/tenant/settings` and `/v1/my/tenant-policy`; the old
  `public_shares_disabled` column is dropped by the same migration, no
  copy). It disables `anyone` links only: creating one, or switching one
  to `anyone`, is refused (403), and an existing `anyone` link resolves as
  dead (404) while it is on. `org` links stay allowed. The room service's
  `anonymousLinkAllowed` is the one reader of the switch.
- **One owner decides whether a request may open a link:**
  `shareAccess(share, caller)` (`services/gateway/share_access.go`). The
  room service still decides whether the link is alive (secret, expiry,
  revocation, the switch for `anyone` links, room and artifact present);
  `shareAccess` decides whether this caller may open a live link:
  - `anyone`: every caller;
  - `org`: an anonymous caller is refused (401 `share_sign_in_required`);
    a signed-in caller (a session at the ticket mint, a share ticket on
    every other route) must be a member of the link's organization
    (`memberRole`, the one tenant-membership read; a suspended
    organization is closed through `tenantClosed`), else 403
    `share_org_only` "This link is only for members of <org>". A ticket
    must be bound to this link and its organization.

### 2. Opening an `org` link (web, main origin)

1. `#/share/<token>` loads the share page, which asks
   `GET /v1/public/shares/<token>` without credentials. An `anyone` link
   answers as before. An `org` link answers 401 `share_sign_in_required`
   (no view is counted).
2. Without a session the page remembers the share (`lib/loginReturn.ts`,
   sessionStorage, the one login return path: none existed) and goes to the
   app's sign-in (the organization's SSO, or direct sign-in where offered).
   After any sign-in the app takes the remembered return and goes back to
   `#/share/<token>`. The return never travels through the IdP.
3. With a session the page mints a **share ticket**:
   `POST /v1/share-tickets {token}` (a session route; the token in the
   body, never the URL) → `{ticket, expiresAtMs}`. The caller must be a
   member of the link's organization; they need not be a member of the
   room. Not a member: 403 "This link is only for members of <org>".
4. The page then uses the ticket as the link's credential in place of the
   token: `GET /v1/public/shares/<ticket>`, `…/content`,
   `…/site/{path...}` and `…/site/~app/…`.

### 3. Share ticket

- `auth.KindShareTicket` (`share-ticket`), gateway-signed: Subject the
  signed-in member, Tenant the link's organization, ID the share id.
  Minted only by `handleShareTicket` after `shareAccess`; it lasts as long
  as a member site ticket (12 h; a running app keeps its frame URL) and
  never outlives the link.
- **It rides in the URL path** in the place of the token, exactly like a
  member site ticket: a frame (`siteUrl`), a download link and the app's
  relative `~app/` calls carry it without a header, and the sites origin
  still sees no session and no cookie (`sitePath`, `withSitesOrigin` and
  the log redaction already cover `/v1/public/shares/{…}`).
- A route's credential is resolved by one function (`openShare`): a
  `bsh_` token resolves the link through the room service as before; a
  share ticket resolves its share id (`POST /internal/v1/shares/resolve
  {shareId}`, gateway-only: the gateway vouches for its own signature);
  anything else spends the guess budget. Then `shareAccess` runs. A ticket
  therefore opens exactly its own link: it names no other share, and no
  other route takes it (a session route verifies a session bearer, a member
  site route a `site-frame` ticket).
- **The app participant** of an `org` link is `member:<subject>`: `_by`
  stays the per-app pseudonym, `me()` returns the member's own display
  name to them, owners see the real author as for any member. A member of
  the organization who is not a member of the room is a participant, never
  an owner (the room service's `appOwner` is unchanged). No visitor token
  is involved; `POST …/visitor` mints only for an anonymous caller of an
  `anyone` link.
- **Re-check windows** (unchanged): metadata, content and every `~app`
  call resolve the link and run `shareAccess` per call; site files reuse a
  passed check for 10 s; a change feed re-resolves every 30 s. Revoking,
  expiry, leaving the organization and switching `anyone` → `org` stop
  access within them.

### 4. `anyone` links

Today's behaviour: no sign-in, a visitor token per browser for an app, and
refused (404) while the organization disables anonymous links.

### 5. Web

- Share dialog: an access selector, "People in <org> (sign-in required)"
  (default) and "Anyone with the link (no sign-in)" with a short warning;
  the second is unavailable while the organization disables anonymous
  links. The list shows each link's access and lets it be changed.
- Organization: the switch reads "Allow links anyone can open (no
  sign-in)"; the Public links tab shows each link's access.
- `PublicSharePage` and `SharedApp` handle the sign-in redirect, the
  not-in-organization page and pass the ticketed `siteUrl` to the frame
  (no visitor host for an `org` link). The share page renders inside the
  auth provider; it sends a session only to the ticket mint.
- Generated wire types: `ShareAccess`, `ShareView.access`, `ShareTicket`,
  `ShareTicketRequest`, `ShareCreateRequest`, `ShareAccessUpdate`,
  `ShareRefusalCode`.

### 6. Desktop

Nothing changes: a member's site (`/v1/sites/{ticket}`) and the
`bnbloc-site` scheme are untouched; share links open in a browser.

## Tests

- an `org` link refuses an anonymous request on every route (metadata,
  content, site, `~app`, visitor);
- a signed-in member of another organization is refused;
- a member of the organization who is not a room member opens the link and
  uses its app as a participant, never as an owner;
- a share ticket is bound to its share (it opens no other share, room
  route or member site);
- the sites origin never accepts a session;
- `anyone` works and is refused while the organization disables it;
- switching `anyone` → `org` and revoking stop access;
- the default is `org`;
- the login return path brings the person back to the share;
- web node tests for the access selector default and the redirect URL.

## Status

- Recorded 2026-10-13, before building.
