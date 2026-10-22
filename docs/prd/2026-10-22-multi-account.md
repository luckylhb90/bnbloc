# Several accounts in one app, and test accounts to switch between (2026-10-22)

Status: designed (2026-10-22). Slice 1 (web) is built with this record; slices
2 and 3 are the desktop and are designed here, not built. Owner: the web's
session owner (`web/src/lib/auth.tsx`, `lib/accounts.ts`), the platform console
(`web/src/views/org/`), and for the desktop the shell's member scope
(`apps/desktop/src-tauri/src/bridge.rs`, `member_dir.rs`, `internal_ext.rs`).

A person can hold several accounts in the app and switch between them from the
account menu, without signing out. A super admin gets a "Test accounts" page
that creates accounts with a chosen organization role (viewer, member,
org admin) in one step, so the roles of a room or an organization can be tried
from the page instead of with a script. The owner asked for both ("a few
accounts with different permissions that I can switch in the page", then "a
multi-account login").

## 1. Why

- QA 2026-10-22: checking that a non-member reads a shared room, a viewer is
  read-only, or a private room needs a code took one test identity
  (`svc:e2e-tester`, a member) and could not show the other roles or a second
  person. The only way to be someone else was to sign out and sign in again.
- A test account is a `svc:<name>` identity whose organization role comes from
  its tenant membership; only a super admin can issue its API token
  (`POST /v1/platform/api-tokens`, shown once), and its role is set with the
  org members API (`POST /v1/tenant/members`). Both exist; nothing ties them
  together, and nothing keeps a session of each at hand.

Checked against the code: the web holds one session record (`bnbloc.session`:
bearer, refresh token, subject, tenant, role), renewed from its refresh token
(`renewSession`); a tenant switch swaps the record and reloads the page
(`auth.tsx switchTenant`), which is what makes a swap safe (module-level
token, acting tenant, query cache, local-agent store and streams all restart).
An API token exchanges for a one-hour session with no refresh token
(`handleAPITokenExchange`), so an account of that kind has to keep the API
token to renew. The desktop adds a bridge (claims jobs for one owner), member
scoped sidecars and a member directory per (gateway, subject, tenant); a bare
swap of the stored session would leave the bridge serving the old owner.

## 2. Design

### 2.1 The accounts of one browser or shell

- The signed-in account stays where it is: `bnbloc.session` (every reader of
  it, the desktop's included, is unchanged). The other accounts are parked in
  `bnbloc.accounts` (`lib/accounts.ts`, the ONE owner of the list): each is its
  last session record, a label, a kind and the credential that renews it:
  a refresh token (a person who signed in) or an API token (a test account).
- Accounts are per gateway: the list shows the accounts of the gateway the app
  is on (a record carries its gateway). An account of another gateway waits
  until the app is switched to that gateway.
- A kind is `member` (signed in with the gateway's sign-in) or `test` (an API
  token a super admin issued). The UI says which: a test account has a badge,
  and the account button shows it, so no one mistakes whom they act as.

### 2.2 Switching (web)

Switch = park the active record (renewed first, so its parked bearer is
fresh), install the target as `bnbloc.session` (renewed from its credential
when its bearer is old), clear what belongs to the old account (query cache,
local-agent store, browser push subscription), then reload the page: the
precedent of a tenant switch, which restarts every singleton. A second tab or
window that sees `bnbloc.session` change to another subject reloads too, so no
window keeps acting as the old account (today a pop-out keeps the old bearer
until a 401 and then flips to the new account mid-view).

- **Add an account:** "Add account" parks the active one and starts the
  gateway's normal sign-in (SSO or, where offered, direct). When it lands the
  new account is active and the previous one is already parked. If the IdP
  signs the same subject again nothing is added and the person is told. A test
  account is added by pasting its API token (or by "Add to this browser" on
  the Test accounts page).
- **Remove an account:** a signed-in account's sign-in is revoked at the
  gateway and forgotten; a test account is forgotten locally (its API token
  stays valid until revoked on the Test accounts page).
- **Sign out** signs out the active account only; the next parked account is
  offered, never switched to silently.

### 2.3 Test accounts (super admin)

A "Test accounts" tab of the Platform console (super admin only, like the
other platform tabs):

- Lists the `svc:` members of the tenant the console acts in, with their role
  and whether an API token is active.
- Creates one: a name (`svc:qa-viewer`), a role (viewer, member, org admin;
  never super admin, which is configuration), then the page adds the tenant
  member first and issues the API token second (a half-done create leaves a
  member with no token, never a token with the wrong role), shows the token
  once with Copy, and offers "Add to this browser" which parks it as a test
  account (no copy and paste).
- Removes one: revokes its tokens and removes the member.
- Acts in the tenant the console acts in (the acting-tenant switch), so an
  account of another organization, to try a non-member of the room's
  organization, is made by acting there.
- It adds no power: a super admin could already issue the token and set the
  role; the page only does the two in the right order and keeps the result.

### 2.4 The desktop (slices 2 and 3)

The desktop must not swap the session and nothing else. What binds to one
account (from the shell's code): the bridge (owner, tenant, gateway, its own
refresh-token family; claims jobs for one owner), the member scoped sidecars
and plugins (restarted when the adapter session's member changes), and the
member directory `members/<hash of gateway, subject, tenant>/` (bridge state,
component settings, MCP sign-ins, sandbox homes). Machine wide and not keyed
by account: `settings.json`'s single owner/tenant/gateway slot, the host
workdirs `~/bnbloc-workspaces/<space>` (named from core, provider and room,
without the owner), `local-files.json`, and some unscoped localStorage UI keys.

- **Slice 1 (this record):** the account switcher is web only; in the desktop
  shell it is hidden (fail closed), because a web swap would leave the bridge
  serving the old owner.
- **Slice 2 (designed):** one shell command, `switch_account`: feed the adapter
  session first, then change the settings slot and restart the bridge ONCE
  (today a switch restarts it twice and, while the two disagree, the bridge
  runs on in-memory state only), refuse while a static gateway token pins the
  bridge, close the pop-out windows, and warn first when a turn is running
  (`bridgeRunningTurns`, as the updater does) because a bridge restart ends
  turns. The same slice fixes sign-out: it must stop the bridge and clear its
  owner, and revoke the bridge's own forked refresh family (today a signed-out
  member's bridge keeps serving their rooms; recorded in
  `docs/bugs/2026-10-22-desktop-sign-out-leaves-the-bridge-serving.md`).
- **Slice 3 (decision needed):** host workdirs per account. Two accounts using
  the same room and agent on one machine share one workdir; including the owner
  in the space name moves existing workdirs. Until then the desktop switcher
  tells the person that agents of two accounts in the same room share their
  working folder.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A person with several accounts (or one person and their test accounts), the super admin who creates test accounts, the accounts themselves (a test account is a `svc:` identity, never a person's). An org admin cannot issue tokens (platform route). |
| Time and lifecycle | A parked bearer is old; it is renewed from its credential at switch time. A refresh token rotates: the parked copy is replaced whenever the account is active and renews. An API token lives up to 365 days (default 90) and is revoked on the Test accounts page; a revoked token makes its parked account fail to renew and it is dropped with a message. |
| Scale and cost | A handful of accounts per browser; no polling of parked accounts; a switch costs one renewal and a reload. |
| Failure and recovery | A parked account that cannot renew (refresh family revoked, token revoked, tenant suspended) is removed from the list with a message, and the switch does not happen (the active account stays). A network failure keeps the list and the active account. |
| Boundaries and privacy | More credentials at rest in the same place as today's one (localStorage), so a script injected into the page reads all of them: the same trust as the one session, a wider blast radius. A test account's API token is a long-lived bearer (renewing is its only use): kept only where the person pastes or adds it. The Test accounts page is super admin only (the platform routes). Test accounts never reach a tenant they were not created in. |
| Abuse and untrusted input | Adding an account by pasting an API token: the token is exchanged at the gateway (rate limited per IP); a wrong one adds nothing. The label and subject shown come from the gateway's answer, never from what was pasted. A test account cannot be given `super_admin`. |
| One owner | `lib/accounts.ts` owns the list; `auth.tsx` owns the active session and the switch; the platform tab owns test accounts' creation by calling the existing APIs in order; the shell (slice 2) owns the bridge's account. |
| Defaults and fallbacks | One account, no list: nothing changes. In the desktop shell the switcher is hidden until slice 2. A record without a credential kind is a signed-in account as before. |
| External contracts | None in the gateway: the page uses `POST /v1/platform/api-tokens`, `DELETE /v1/platform/api-tokens/{id}`, `GET /v1/platform/api-tokens`, `POST/DELETE /v1/tenant/members`, `PUT /v1/tenant/members/{id}/role`, `POST /v1/auth/api-token`. `bnbloc.session` gains an optional `apiToken` field. |
| Load on users and agents | One menu section; a badge for test accounts. No agent text, tool or rule changes. |
| Release and compatibility | Web only for slice 1; a gateway without the platform routes shows the tab empty. Old records parse as before. |
| Observability | API token issue, exchange and revoke are already audited (`api-token` extension audit). A switch is a client event with no server trace; the exchange of a test account's token is the trace. |
| AI capability | None. |
| Component or platform | Platform: sessions and organization roles are the platform's. |

**Pre-mortem.**
1. *The desktop swaps the web session and the bridge keeps the old owner.* The
   switcher is hidden in the shell until slice 2; a web test holds it.
2. *A window keeps acting as the old account.* Every window reloads when
   `bnbloc.session` changes subject (`storage` event); a test holds the
   decision function.
3. *Two tabs renew one refresh token at once.* The gateway tolerates a reused
   refresh token inside a grace window; the parked copy is rewritten from the
   storage the renewal wrote, never from a stale closure.
4. *A test account is mistaken for a person.* Badge in the account button and
   the menu; the banner of the Test accounts page says what it is.
5. *A create leaves a token with the wrong role.* The member is added first and
   the role it was given is read back from the exchange; a test holds the order.
6. *An old parked account is switched to and fails after the reload.* The
   switch renews the target first; failure aborts before anything changes.

## 4. Build order

1. Web: the accounts store and its pure decisions, the switcher in the account
   menu, add and remove, cross-window reload, the API-token credential in the
   session renewal, the Test accounts tab. (built with this record)
2. Desktop: `switch_account`, the single bridge restart, the running-turn
   warning, the sign-out fix. (designed)
3. Host workdirs per account. (decision needed)
