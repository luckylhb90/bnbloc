# Organization sync

Organization sync maps the directory attributes an auth provider asserts at
sign-in onto bnbloc tenant memberships, org roles and default rooms, using
mapping rules a platform super-admin maintains. It lives in the gateway
(`internal/orgsync`, routes in `services/gateway/org_sync.go`).

## What the auth provider sends

The `bnbloc.auth-provider.contract/v1` ACS result
(`POST /v1/auth-provider/acs`, `Accept: application/json`) is
`internal/authprovider.Assertion`:

| Field | Required | Meaning |
| --- | --- | --- |
| `subject` | yes (else `email`) | The member id. |
| `email` | no | Email; its domain is matched by `emailDomain` rules. |
| `relay` | no | Return address. |
| `groups` | no | Directory groups (list of strings). |
| `department` | no | Department. |
| `displayName` | no | Display name (recorded with the attributes). |

Which IdP attributes become `groups` / `department` / `displayName` is the
auth provider's configuration; a provider that sends none still supports
`emailDomain` rules.

## Rules

`GET /v1/platform/org-sync/rules` → `{rules, updatedAtMs, updatedBy}`;
`PUT /v1/platform/org-sync/rules` `{rules}` replaces the set (super-admin;
stored in platform settings under `org_sync.rules`). A rule:

```json
{"match": {"group": "engineering"}, "tenant": "acme", "role": "member", "defaultRooms": ["lobby"]}
```

- `match` names exactly one of `group`, `department`, `emailDomain`
  (case-insensitive equality; a group matches when any asserted group is it).
- `role` is `member` (default) or `org_admin`. Rules can never grant
  `super_admin` — that platform role comes only from `BNBLOC_SUPER_ADMINS`;
  such a rule is refused (400), and a hand-edited setting carrying it is
  skipped.
- `tenant` must exist; every `defaultRooms` id must be a live room of that
  tenant (400 naming the rule otherwise). At most 200 rules, 50 rooms each.

**Order matters.** Rules are evaluated top to bottom: the first matching rule
for a tenant decides that tenant's role and default rooms (later rules for the
same tenant are ignored), and the first matching rule overall picks the tenant
the sign-in lands in. A user can be synced into several tenants.

With no rules saved, sync is off: nothing is changed.

## What a sign-in does

On every SSO sign-in (before the session scope is resolved) the gateway
applies the rules to the asserted attributes:

| Situation | Effect |
| --- | --- |
| A rule targets a tenant the user is not in | membership created with `source = sync` |
| The user holds a **sync** membership with another role | role set to the rule's |
| The user holds a **manual** membership in that tenant | left untouched (role included) |
| A **sync** membership no rule targets any more | removed, and the user's sign-ins that landed in that tenant are revoked |
| A rule's default room the user was never added to by sync | added as room member, once; leaving the room later is respected |

Manual memberships (admin-added, approved join requests, login
provisioning) are never changed or removed by sync. An org admin changing a
synced member's role or re-adding them takes the row over (it becomes
manual). The Members tab marks synced rows **sync**. A failed sync refuses the
sign-in (503): membership is an access decision.

Every change is written to the extension audit trail with kind `org-sync`
(name = user id; actions `add`, `role`, `remove`, `room`, `deprovision`,
and `rules` for a rule-set change).

## Preview, resync, deprovision

| Route (super-admin) | CLI | Meaning |
| --- | --- | --- |
| `POST /v1/platform/org-sync/preview` `{email, userId?, groups?, department?}` | `bnbloc org-sync preview --email e [--group g]... [--department d]` | Dry run against the saved rules and the user's current memberships: `{userId, tenant, targets, changes, dryRun}`. Writes nothing. |
| `POST /v1/platform/org-sync/resync` | `bnbloc org-sync resync` | Re-applies the current rules to every user a sign-in recorded attributes for (`org_sync_subjects`), so a rule change takes effect without waiting for each next sign-in. Run it after editing rules or on a schedule. |
| `POST /v1/platform/org-sync/deprovision` `{userIds}` (1–1000) | `bnbloc org-sync deprovision <id>... \| --file ids.txt` | Ends departed users' access: removes **every** tenant membership (manual included), revokes all their sign-ins, and forgets their recorded attributes. Room memberships stay as history. |

**Deprovisioning needs a directory feed.** A sign-in protocol only tells
bnbloc about users who sign in; it never reports that someone stopped. Have
the directory (HR system, IdP export, SCIM bridge) call the deprovision route
with an API token (`bnbloc api-tokens create --subject svc:directory-feed`,
then `BNBLOC_API_TOKEN=… bnbloc org-sync deprovision --file departed.txt`)
when people leave. The identity provider must also stop letting them sign in;
otherwise their next sign-in provisions them again.

The Platform console's **Org sync** tab edits the rules (ordering, tenant and
role pickers, default rooms), previews attributes, runs a resync, and
deprovisions users.

## Storage

- `tenant_members.source` (`manual` | `sync`).
- `org_sync_subjects`: last asserted attributes per user (resync input).
- `org_sync_default_rooms`: (user, room) pairs sync already added.

Migration `2026_10_b3_r11_org_sync`.
