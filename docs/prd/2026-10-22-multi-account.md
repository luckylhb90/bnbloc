# Several accounts in one app, all live (2026-10-22)

Status: end-state design (2026-10-22). Built so far: the web's account list and
switcher (first cut, reshaped by slice 4), the Test accounts page, the cascade
that ends a sign-in with everything it opened (gateway and shell), and slices 1
to 6 of §4: the account scope in the shell; the account runtime, session
keeper, registry, settings split and window binding; the per-account
components (shared cores, per-account plugins and sandbox, union sweeps); the
web's vault with per-account renewal locks, keep-alive, window binding and last
view; shell windows per account; the switcher's status, removal with optional
local-data deletion and "Continue as". Not built: the sandbox VM's start on
demand and idle stop (§2.4). Unread rooms of the other accounts: built, with
the query shaped by measurement. Unverified here, to run on a real desktop: the
shell windows, the SSO deep link across windows, the VM runtime's machine
naming, a shared core's behavior across two accounts. Owner: the account vault and window binding
(`web/src/lib/accounts.ts`, `auth.tsx`), the platform console
(`web/src/views/org/`), and for the desktop the account runtime
(`apps/desktop/src-tauri/src/`).

## 0. The end state in one paragraph

A person (or a tester) holds several accounts in one app, on one or several
gateways. Every signed-in account is **live**: its agents serve its rooms, its
sandbox, plugins and MCP tools run, its session renews, whether or not a
window shows it. A **window** (a browser tab, a desktop window) is bound to one
account and shows only that one; two windows can show two accounts side by
side, so an admin and a viewer can be watched interacting. Switching an
account in a window re-binds that window and nothing else. Credentials live in
one **vault**; everything on the machine that belongs to an account lives
under that account's own directory; everything shared (binaries, caches) is
content-addressed and swept by what **all** accounts still need. Signing an
account out ends everything it opened, server side and on the machine.

## 1. Why, and what the first cut got wrong

- QA 2026-10-22: trying the roles of a room (a viewer, a member, a non-member
  of a shared room) took one identity and a sign-out per role. The owner asked
  for accounts with different permissions to switch between in the page, then
  for a multi-account login for colleagues with two real accounts.
- The first cut (web) parks the other accounts and swaps the one stored session
  on a switch, reloading every window that sees it change. That is a
  per-browser singleton with a reload patched over it: two accounts can never
  be seen at once, a pop-out follows whatever the last switch did, parked
  accounts are signed in but not live, and the desktop's bridge and sidecars
  cannot follow a swapped session at all (hidden there). It is a stage, not the
  design.
- The desktop binds one bridge, one adapter (catalog, plugins, sandbox, MCP),
  one settings slot and three machine directories to "the" signed-in member
  (`bridge.rs`, `internal_ext.rs`, `sidecar_sweep.rs`): checked in the code,
  see §2.4.

## 2. The model

### 2.1 Account, scope, vault

- An **account** is a gateway and a subject (`accountKey`). It has a current
  **scope**: its tenant, which the account switches among its organizations
  (`switchTenant`); the shell's member directory is keyed by the full scope
  (gateway, subject, tenant) and stays so.
- The gateway is part of the account. A window's gateway is its account's
  gateway; the gateway override stops being one global value and becomes the
  question "add an account on which gateway".
- Credentials: a person's account renews from a refresh family; a **test
  account** (a super admin's `svc:` identity) from its API token, and has no
  refresh family. The **vault** holds every account's record in the browser's
  storage (the webview's on the desktop, so the trust model is today's: the
  page that holds one session holds them all). A script in the page reads the
  whole vault: the blast radius of one session becomes all accounts, and the
  vault says so where an account is added.
- Every account stays signed in: a **keep-alive** renews each account's
  refresh family before it ages out (30 days), in the window that has the
  vault open, under a per-account lock (`navigator.locks`) so two windows never
  rotate one token at once. An account that can no longer renew is shown as
  signed out ("sign in again"), never dropped silently.
- The vault is the one list; it replaces "the signed-in account plus parked
  ones" with accounts, each with `lastView` (its last room and view, restored on
  every bind) and a `lastUsed` stamp.
