# Acting tenant ignored by gateway-served handlers (2026-10-12)

Status: fixed (gateway); pending deploy.

## Repro

A platform super admin whose session tenant is `org-a` uses the
platform-wide acting-tenant switch (`X-Bnbloc-Acting-Tenant: default`, the
web sends it on every request; or `?tenantId=default`).

1. In a room of tenant `default` they ask the built-in Expert builder to
   create an expert. The room tools' `save_component` saves through the job
   grant (`POST /v1/content-components?job=&room=`). The audit row says the
   save succeeded, but the expert never appears in the member's Library,
   whichever acting tenant is chosen.
2. The reverse: a component created from the Library while acting as
   `default` is stored under `org-a`.

## Root cause

The "effective tenant" rule (session tenant; a super admin's acting-tenant
header or `?tenantId=`; the room's own tenant on a room-scoped route) was
implemented in two places only: the reverse-proxy path
(`handleProxy`, `services/gateway/service.go`) and `orgAdmin`
(`services/gateway/mcp_tools.go`), as two copies. Every other handler the
gateway serves itself authenticates with `s.authenticate(r)` and reads
`claims.Tenant`, which is always the SESSION tenant.

- `contentActing` acts with `Tenant = job.TenantID` (the room's tenant,
  `default`): correct, the save lands in `default`.
- The member's own content routes (`contentCaller` → `authenticate` →
  `claims.Tenant`) list and create in `org-a`, ignoring the acting tenant.
  So the Library lists `org-a` personal content while the save landed in
  `default`, and a UI-created component lands in `org-a`.

The room-tenant half of the rule was copied as well (`contentJob`,
`contentBundleEntitled`, `contentUsesChanged`, `handleSiteTicket`,
`shareRecipients`), and so was the room-reach check
(`claims.HasRoom(room) || subjectInRoom(...)`).

Affected gateway-served handlers (all read `claims.Tenant` from
`authenticate`): the content routes (`content_components.go`,
`content_prompts.go`, `content_sharing.go`, `content_teams.go`,
`content_drafts.go`), component catalog and opt-in install
(`components.go`, `core_packages.go`, `/v1/cores`), local-agent run and
targets (`localagent.go`), artifact download entitlement (`artifacts.go`),
site tickets (`sites.go`), previewer frame tickets (`previewers.go`), and
the super-admin routes that pick a tenant (`component_notifier.go` alert and
notifier test, `api_tokens.go` default tenant hint).

## Fix

One rule, one file: `services/gateway/tenant_scope.go`.

- `actingTenant(r, claims)`: the session tenant, overridden for a platform
  super admin by the acting-tenant header and then `?tenantId=` (which
  wins); ignored for everyone else. It is applied at authentication, in
  both entry points: `authenticate` (every handler the gateway serves
  itself) and `proxyClaims` (the reverse proxy). A handler's
  `claims.Tenant` is therefore always the effective tenant; no handler
  re-derives it.
- `roomClaims(claims, room)`: the room-reach check (baked token rooms, else
  the live membership lookup; extracted as `roomReachable`) plus the room's
  own tenant (`roomTenantOr`). A room-scoped request acts in the room's
  tenant, which wins over the acting tenant, exactly as the proxy did.
- Deleted copies: the proxy's inline header/`?tenantId=` block and its
  room-tenant block, `orgAdmin`'s header/`?tenantId=` block, and the
  room-tenant lookups in `contentJob`, `contentBundleEntitled`,
  `contentUsesChanged`, `handleSiteTicket` and `shareRecipients`.

Per-handler decisions:

- Content routes: `contentCaller` acts in the effective tenant; with
  `?room=` it requires the caller's reach of the room (403 otherwise, like
  the proxy) and acts in the room's tenant, consistent with the job grant
  (`contentActing` keeps `job.TenantID`, which is the room's). Room content
  uses (`PUT/DELETE /v1/content-uses` with scope `room`/`room-expert`) act
  in the room's tenant through `contentUseScope`, so the use row and the
  `content_uses_changed` event carry it.
- Component catalog, `/v1/cores`, the core artifact route, local-agent
  targets/run, artifact entitlement, previewer frame tickets: the acting
  tenant through `authenticate` (a super admin acting as B sees B's
  catalog, as the proxied routes already did).
- Local-agent run: `roomBackendCall` now sends the signed tenant, so the
  adopted room is created in the effective tenant (it was sent without one).
- Opt-in install/uninstall signal `components_changed` to every desktop of
  the member (tenant ""): the opt-in row is the member's in every
  organization and the desktop's stream is in its own session tenant.
- Super-admin routes that pick a tenant (platform alert, notifier test,
  API token default tenant hint) use the acting tenant (docs updated).
- Org admin routes (`orgAdmin`, `contentAdmin`) unchanged in behavior, now
  through `authenticate`.

## Verification

`services/gateway/tenant_scope_test.go`: a super admin acting as B creates,
lists and versions content in B (header; `?tenantId=` wins); a job-grant
save in a B room shows in the requester's Library acting as B and not in
their session tenant, and reads as theirs from the room; a member's header
and `?tenantId=` are ignored; room content uses act in the room's tenant and
a room out of reach is a 403; the proxy scopes a tenant path to the acting
tenant, a room path to the room's tenant, and never forwards the header.
The new tests fail against the old rule.
