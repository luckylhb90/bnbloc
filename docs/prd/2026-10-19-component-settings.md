# Components describe their own settings (2026-10-19)

Status: proposed. Owner: the component contract (`core/pkgmeta`), the
settings store, the one settings form.

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
  `BNBLOC_*` (the platform's) and never a variable that changes how a
  process runs (`PATH`, `HOME`, `LD_*`, `DYLD_*`, `NODE_OPTIONS`, …: one
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
  - Settings → Components (member scope; today's plugin and tool pages).

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
  HTML.
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
| An endpoint component beside the gateway (notifier, auth provider) | it asks: `GET /v1/component-settings` with its install identity, answered its platform and organization values (per tenant); a change notifies it to ask again |
| A built-in adapter (media, decision, assist) | §2.4 |

A value changes without a reinstall: a process gets it at its next start
(the form says so), an endpoint component on its next ask.

### 2.4 Built-in providers declare theirs too

A media, decision or assist adapter is Go code, not a package, but it
declares its fields the same way: `Adapter.Settings() []Setting` (the base
URL, models, key and the adapter's own params). The provider routes answer
the declaration with the values, and the console renders the one form. The
web's copies of the adapter kinds and its params allow list are deleted. A
new adapter, or a new param, is then a Go change in the adapter only.

Later: an adapter as an installable component (a provider over a wire
protocol), when a second family needs one.

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

## 5. Build order (slices of §2)

1. **The contract and its form.** `settings` in `pkgmeta` and the wire;
   the store; the widened form; stdio and HTTP MCP tools and plugins on
   `settings` (`mcp.env`, `localSettings` removed); needs setup; audit.
   Our components republish: the browser declares its Chrome and profile
   paths; media-tools declares none.
2. **A component's own page.** `settingsPage`, the sandboxed dialog and
   its host API (`get`, `set`, `check`, `close`); a plugin with a
   hand-written view in the web (local skills) moves to its own page.
3. **Endpoint components ask.** `GET /v1/component-settings`; the notifier
   and the auth provider move their deployment environment into
   `settings`.
4. **Built-in adapters declare.** `Adapter.Settings()`; the console's
   provider cards become the one form; the web's copies go.
5. **The rest.** The sandbox's and core connection's desktop fields as
   their components' settings; Fill with AI and Check.