- **The vault is the source of truth and the shell reconciles to it.** The web
  hands the shell a snapshot of the vault (each account: key, gateway, tenant,
  its credential kind and token) at start and on every change; the shell makes
  its runtimes match (create the missing, re-scope a changed tenant, stop and
  forget the absent). There is no imperative "start", "sign out" or
  "ensure the bridge runs" command: removing an account from the vault is what
  stops its runtime. The handoff always reads the vault at that moment.

### 2.2 Window binding

- A window is bound to one account by storage that dies with the window
  (`sessionStorage` on the web; the window label in the shell). A new window
  binds to the vault's most recently used account; a window opened from another
  (a pop-out, a preview) inherits its opener's account explicitly.
- Switching re-binds **this** window: renew the target, bind, restore its last
  view, reload. A reload (not an in-place swap) because every singleton of the
  page (bearer, acting tenant, caches, streams, local-agent store) is bound to
  an account, and restarting them is the only way to be sure nothing of the old
  account is visible in the new one. Other windows are unaffected, which
  removes the cross-window reload of the first cut.
- **Add account** opens a new window with the sign-in; the window it was chosen
  in is untouched (no sign-out, no parking). When the sign-in lands, that window
  is bound to the new account. A desktop SSO deep link is delivered to every
  window and redeemed only by the window holding the pending nonce (kept in
  that window's own storage).
- **Removing or signing out** an account sends every window bound to it to the
  sign-in (the one cross-window event that remains: the vault no longer has
  the account).
- The app ends when its last **account window** closes (pop-outs and previews
  are views and never count); the first window is no longer special.
- A window never reads another window's account. Keys that held a choice
  without naming the account (agent target, local agent address, viewer choice,
  workbench layout) become account-scoped (`memberKey`), a precondition.

**Switching or several windows: both, one model.** The window is the unit and
holds one account for as long as it is bound. Several accounts at once is
several windows (browser tabs on the web, shell windows on the desktop), each
with its own page, state, streams and drafts untouched; "switch this window"
is only the shortcut that re-binds a window, for the person who wants one
window. An in-app tab strip (several accounts in one window) is not built: it
needs several webviews in one window and a tab bar that duplicates the
browser's, and gives nothing the windows do not, since every window already
keeps its state while another is in front.

### 2.3 The account runtime (desktop)

One **account runtime** per signed-in account, owning:

- a **session keeper**: forks the account's own refresh family once from the
  token the vault hands it (a fork rotates nothing of the web's family), then
  never uses the web's token again: it renews its own family hourly, hands
  bearers to its parts, and revokes it on removal. **The bridge forks from the
  keeper's family, never from the web's token**: the web's keep-alive rotates
  its token, and a rotated-away token presented later is a reuse that ends the
  whole family and every fork under it. A keeper of a test account exchanges
  its API token hourly instead;
- an **adapter**: gateway client, catalog reconcile, plugin-events stream,
  member-scoped plugins, sandbox, MCP tools, skills (everything member-scoped
  today, unchanged in behavior, one instance per account);
- a **supervisor** and its bridge (own env, own member directory, own refresh
  family), running while the account's "this computer serves my agents" switch
  is on (default on; a person can turn it off for an account they do not want
  serving from this machine);
- its member directory, its **account settings** (below) and its **workdir
  root** (§2.4).

**Settings split.** Machine settings stay machine-wide: the configured cores
(their endpoints and tokens) and the dev single-host flag. Everything a person
chose *as that account* becomes account settings, kept beside the member
directories under the account (`accounts/<hash of gateway and subject>/`):
gateway, tenant, default agent, disabled providers, sync secret, the static
gateway token, and the serve switch. The single owner, tenant and gateway slot
of `settings.json` goes away (no migration: pre-launch).

A **registry** maps account to runtime. Windows resolve their runtime through
the registry: a command resolves it from the calling window's label; a
custom-scheme handler from the request's webview label (Tauri 2
`webview_label`). Lifecycle events are the only writers:

| Event | Effect |
|---|---|
| the vault gains an account | create the runtime, start it |
| the vault changes an account's tenant | re-scope that runtime (stop its scope-bound parts, start for the new scope) |
| the vault loses an account (remove, sign out) | stop the runtime first, then revoke its fork and forget its credentials; ask whether to delete its local data (member directory, workdirs) |
| app start | create a runtime for every vault account that can renew |
| app exit | stop every runtime (graceful, then kill) |

A test account has no refresh family, so its runtime has no bridge and no
session keeper beyond the API-token exchange: it is a way to *see* a role, not
to serve agents, and says so.

### 2.4 Shared machine resources

Checked in the code (`sidecar_sweep.rs`, `mcp_tools.rs`, `sidecar_status.rs`,
`previewer_cache.rs`, `sidecar_orphan.rs`, `kind_runtime.rs`, `sandbox.rs`,
`bridge.go`):

- **Binaries and bundles** (cores, plugins, previewers, MCP tools, sandbox
  bundles) are content-addressed and machine-wide. Every sweep, garbage
  collection and `retain` judges by the **union** of every runtime's live set
  (a `LiveSet` owned by the registry), never by one catalog.
- **What a catalog decides** (`core-manifests`, the MCP tools `index.json`)
  moves under the account's member directory; the shared store holds bytes only.
  The bridge's env names the account's paths.
- **Core sidecars** are machine processes keyed by name and content digest,
  shared by every account whose catalog pins that digest (an account whose
  catalog pins a different digest of a running core gets an explicit
  "core version differs" state, never a silent reuse of the other build).
- **Plugins and the sandbox run one instance per account** on ephemeral
  loopback listeners (the sandbox already does; plugins stop binding the
  package's fixed `localUrl`, which stays only the declared shape). Every
  plugin, not only those that read member state: a plugin talks to the shell
  through the plugin host API with a per-launch token that belongs to one
  adapter, so one shared plugin process could serve only one account. A
  plugin's address is reached through the shell, per account. Only **cores**
  (daemons the bridges speak the Core Protocol to) and the content-addressed
  binaries are shared.
- **The sandbox** runs once per account that serves agents, as it does for one
  account today (its data dir is in the member directory, its machine is
  prewarmed once its sidecar answers). An account that does not serve agents
  (a test account) runs no component at all. **Not built: starting the VM on the
  first sandboxed turn and stopping it when idle.** It needs the bridge to tell
  the desktop a turn wants the sandbox (a new bridge-to-shell signal, beside
  `turns.json`) and a VM runtime whose idle stop and start cost can be measured
  on a real desktop; until then each serving account's machine is up while its
  sandbox sidecar runs, which the person sees as the memory of one VM per such
  account.
- **Spawn records and orphan sweeping** are keyed per runtime and judge
  liveness of the recorded app pid, so one app instance never kills another's
  children.
- **Workdirs**: a sandbox space, and its workspace folder under the shared
  root, is named by a hash of the seat (room, core, provider, requester) AND
  the account the bridge serves (`bridge.BridgeAccount`, gateway and owner), so
  two accounts' agents of one core and provider in one room never share a space
  or a folder (they did: an owner's own seat used to be named without the
  owner). No migration: the names change, existing folders are left (pre-launch,
  no backfill). A host-mode workspace is the core's (it creates one per bridge
  request).
- **Machine-wide by nature, not isolated**: the OS user (a host-mode agent
  can read another account's files; the sandbox is the boundary between
  accounts), the machine's CLI logins and shell environment. The add-account
  dialog says so.
- **The update gate** sums running turns of every runtime; a core or app
  update warns about all accounts' turns.

### 2.5 The experience

- The account menu lists every account with its status (bridge online or
  offline, running turns, unread, signed out). Choosing one **switches this
  window**; the control beside it **opens it in a new window** (or focuses the
  window already showing it), which is how two accounts are watched side by
  side. It also adds, removes ("and delete its local data" is a
  separate, explicit choice), and shows a Test badge on test accounts.
- Sign-in offers "Continue as" for the vault's accounts; the first run of a
  desktop build without a default gateway asks which gateway.
- Notifications and sounds come from the visible account of a window;
  background accounts show how many of their rooms have unread messages and
  their agent status on the switcher, and a dot on the account button. The
  count is one bounded request per account (`GET /v1/my/unread`: rooms of the
  member resolved on the server, one indexed query that stops at 100, a
  10 s server cache), asked by the web as that account once a minute while a
  window is visible and only when there is another account; the account in
  front keeps its own per-room poll. Rooms with unread, not messages: the
  switcher answers "is there something there", and the number is cheap and
  bounded. Native
  notifications for background accounts are not part of this design: they need
  a message stream per account that nothing else uses.
- Test accounts (§ below) are accounts like any other in the vault; the
  Platform console creates them and adds them in one click.

### 2.6 Test accounts (built)

The Platform console's Test accounts tab (super admin): create a `svc:`
account with a role (the member first, then its token), add it to the vault,
remove it (revoke its tokens, remove the member). It adds no power a super
admin lacked.

### 2.7 Server

Nothing new beyond what is built: a sign-in ends with everything it opened
(`revokeFamilyAndForks`: logout and "revoke this session" end the family and
the sessions forked from it), API tokens, the member list roles. The shell's
fork per account is a `desktop` client family nested under the account's web
family, so "My sessions" shows the app's accounts under their sign-in and a
logout ends them.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A person with several accounts, a tester with test accounts, the super admin who creates them; each account's own members and agents see nothing of the others. Two windows may show two accounts of one person at once. |
| Time and lifecycle | An account lives from sign-in to removal, runtime included; a keep-alive renews it; an account that cannot renew shows as signed out. Switching is a re-bind of one window. Sign-out ends the family, its forks, its runtime and its fork on the machine. App start recreates every runtime; app exit stops them. |
| Scale and cost | A handful of accounts. Costs per account: a bridge, an adapter's plugins and sandbox, a sandbox VM only while in use. Shared binaries and cores are paid once. Two accounts on one gateway make one catalog poll each (60 s). |
| Failure and recovery | A runtime that fails (bridge crash, a plugin) is restarted by its own supervisor; the others run on. A parked account whose renewal is refused is marked, not removed. A core digest the account's catalog pins and the machine cannot run says so on that account only. |
| Boundaries and privacy | Per-account member directories and workdir roots; sandbox per account; windows never share an account. The vault is one store: its blast radius is all accounts (said at add time). Host mode shares the OS user (documented boundary). A removal can delete the account's local data. |
| Abuse and untrusted input | Pasting an API token exchanges it at the gateway (rate limited); the shown subject is the gateway's answer. A window's account binding comes from storage the page owns, not from the URL, except an explicit opener inheritance validated against the vault. |
| One owner | The vault owns the account list and renewal; the registry owns runtimes and windows' binding; `LiveSet` owns what the machine keeps; the member directory owns an account's local state; the gateway owns sessions. |
| Defaults and fallbacks | One account: nothing visible changes (one runtime, one window). No default gateway: ask. A keychain is not needed: credentials stay where today's one session is. |
| External contracts | Sidecar `localUrl` is the declared shape, not the instance address of a plugin or the sandbox (the sandbox already works so). Account-scoped storage keys replace unscoped ones. The gateway gains nothing new. |
| Load on users and agents | A switcher with status; no agent text, tool or rule changes; background accounts' agents are as available as the visible one's. |
| Release and compatibility | Slices 1-3 change nothing for one account and ship alone; 4-6 turn the feature on. Pre-launch: unscoped keys and the single settings slot are replaced, not migrated; existing workdirs are left, not moved. |
| Observability | Per-runtime status and logs (the bridge status per account), account events in the shell log, the `desktop` families in My sessions, bridge ids derived from owner and tenant as today. |
| AI capability | None changes: agents are the account's. |
| Component or platform | Platform: sessions, the vault, the registry and the sweeps are the platform's; components stay components and learn nothing about accounts. |

**Pre-mortem.**
1. *A sweep deletes another account's binaries.* Every sweep takes the registry's
   union `LiveSet`; tests hold a two-catalog case on each sweep (`sidecar_sweep`,
   MCP, manifests, previewers).
2. *One app instance's orphan rule kills another's children.* Records are per
   runtime and liveness-checked; a test holds two records with a live pid.
3. *Two windows rotate one refresh token.* Renewal runs under a per-account lock
   and writes the vault once; a test holds the single-flight decision.
4. *A window shows the wrong account after another window switched.* The binding
   is window-scoped; a test holds that a vault change never re-binds a window.
5. *Two accounts in one room share a workdir.* The space name carries the
   account; a Go test holds that two accounts get two names, that a bridge that
   serves no known account keeps the old name, and that the guest user name
   stays under its 32 characters.
6. *Sign-out leaves a runtime serving.* Removal stops the runtime before it
   revokes; a shell test holds the order and that a failed revoke still stops it.
7. *The sandbox VM of every account sits idle.* Not closed: only accounts that
   serve agents run one (a test holds that a test account runs no component);
   start-on-demand and idle stop are not built (§2.4), and the cost is named
   there.
8. *The shell uses a rotated-away web token.* The bridge and every restart fork
   from the keeper's own family; the web's token is read from the vault only
   at the handoff, and a failed fork waits for the next handoff; a test holds
   the fork source.
9. *Two windows of one account disagree.* They share the account's vault record
   and renew under its lock; a window whose bearer a sibling rotated reads the
   vault on its next refusal.
10. *The vault says remove and a runtime keeps serving.* The shell is a
   reconciler: an account absent from a snapshot is stopped before it is
   forgotten; a test holds the reconcile (create, re-scope, stop).
11. *A plugin breaks on an ephemeral port.* Every plugin moves (a plugin whose
    package documents a fixed port for an external redirect is a package
    contract to fix, not a case to keep); the package's `localUrl` stays the
    declared shape; a test holds which kinds move (`kind_runtime.rs`, no branch
    on a name).

## 4. Build order (slices of this one design)

Each slice is a part of the end state, not a stand-in. 1-3 are invisible with
one account.

1. **(built)** **Account scope in the shell**: the `AccountKey`, per-account catalog dirs
   (manifests, MCP index, spawn records), the `LiveSet` union for every sweep and
   `retain`, owner-gated handoffs (core URLs, sandbox, the sandbox owner label
   from the adapter's own session), the adapter decoupled from `AppState`
   (a bridge-control handle), orphan liveness. Tenant switching benefits at once.
2. **(built)** **Account runtime and registry**: the reconciling hand-off (the vault's
   snapshot replaces `internal_adapter_session` and `ensureBridgeRunning`),
   runtime, session keeper (fork, renew, revoke), per-runtime supervisor, the
   settings split (machine settings and account settings), window-resolved
   commands and scheme handlers, summed update gate, runtime lifecycle.
3. **(built, except the sandbox's start on demand)** **Per-account components**: ephemeral listeners for every plugin,
   core sharing by digest with the explicit mismatch state, sandbox on demand and
   idle stop, per-account workdir roots (shell env, bridge naming).
4. **(built)** **Vault and window binding (web)**: account-scoped storage keys, the vault with
   keep-alive and renewal lock, `sessionStorage` binding, last view per account,
   hand-off of every account to the shell; replaces the first cut's single
   session, parked list and cross-window reload.
5. **(built)** **Desktop windows per account**: window label to account, pop-outs inheriting
   the opener's account, "Open in new window", per-window gateway.
6. **(built)** **The experience**: status on the switcher
   (session, agents online and running turns from the shell's own state; the
   unread rooms of every other account, §2.5), remove with optional local-data deletion, Continue as (an
   account that needs a sign-in says so), and the add-account dialog's vault
   note (the gateway of a new sign-in is chosen where it always was, the gateway
   switcher of the sign-in window). Account events are the shell log's
   `[accounts]` lines.

What cannot be verified here and is handed to a real desktop run: the external
sandbox runtime's host-global names, a core's behavior on a shared digest,
the real sandbox VM's idle cost. Each slice names what it ran (`cargo test`,
`go test`, node tests) and what it did not.

## 5. Boundaries (not goals)

- A switch reloads the window (isolation over animation); it restores the
  account's last view.
- Native notifications for background accounts.
- A security boundary between accounts in host mode (the sandbox is that
  boundary).
- Several tenants of one account side by side (a tenant is a scope the account
  switches, as today).
