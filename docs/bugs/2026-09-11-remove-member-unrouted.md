# Bug: owner cannot remove a room member (unrouted handler)

Status: done
Commits: (this change)

## Repro / gap

An owner has no way to remove (kick) a member. `handleRemoveMember` existed in
the room service but was **never registered to a route**, and there was no
client action — dead, half-built code.

## Fix

- Register `DELETE /v1/rooms/{roomId}/members/{userId}` (room service).
- Harden the handler: caller must be owner/admin (`CanInvite`); refuse removing
  yourself (use leave) and refuse removing the room owner (transfer ownership
  first).
- gateway routes `DELETE .../members/{userId}` to the room backend (already
  covered by the `/v1/rooms` room-service prefix).
- web: `api.removeMember`; the members panel shows a remove button on other
  non-owner members when the caller is owner/admin.

## Verification

Non-owner → 403; owner self-remove → 400; owner removes a member → 200 and the
list updates; owner cannot remove the owner.
