# Components describe their own settings (2026-10-19)

Status: slice 1 built (2026-10-20, §5); slice 2's own settings page built
(2026-10-20, §2.2a, §5), moving local skills to its page not yet; slice 3
built (2026-10-20, §2.3, §5: endpoint components ask, in a staged
migration); slices 4–5 proposed. Owner: the component contract
(`core/pkgmeta`), the settings store (`internal/componentsettings`), the
one settings form.

A component that needs a setting (a URL, a key, a model, a path, a switch)
declares it in its own manifest. The platform stores the values, shows one
form for them and hands them to the component. Adding a setting to a
component is a change to that component's repo only, never to this one.

## 1. Why

Today two kinds already work this way, and the rest do not
(survey 2026-10-19):

| Kind | Where its settings are defined | A new setting needs the main repo |
|---|---|---|
| MCP tool | the manifest's `mcp.env` (name, description, secret, required) | no |
| Desktop plugin | the manifest's `localSettings` (key, label, help, secret, required) | no |
| Media, decision and assist providers | Go settings keys, wire types, routes and a hand-written form; the adapter kinds listed again in the web | yes, four places |
| Notifier, auth provider (endpoint components) | the component's own deployment environment, invisible to the platform | the platform cannot set it at all |
| Sandbox, core connection | fixed fields of the desktop's settings struct and hand-written pages | yes |
| A plugin with its own page | a hand-written settings view in the web | yes |

And the two that do work have gaps:
- `mcp.env` and `localSettings` are two shapes of one idea, with two
  validators. Neither has a type, a default, a choice list or a scope:
  every value is a string a person types.
- A room-credential MCP tool may declare no setting at all, so the browser
  component's own knobs (the Chrome binary, its profile directory) have no
  place to be set.
- The manifest parser refuses unknown fields, so anything the two shapes
  cannot say needs a main-repo change.

The AGENTS rule already points here: never grow a component-specific path,
env name or default in public code; extend the manifest schema instead.

### 1.1 What uses settings today (QA, 2026-10-19)

Fifteen components are installed on QA; nine need settings, and some
outside QA too. Each is a migration of a slice (§5):

| Component | Kind | Its settings today | Slice |
|---|---|---|---|
| browser | MCP tool | Chrome binary, profile and state directories, endpoint, model, call budget: env and flags nobody can set | 1 |
| a chat bot plugin | plugin | app id, app key, bot id (`localSettings`); its API and websocket URLs baked into `sidecar.args` at publish | 1 |
| local skills | plugin | data directory, mounts; a shipped default policy; a hand-written page here | 1, 2 |
| acp | core | a config file: the agent list (command, args, env, models, permissions, profile) | 2, 5 |
| a SAML auth provider | auth provider | IdP metadata URL, SP entity, ACS and login URLs, attribute mapping, SP certificate and key, allowed redirects: deployment env | 3 |
| a chat notifier | notifier | API URL, app id and key, bot id, tenants, recipient map, signing secret: deployment env | 3 |
| media-tools | MCP tool | none of its own: its providers are the platform's media settings | provider components |
| sandbox | sandbox | mode and env as fixed desktop fields; guest CPUs and memory | 5 |
| other cores (a CLI-fronting sidecar, an adapter daemon, a manifest core) | core | their own CLI's or daemon's configuration; an upstream URL; a listener path | 5 |
| six previewers | previewer | none | — |

Env names: components read their own as `BNBLOC_<THEIR NAME>_*`, so a
setting may be delivered under its component's own prefix; every other
`BNBLOC_*` name stays the platform's (§2.1).

## 2. What it is (the end state)

### 2.1 One declaration: `settings`

Every component kind may declare `settings` in its manifest, a list of
fields:

```json
"settings": [
  {"key": "apiBase", "label": "API base URL", "type": "url", "scope": "org",
   "required": true, "deliver": {"env": "NOTES_API_BASE"}},
  {"key": "apiKey", "label": "API key", "type": "secret", "scope": "org",
   "required": true, "deliver": {"env": "NOTES_API_KEY"}},
  {"key": "chrome", "label": "Chrome binary", "type": "path", "scope": "member",
   "help": "Leave empty to use the installed Google Chrome.",
   "deliver": {"env": "BROWSER_CHROME"}},
  {"key": "quality", "label": "Picture quality", "type": "choice",
   "options": ["low", "medium", "high"], "default": "medium", "scope": "member"}
]
```

