# Agents vanish from rooms of a member's other tenants (bridge flapping)

Status: fixed

## Repro

Member in two tenants (`default`, `org-example-com`); the desktop bridge's
session is in `org-example-com`. In the browser, a room of `default` ("Test")
shows "No agents available yet" / "No local agent" although the agent had
replied there minutes earlier. `GET /v1/rooms/Test/agents` → `[]`, while a room
of `org-example-com` lists all 8 agents.

## Root cause

The bridge serves every room its owner belongs to: discovery lists rooms with
`allTenants=1`, and room streams/members/channels answer across tenants. Its
live-sync loop, however, reads `GET /v1/rooms/{id}`, which the room service
scopes to the session's tenant, so a room of another tenant answers
`404 store: room not found`. The H6 change (2026-10-08, "stale room 404
loop") treated any 404/403 there as "room gone" and dropped the room —
cancelling its stream and so withdrawing its roster entry — for a one-minute
cooldown, after which discovery re-added it and the cycle repeated. Rooms of
other tenants therefore flapped: agents were advertised only part of the
time, and a browser opening the room in a "down" window saw none.

## Fix

Whether a room still exists is decided by the authoritative listing and by
the room stream (`watchRoom` still drops on a stream 404/403). A 404/403 from
the tenant-scoped detail lookup only means there is no sync plan: live sync
stays off for that room and the room is kept. Regression test:
`TestBridgeKeepsRoomWhoseDetailIsOutOfTenant`. Shipped in desktop 0.1.18
(the bridge runs inside the desktop app).

## Follow-up

Live sync (`syncMode: mutagen`) cannot read the sync plan of a room in
another tenant; the room detail should be readable by its members regardless
of the session tenant (like members/channels/events) — tracked separately.
