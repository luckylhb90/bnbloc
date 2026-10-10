# Agent drivability follows the user's bridge, not the session tenant

Date: 2026-09-28
Status: implemented

## Principle (user-stated design intent)

Whether an agent can be driven in a room is a property of **the user and their
bridge**: the bridge of a room member subscribes to that room, advertises the
member's agents on its SSE connection, and claims jobs for its owner — from the
desktop app or a browser alike. The tenant a login session happens to land in
must never gate this. At the same time the multi-tenant isolation invariants
stand: two tenants may each have a room with the same id, and one tenant's UI
listings must not show another tenant's rooms.

## Defect

Three seams keyed agent drivability to the caller's session tenant:

1. `RoomsForUser` (room service) is tenant-scoped, and the BRIDGE used it to
   discover which rooms to subscribe to. A member whose login landed in tenant
   A while their room lived in tenant B had a bridge that never discovered the
   room → never subscribed → never advertised → the room's agents showed
   offline for everyone (including the owner's own browser sessions), and
   jobs timed out with "no bridge online".
2. `ListBridgeAgents` (activity) filtered advertisements by tenant and joined
   heartbeats on (bridge, tenant). The same bridge re-logging in another
   tenant wrote its heartbeat under a different tenant row, so the join could
   starve.
3. The bridge's owner token is minted/switched per tenant (`gatewayAuth`),
   compounding 1.

The claim path was already correct (cross-tenant by design, owner-authorized);
the SSE `?agents=` advertisement path was already room-anchored.

## Change

- **Room store**: new `RoomsForUserAllTenants` — the bridge discovery shape.
  `RoomsForUser` stays tenant-scoped for UI listings and login stamping.
  Room service `GET /v1/users/{id}/rooms?allTenants=1` serves it (gateway's
  reverse proxy passes the query through unchanged; the caller may only query
  their own rooms, unchanged).
- **Bridge**: `discoverRooms` uses `allTenants=1`.
- **Activity store**: `ListBridgeAgents` no longer tenant-filters; liveness
  joins on `bridge_id` only (the PK; `bridge_id` and `owner_user_id` are
  globally unique).
- **Unchanged on purpose**: `GetRoom`/`MemberRole`/`Members` remain
  tenant-scoped (room ids are NOT globally unique across tenants — the
  isolation tests pin this). Cross-tenant room access flows through the
  room-anchored probes (`IsMemberAnyTenant`, `RoomTenant`) that the gateway's
  room-scope fallback and realtime's membership client already use.

## Verification

- `TestRoomsForUserAllTenantsIsTheBridgeDiscoveryShape` pins the split:
  tenant-scoped listings stay scoped; bridge discovery from a tenant-A context
  finds the tenant-B room; other users' rooms are never included.
  `TestRoomIsolationAcrossTenants` (pre-existing) still passes.
- Live end-to-end on the local stack: a room created under tenant `default`
  and a bridge running with `BNBLOC_BRIDGE_TENANT=org-example-com` (an owner
  with no membership there) — the bridge discovered the room via
  `allTenants=1`, subscribed, advertised, and the room roster (third-party
  view) showed the full agent set.
- Full `go test ./...` green.

## Commits

(see git log: room store + room service + bridge + activity store)


## Correction (same day)

The first cut overreached: RoomsForUserDetailed (the UI "my rooms" listing)
was made cross-tenant too, so switching the acting tenant still showed other
tenants' rooms — a tenant-isolation regression, not part of the requested
change. Corrected boundary:

- UI room listings (RoomsForUser, RoomsForUserDetailed): tenant-scoped.
  Switching the acting tenant switches the visible workspace.
- Bridge room discovery (allTenants=1 → RoomsForUserAllTenants): cross-tenant;
  no UI call ever passes the flag.
- Bridge agent advertisements (ListBridgeAgents): keyed on the globally
  unique bridge_id/owner_user_id.

Pinned by TestRoomsForUserDetailedStaysTenantScoped alongside the isolation
suite.