- `key`: `[a-z][a-zA-Z0-9]{0,39}`, unique in the component.
- `type`: `text`, `secret`, `url`, `number`, `boolean`, `choice` (with
  `options`), `path` (member scope only: a path on the member's own
  machine). A `secret` is write-only: never shown back, only "set".
- `scope`: who sets it, and for whom it holds:
  - `platform`: the super admin, for the whole deployment;
  - `org`: an organization admin, for their organization;
  - `member`: each member, for themselves.
- `required`, `default` (valid for its type; never for a secret), `help`.
- `deliver` says how the value reaches the component (§2.3); without it,
  the component reads its settings file.
- Bounds: at most 24 settings; an env name matches `[A-Z][A-Z0-9_]*`, never
  `BNBLOC_*` except the component's own `BNBLOC_<NAME>_` prefix (the rest
  is the platform's), and never a variable that changes how a process
  runs (`PATH`, `HOME`, `LD_*`, `DYLD_*`, `NODE_OPTIONS`, …: one
  deny list, owned by `pkgmeta`).

`mcp.env` and `localSettings` are removed: a setting is a setting
(`mcp.env` becomes `settings` with `deliver.env`; `localSettings` becomes
`settings` of member scope). An HTTP MCP tool's auth header names a
setting key instead of an env name.

### 2.2 One store, one form

- **Values.** One sealed record per component, scope and owner:
  `component.settings/platform/<component>`,
  `component.settings/org/<tenant>/<component>`, and member values on the
  member's device for a component that runs there (as plugin and MCP
  member values are today), else
  `component.settings/member/<tenant>/<subject>/<component>`. Secrets are
  sealed bound to their key (sealbox), as the MCP connectors' are. A save
  is checked against the declaration (types, required, options); values
  of settings the version no longer declares are dropped on the next save.
- **Form.** The one settings form (today's `LocalSettingsCard`, widened to
  the types) renders a declaration wherever it is set:
  - Platform console → Components → a component → Settings (platform
    scope);
  - Organization → Components → Settings (org scope; today's connectors
    tab);
  - the Components page, where the component is added: its card's Settings
    (Set up while it needs any) opens one dialog with every scope the viewer
    may set, and it opens by itself right after adding a component that
    needs setup (revised 2026-10-20: settings beside the component, not in
    a separate settings page).

  A component's settings show only where its scope is set; a member sees
  an organization setting as "set by your organization", never its value.
- **Needs setup.** A version whose required settings are not all set is
  installed but "needs setup": it does not run and is not offered to
  agents, and the form says what is missing (plugins behave so today).
  Upgrading to a version that adds a required setting without a default
  keeps the running version until the new one is set up.
- **Audit.** Every save is audited: the component, the scope, the keys
  changed; never a value.

### 2.2a A component's own settings page (optional)

The declaration stays the contract; how the fields are shown can be the
component's. A component whose settings need more than fields (folder
lists, an OAuth sign-in, fields shown by a choice, a live preview) ships
its own page, and the platform shows it in a dialog where the one form
would be (decided 2026-10-19).

- **Declared.** `settingsPage` names an HTML file in the package (with its
  scripts and styles beside it, like a previewer's bundle). Without one,
  the one form renders the declaration (§2.2): most components write no
  HTML. Built as `"settingsPage": {"entry": "index.html", "bundle": {"url",
  "sha256"}}` on a plugin or an MCP tool beside its `settings` (decided
  2026-10-20): the bundle is a previewer bundle (the same archive format,
  limits, probe and serving), pinned beside its entry rather than in
  `artifacts`, which are a plugin's or a stdio tool's per-platform binaries
  (an http tool has none). `Package.Pins` makes the install gate check it
  like every artifact (`docs/architecture/settings-pages.md`).
- **Sandboxed like a previewer.** The page runs in the platform's
  sandboxed frame (`docs/architecture/previewer-components.md`): an opaque
  origin, no network (`default-src 'none'`), no access to the console
  around it. It talks to the platform only through a narrow host API over
  `postMessage`:
  - `get()`: the current values of its own settings in the scope being
    edited, a secret only as "set" or "not set", never its value;
  - `set(values)`: a save, checked by the platform against the
    declaration (types, required, options), then sealed and stored as any
    save; refused values answer the field's reason;
  - `check()`: the component's own check (§2.5), when it declares one;
  - `close()`.

  The protocol (version 1: `settings.ready`/`init`, `get`/`set`/`check`
  answered by `view`/`refused`/`checked`, `close`) and its security rules
  are in `docs/architecture/settings-pages.md`.
- **The dialog is the platform's.** Its title (the component and the
  scope), its Close, and a note that the page is the component's own sit
  outside the frame, so a page cannot pose as the console; the theme
  (light or dark) is passed in.
