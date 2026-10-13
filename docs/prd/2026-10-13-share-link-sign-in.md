# Share links require sign-in by default (2026-10-13)

Status: accepted by the product owner (2026-10-13, final); **landed** 2026-10-13
(migration `2026_10_share_link_access`).

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
  `anonymousLinkAllowed` is the one reader of the switch. (Amended below:
  `signed_in` links and the outside-links switch; the one reader is now
  `store.Tenant.ShareAccessAllowed`, and the member's tenant policy carries
  the allowed modes instead of the switch.)
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
- Landed 2026-10-13: the store, room service and gateway (`share_access.go`,
  `POST /v1/share-tickets`, `POST /internal/v1/shares/view`), then the web
  (`lib/loginReturn.ts`, the share page, the share dialog, the
  organization settings). Decided while building:
  - The view count moved out of the resolve call: the gateway counts a
    view only after `shareAccess` passes, so a refused probe of an org
    link counts none.
  - `/v1/my/tenant-policy` also carries the organization's `name` (the
    share dialog's "People in <org>").
  - The default access lives in the store (`shareAccessOf`); the room
    service passes an absent access through.
  - An org admin keeps the revoke exemption only; an access change goes
    through the room's own reach (the creator, a room owner or admin).
  - The gateway's `orgMember` (content sharing) now reads membership
    through `memberRole`, the one tenant-membership read.

## Amendment: links anyone signed in can open (2026-10-13)

Status: accepted by the product owner (2026-10-13, final); **landed**
2026-10-13: recorded in c92385b, the store, room service, gateway and
settings in 8bc0d83 (migration `2026_10_share_link_signed_in`), the web
in 658d9e0. Decided while building: the policy is two switches with one
owner (an outside switch that implies the anonymous one reads better in
the settings than one three-way control, and keeps the existing column's
meaning, so the migration only adds); a link's active access in the share
dialog's list is a selector of the three modes.

### Decision

A third link access mode beside `org` (the default) and `anyone`:
**`signed_in`**: anyone who can sign in to this deployment may open the
link, without being a member of the link's organization. In an SSO-only
deployment that is anyone the company's identity provider authenticates,
including people in no organization yet or in a different one. The
creator picks it per link; `org` stays the default.

### Design

- **Store.** `artifact_shares.access` takes `signed_in` (the validation's
  one owner is `wire.ValidShareAccess` over `wire.ShareAccessModes`, read by
  both backends; the column widens from `VARCHAR(8)` to `VARCHAR(16)`,
  migration `2026_10_share_link_signed_in`).
- **Access owner.** `shareAccess` gains: `signed_in` needs a signed-in
  caller (a session at the ticket mint, a share ticket on every other
  route), of any organization; never a tenant membership. An anonymous
  caller is refused with 401 `share_sign_in_required`, exactly like an
  `org` link, so the share page sends the person to sign in and back. The
  share ticket is unchanged: bound to (share id, the link's organization,
  the subject); the data scope is the link's room and organization; the
  caller is the app's `member:<subject>` participant, never an owner (the
  room service's `appOwner` reads room membership), and gains nothing else
  in that organization.
- **A person in no organization.** Every session lands in an organization:
  sign-in resolves one (the IdP's, the issuer's, the person's latest
  membership, else the default) and, with auto-provisioning off, a person
  who belongs to none still signs in, as a viewer of that organization
  without a membership row. Such a session mints a ticket for a
  `signed_in` link like any other: the mode asks only for a verified
  session.
- **Re-checks.** Every use re-resolves the link (alive, still `signed_in`,
  allowed by the organization); there is no per-person check beyond the
  ticket's signature and expiry (at most 12 h, never past the link).
- **Organization policy: two switches, one owner.** The organization keeps
  `anonymousLinksDisabled` ("Allow links anyone can open (no sign-in)") and
  gains `outsideLinksDisabled` ("Allow links for people outside this
  organization"; `tenants.outside_links_disabled`, default allowed). Off,
  the outside switch disables both `signed_in` and `anyone` links; the
  anonymous switch disables `anyone` only. The one rule is
  `store.Tenant.ShareAccessAllowed(access)`: `org` always; `signed_in`
  unless outside links are off; `anyone` unless either switch is off. The
  room service asks it at create, at an access change (403) and at every
  resolution (a disabled mode resolves as dead, 404); the member's
  `GET /v1/my/tenant-policy` (`wire.TenantPolicy`) reports its answer as
  `shareAccess` (the modes allowed), so the web never re-decides it.
- **Web.** The share dialog's selector: "People in <org> (sign-in
  required)" (default), "Anyone signed in (any organization)", "Anyone with
  the link (no sign-in)", each with a short description, a mode the
  organization disallows unavailable; the list shows each link's mode and
  changes it. The share page treats `signed_in` like `org` (sign in if
  needed, then the ticket); the "only for members of" refusal comes only
  from an `org` link. The organization settings show both switches, the
  anonymous one under the outside one.

### Tests

- `signed_in` refuses an anonymous caller on every route (metadata,
  content, site, `~app`, visitor);
- a signed-in member of another organization opens it; a person with no
  membership anywhere opens it;
- inside an app that person is a participant, not an owner, and their
  ticket opens only that share;
- the outside switch turns `signed_in` and `anyone` off at once (create,
  change, existing links); the anonymous switch only `anyone`;
- the default stays `org`; `TestShareAccessSingleOwner` covers the new
  mode and switch;
- web: the selector's options and default, and the share page's sign-in
  redirect and ticket for a `signed_in` link.
