# Feature: room member role management

Status: done
Commits: (this change)

## Intent

An owner/admin should be able to promote/demote members (admin ↔ member ↔
visitor) from the members panel.

## Design

- The store's `AddMember` already upserts role (`ON DUPLICATE KEY UPDATE role`),
  so `POST /v1/rooms/{id}/members` with an existing user changes their role — no
  new endpoint needed.
- Hardened `handleAddMember` with two invariants (both fail-closed):
  - the **owner's role is immutable** here (change → 403); ownership changes go
    through transfer.
  - **owner cannot be granted** via add/update (role=owner → 400), so a room can
    never end up with two owners.
- web: `api.setMemberRole`; the members panel renders a role `<select>`
  (admin/member/visitor) for non-owner members when the caller is owner/admin,
  next to the existing remove button.

## Verification

Promote to admin → 200 (list reflects it); set role=owner → 400; change the
owner's role → 403; demote back to member → 200.