- **Only what it declares.** A page cannot set a key the declaration
  lacks, read another component's values, or read a secret back. Who may
  open it is the scope's rule (an organization admin for `org`).

### 2.3 Delivery, by how the component runs

| Runtime | How it gets its values |
|---|---|
| A process on a machine (stdio MCP tool, plugin, desktop sidecar, manifest core) | `deliver.env` as its environment; every value also in a JSON settings file named by `BNBLOC_COMPONENT_SETTINGS_FILE` (0600, rewritten on change) |
| An MCP tool over HTTP | `deliver.header` (`{"header": "X-Api-Key"}` or bearer) |
| An endpoint component beside the gateway (notifier, auth provider) | it asks: `GET /v1/component-settings` signed with its component credential, answered its platform and organization values (per tenant) and a version; it asks again every minute and applies a changed version (decided 2026-10-20: pull, no push; `docs/architecture/endpoint-components.md`) |
| A built-in adapter (media, decision, assist) | §2.4 |

A stdio tool's values, secrets included, are visible to the agent CLI
that starts it: the CLI holds the tool's environment, and the tool's
settings file lies where a sandboxed tool can read it (a private temp
directory, not the desktop's data directory, which confinement
read-denies). A secret an agent must never see belongs to an HTTP tool or
a room-credential tool whose secret stays on the platform's side.

A value changes without a reinstall: a process gets it at its next start
(the form says so), an endpoint component on its next ask.

A process component runs on members' computers (a plugin, a stdio MCP
tool), so its platform and organization values, secrets included, are sent
to those computers: to the bridge in a job's resolution, to the desktop by
`GET /v1/components/{name}/settings/resolved` (a member of the
organization, for a component installed for them; not logged, reads not
audited). The platform and organization forms say so for such a
component ("these values are sent to the computers where this component
runs").

### 2.4 Model providers are components

The assist, decision and media models become provider components that
declare their settings like any component, installed by the platform, an
organization or a member (decided 2026-10-20):
`docs/prd/2026-10-20-model-provider-components.md`.

### 2.5 AI

- **Fill from a note.** "Fill with AI" on the form: the admin pastes the
  setup note a vendor sent (an email, a README section), and the assist
  maps it onto the declared fields (a JSON assist whose output schema is
  the declaration, secrets included only into the form, never stored by
  the assist or logged). The admin checks and saves.
- **Check.** A component may declare `check` (a process run with
  `--check-settings`, or `POST /check` for an endpoint), which answers ok
  or a reason; the form offers "Check" and, on a failure, the assist
  explains the reason against the declaration ("the URL must end with
  /v1").

## 3. Design review

| Lens | Answer |
|---|---|
| One owner | One declaration shape (`pkgmeta`), one validator, one store, one form, one delivery per runtime; the web's copies of adapter kinds go. |
| Component or platform | The platform keeps what a component cannot do safely: sealing, scope, consent, audit, delivery. What a setting means is the component's. |
| Load on users | One form everywhere, in the place its scope is set; fewer hand-written pages. A component's own page only when fields are not enough. |
| Load on agents | None: settings never reach an agent's context. |
| Boundaries | A value holds only for its scope; a member never reads an organization's secret; a component reads only its own record. |
| Untrusted input | A declaration is a package's: bounded, typed, env names checked against the deny list. A value is checked against its declaration on save, from the form or a component's page alike. A component's page is untrusted code in a sandboxed frame with no network and no secret read. |
| Time and lifecycle | Needs setup; an upgrade keeps the running version until set up; dropped settings' values go on the next save. |
| Release safety | Pre-launch: `mcp.env` and `localSettings` are replaced, not kept beside; our components republish with `settings`. Organization connector values are entered again (no backfill). |
| AI | Fill from a note; a check explained. |

## 4. Pre-mortem

1. **A component reads another's secret.** Each record is sealed bound to
   `component.settings/…/<component>`; delivery reads only the running
   component's record; endpoint components prove their install identity.
2. **A setting hijacks the process** (`LD_PRELOAD`, `PATH`). The env deny
   list refuses the declaration at install.
3. **An upgrade breaks a working component** (a new required key). The
   new version waits in "needs setup"; the old one keeps running.
4. **Admins lose their typed values** in the move from connectors. Few
   organizations, pre-launch; the form says what is missing (needs setup).
5. **A component's page phishes or leaks.** It runs with no network in an
   opaque origin, reads no secret back, and sits inside the platform's
   dialog that names it; anything it saves is only its own declared keys.
6. **A secret leaks through the assist.** The note goes to the assist
   model like any assist input; the answer fills the form only; the run's
   audit row holds no input.
7. **A wrong console value takes sign-in down** (slice 3: an auth provider
   reads its settings). A changed answer is applied by building a new
   provider beside the running one and swapping only when it builds; a
   failed build keeps the old one and logs why. During the staged
   migration the environment is still the fallback for every value not
   set in the console.
8. **Sealing breaks deliveries** (slice 3: the notifier's secret is now
   sealed and realtime must open it). A secret stored in clear before is
   still read without the key, so the release itself changes nothing; a
   rotation seals it, so realtime gets the key first (the deploy surfaces
   pass it, the operator steps say so, and realtime logs at start when it
   is missing).

## 5. Build order (slices of §2)

1. **The contract and its form.** `settings` in `pkgmeta` and the wire;
   the store; the widened form; stdio and HTTP MCP tools and plugins on
   `settings` (`mcp.env`, `localSettings` removed); needs setup; audit.
   Our components republish: the browser declares its Chrome and profile
   paths; media-tools declares none.

   **Built 2026-10-20** (commits 9bfd31d6 platform, d5644896 web,
   f2b53396 desktop, and the docs beside this record):
   - *Declaration.* `settings` on plugin and MCP packages only (the
     field-to-kinds table; `wire.ComponentSetting`, `pkgmeta.Setting`),
     validated only by `core/pkgmeta/settings.go`: the key rule, the seven
     types, the three scopes, `path` member-only, a choice's 1–32 options,
     a default valid for its type and never a secret's, at most 24.
     `deliver.env` only for a process (a plugin, a stdio tool): an env
     name, never one of the process-hijack list (`processEnv`), under
     `BNBLOC_` only the component's own `BNBLOC_<NAME>_` and never a name
     the platform sets (`ComponentSettingsFileEnv`, the room relay's, the
     plugin host's), never one of its own sidecar env names.
     `deliver.header`/`deliver.bearer` only for an http tool, one header
     each, one bearer at most. `mcp.http.auth` keeps `scheme` and names
     its credential's `setting` (was `env`); every setting of an http tool
     must reach it in a header. `mcp.credentials` is only `"room"`, beside
     settings of any scope. `requires.mcp[].settings` names keys.
   - *Values.* One rule each in `pkgmeta`: `CheckSettingValue` (text up to
     4 KiB, an http(s) URL, a JSON-syntax number, `true`/`false`, one of a
     choice's options), `MergeSettings` (a save: unknown or other-scope
     keys refused, an absent key kept, `""` clears, an empty secret keeps
     the stored one, `clear` removes, required ones of the scope must end
     set, undeclared values dropped, the changed keys for the audit),
     `EffectiveSettings` (a default where no value), `UnsetSettings`
     (needs setup), `SettingsEnv`. The desktop's Rust copy is pinned to
     `core/pkgmeta/testdata/setting_values.json`.
   - *Store.* `internal/componentsettings`: one sealed record per scope at
     `component.settings/platform/<kind>/<name>` and
     `component.settings/org/<tenant>/<kind>/<name>` (the kind is in the
     key: a name is unique per kind only), written by one
     read-modify-write, deleted with the component; the MCP connectors
     (`mcp.connector/…`) are gone, not migrated. Member values: one 0600
     file per component on the desktop
     (`<member>/component-settings/<kind>@<name>.json`), one Rust owner
     (`internal_ext/component_settings.rs`) for plugins and tools alike.
   - *Routes.* `GET/PUT /v1/platform/components/{name}/settings` (super
     admin) and `GET/PUT /v1/tenant/components/{name}/settings` (org admin):
     `wire.ComponentSettingsView` (the scope's fields, a secret as set
     only; the required settings of this and the scopes above it not set;
     whether the values reach members' computers; off without
     `BNBLOC_SECRET_STORE_KEY`, a save then 409). A save is audited
     (`settings-set`, `scope=… keys=…`, never a value). `GET
     /v1/components/{name}/settings/resolved` (member) answers a process
     component installed for the caller its platform and organization
     values. The member catalog carries `settings`, `settingsSet` and
     `settingsMissing` (fail closed on a store error).
   - *Delivery.* Stdio MCP: the gateway's per-job resolution carries the
     declaration and platform and organization values; the bridge adds
     the member's own values on their own drives, sets each
     `deliver.env` and writes every value to the 0600 file named by
     `BNBLOC_COMPONENT_SETTINGS_FILE` (under its state dir, one per tool,
     organization and own or lent drive). Http MCP: headers from the auth
     block and `deliver.header`/`deliver.bearer`. Plugins: the desktop
     reads the resolved values at each launch, merges the member's and
     writes the same file (replacing `BNBLOC_PLUGIN_SETTINGS_FILE`) and
     env. The bridge's dirs are `BNBLOC_COMPONENT_SETTINGS_DIR` and
     `BNBLOC_MCP_SIGN_INS_DIR` (was `BNBLOC_MCP_SETTINGS_DIR`).
   - *Needs setup.* A tool missing a platform or organization setting is
     not offered (a requiring package's turn lists it missing: "needs
     setup: an admin has not set …"); a member one, "set … in Settings →
     Components", or "not lent" on a borrowed drive. A plugin missing any
     is not launched, and restarts when the catalog says the values
     changed. Every settings view shows "Needs setup: …".
   - *Forms.* The one form (`web/src/views/settings/SettingsForm.tsx`, the
     widened `LocalSettingsCard`) renders every type and each field's
     scope; Platform console → Components → Settings, Organization →
     Component settings (was Tool connectors), the Components page
     (merging Tools and Personal bot); a member sees organization and
     platform settings as set or not, never a value.
   - *Not in slice 1.* An upgrade that adds a required setting is not held
     back (the new version needs setup at once); member values of a
     component that does not run on the member's device
     (`component.settings/member/…`) and the endpoint components' ask
     (slice 3) are not built.
2. **A component's own page.** `settingsPage`, the sandboxed dialog and
   its host API (`get`, `set`, `check`, `close`); a plugin with a
   hand-written view in the web (local skills) moves to its own page.

   **Page built 2026-10-20** (commits 47c56bf6 platform, 831eb402 web,
   ca81a50f desktop, and the docs beside this record;
   `docs/architecture/settings-pages.md`):
   - *Declaration.* `settingsPage {entry, bundle}` on plugins and MCP tools
     (the field-to-kinds table), only beside `settings`, validated by
     `core/pkgmeta/settings_page.go`: the entry by the one bundle entry
     rule (`ValidBundleEntry`, now the previewer's too), the bundle a pin
     or an upload placeholder never published. `Package.Pins` (every
     `artifacts.<key>` and `settingsPage.bundle`) is what the complete,
     gateway-URL and probe checks walk.
   - *Install.* The bundle is probed like a previewer's
     (`probeFrameBundle`: in the store, its sha256, a valid archive within
     the previewer limits, holding the entry).
   - *Serving.* A scope's settings view carries `page` (entry, pin and a
     1-hour `settings-page-frame` ticket bound to the subject and
     `<scope>:<kind>/<name>@<version>`);
     `GET /v1/settings-pages/{kind}/{name}/{version}/{ticket}/{path...}`
     serves the files through the previewer's code (`serveFrameFile`,
     `frameCSP`), re-checking on every request that the component is at that
     version and the holder may still set the scope (the platform's:
     `manage_platform`; an organization's: `manage_org` and the component
     visible to it). The desktop serves the same bundle at
     `bnbloc-previewer://localhost/_settings/{sha256}/…`, downloaded by its
     pin with the member's session; the artifact route lets anyone download
     the page bundle of a component listed to them. Catalogs list
     `settingsPage`.
   - *Dialog.* Each scope section of the settings dialog shows the page
     (`SettingsPageFrame`) instead of the one form, editing that scope
     only, with the platform's chrome outside the frame (the component, the
     scope, "This page is the component's own", Close). A member's own scope
     is the desktop's; in a browser it says so, as the form does.
   - *Host API.* `web/previewer-sdk/src/settingsProtocol.ts` (both sides)
     and `@bnbloc/previewer-sdk/settings` (the page side);
     `web/src/lib/settingsPageFrame.ts` (the host): a view with a secret
     only as set and the other scopes only as set or not; a save naming a
     key outside the scope refused before sending; the platform's refusal
     back with the setting it names (`pkgmeta.SettingRefusal`, answered as
     `{"error", "field"}` by the settings routes and the desktop's store,
     which is pinned to it); `check()` answers `supported: false`.
   - *Publishing.* `bnbloc cores publish-mcp` and `bnbloc components
     publish-package` take `--settings-page <dir or tar>`, read with the
     previewer's bundle input before any upload, then uploaded and pinned.
   - *Example.* `core/pkgmeta/testdata/settings-page`, served by the
     gateway's tests and driven through the host by the web's.
   - *Not built.* Local skills' page (the move of a hand-written view);
     `check()` (§2.5, slice 5); the platform console's package form for an
     MCP tool takes no page (the CLI does); a member's own values of a
     component that does not run on their device (§2.2, still slice 1's
     gap).
3. **Endpoint components ask.** `GET /v1/component-settings`; the notifier
   and the auth provider move their deployment environment into
   `settings`.

   **Built 2026-10-20** (the platform's commits beside this record; the
   components' own change in their repository;
   `docs/architecture/endpoint-components.md`):
   - *Declaration.* `settings` on notifier and auth-provider packages too
     (the field-to-kinds table), scope `platform` or `org` only and no
     `deliver` (`pkgmeta.AsksForSettings`, `validateSettings`); no
     `settingsPage` (the one form).
   - *Credential.* One per installed endpoint component
     (`internal/componentcred`, replacing the notifier-only secret store):
     created at install and answered once, rotated by a super admin
     (`POST /v1/platform/components/{name}/secret/rotate?kind=…`, the CLI's
     `--kind`, "Rotate secret" on every endpoint component's card), deleted
     at uninstall; an upgrade of an auth provider installed before gets its
     first one (shown once). Now **sealed at rest**
     (`component.credential/<kind>/<name>`, `BNBLOC_SECRET_STORE_KEY`):
     the gateway creates and verifies, realtime opens a notifier's to sign
     deliveries, so realtime now loads the key too (compose, the
     production overlay, Helm, the all-in-one image). Without the key on
     the gateway a notifier or auth-provider install is refused (409). A
     notifier secret stored in clear before (`notify.secret/<name>`) is
     still read, with or without the key, until its first rotation or
     uninstall removes it, so a deployed notifier keeps delivering across
     the release (`TestLegacyNotifierSecretKeepsWorking`).
   - *Signed calls.* `verifyComponentCall` is the one check
     (`TestComponentCallSingleOwner`): the notifier's reply routes (the
     body signed, the header its name, still behind the reply store) and
     the ask (`X-Bnbloc-Component: <kind>/<name>`, the signature over
     `GET /v1/component-settings`), 5-minute window both ways.
   - *The ask.* `GET /v1/component-settings` answers
     `wire.EndpointComponentSettings`: the stored platform values (secrets
     included, no defaults filled in: the component applies its own, so a
     value it still reads from its environment is not overridden), the
     organization values of the organizations the install serves that
     stored any, the required ones not set per scope, and `version`, a
     hash of the rest (`componentsettings.Store.Endpoint`). Not logged, not
     audited; 409 without the key.
   - *Forms.* An endpoint component's platform settings are on its card in
     the platform console like any component's, saying the component reads
     them itself (`ComponentSettingsView.asks`); Organization → Component
     settings lists components of every kind with organization settings
     (`GET /v1/tenant/component-settings`), since an endpoint component is
     in no member's catalog.
   - *Staged migration (the components).* Sign-in depends on the auth
     provider, so its move is in stages. Its deployment environment keeps
     only what it needs before it can ask: its listen address, its public
     URL, the gateway's address, its credential (and the notifier's signing
     secret, which is the notifier's credential), and the secret-manager
     reference that loads them. Every other value is declared in
     `settings`; during the migration a value set in the console wins and
     the old environment variable is the fallback (the component logs at
     start which source each setting came from, keys only), and a changed
     answer rebuilds the provider in place, keeping the old one when the
     new values do not work. Once the deployment runs on console settings,
     the fallback is deleted in a later change.
   - *Not built.* A push to ask again (the minute's poll is the delay);
     member values of an endpoint component (none by design); `check`
     (slice 5). Before a component drops its environment fallback, two
     bootstrap loops need the platform: an auth provider is not healthy
     until it has settings, but its install probe requires health (and the
     credential it asks with comes only from the install), and the
     gateway activates an installed auth provider at boot with one probe,
     not retried until the record changes. The install probe must accept a
     provider that answers discover but waits for its settings, and the
     activation must retry until it is healthy.
4. **Model providers.** `docs/prd/2026-10-20-model-provider-components.md`.
5. **The rest.** The sandbox's and core connection's desktop fields as
   their components' settings; Fill with AI and Check.
