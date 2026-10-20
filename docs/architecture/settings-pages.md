# A component's own settings page

A component that declares settings (`docs/prd/2026-10-19-component-settings.md`)
gets the one settings form by default. One whose settings need more than
fields (folder lists, a sign-in, fields shown by a choice, a live preview)
may ship its own page instead: an HTML file of a static bundle the platform
shows in the settings dialog where the form would be (PRD §2.2a). The
declaration stays the contract; the page is only another way to edit it.

A settings page is a previewer bundle in all but its use: the same archive
format and limits, served by the same code, under the same sandbox
(`docs/architecture/previewer-components.md`). It never gets network
access, runs in an opaque origin, and reads and saves its settings only
through the host, one scope at a time.

## Package

```json
{
  "kind": "bnbloc.mcp.package/v1",
  "name": "notes",
  "settings": [
    {"key": "apiBase", "label": "API base URL", "type": "url", "scope": "platform", "required": true},
    {"key": "apiKey", "label": "API key", "type": "secret", "scope": "org", "required": true}
  ],
  "settingsPage": {
    "entry": "index.html",
    "bundle": {"url": "<gateway artifact URL>", "sha256": "<hex>"}
  }
}
```

Validation (`core/pkgmeta/settings_page.go`, at install):

- only on a kind that may declare `settings` (a plugin, an MCP tool of
  either runtime), and only beside a non-empty `settings`;
- `entry`: a bundle-relative `.html` path (`pkgmeta.ValidBundleEntry`, the
  previewer's entry rule);
- `bundle`: a gateway artifact pin, url and sha256, or an upload
  placeholder (`{"url": "", "sha256": ""}`) in a draft, which is never
  published (`ValidateCompleteArtifacts`).

**Why the bundle is pinned in `settingsPage` and not in `artifacts`.** A
previewer's bundle is its package's one artifact, `artifacts.bundle`. A
plugin's and a stdio tool's `artifacts` are per-platform binaries
(`darwin-arm64`, `<platform>/<file>`), which the desktop, the MCP assembly
and the sidecar validation all read as platforms, and an http tool has no
artifacts at all. The page bundle keeps the artifact shape (url + sha256 of
a gateway artifact) beside its entry instead. `Package.Pins` lists every
pin of a package (`artifacts.<key>` and `settingsPage.bundle`), so the
install gate checks it like any artifact: uploaded, on this gateway's
artifact route, present in the store with that sha256
(`probeUploadedArtifacts`); and then like a previewer's bundle: a valid
archive within the bundle limits holding `entry` (`probeFrameBundle`).

## Serving

**Browser.** A scope's settings view (`GET /v1/platform/components/{name}/settings`,
`GET /v1/tenant/components/{name}/settings`) carries the page for that
scope:

```json
"page": {"entry": "index.html", "bundleSha256": "<hex>",
         "frameUrl": "/v1/settings-pages/mcp/notes/1.0.0/<ticket>/", "frameExpiresAtMs": 1791546426000}
```

`GET /v1/settings-pages/{kind}/{name}/{version}/{ticket}/{path…}`
(`services/gateway/settings_pages.go`) serves the bundle's files. The ticket
is a 1-hour `settings-page-frame` token bound to the subject, tenant and
`<scope>:<kind>/<name>@<version>`. Every request re-checks that the
component is installed at exactly that version and that the ticket's holder
may still set the scope (the platform's: `manage_platform`; an
organization's: `manage_org` and the component visible to the
organization). The files are served by the previewer's code
(`serveFrameFile`): from the sha256-verified archive, with the frame policy
(`frameCSP`, below), `nosniff`, `no-referrer`, and cached until the ticket
expires. Anything else is a 404. The ticket is redacted from request logs.

**Desktop.** `bnbloc-previewer://localhost/_settings/{sha256}/{path…}`
(`apps/desktop/src-tauri/src/previewer_scheme.rs`; `_settings` is never a
package name). The shell downloads the archive by its pin from the
gateway's artifact route with the member's session, refuses a sha256
mismatch, keeps it in the previewer caches and serves it with the same
policy. The gateway decides whether the session may read it: a super admin
any key, anyone else the page bundle of a component listed to them (an
organization admin sets up an opt-in tool before anyone installed it).
Serving by pin rather than through the member catalog lets the platform's
admin set up a component the catalog does not list to them.

The policy of every file, as a previewer's:

```
Content-Security-Policy: default-src 'none'; script-src 'self' <bundle base>;
  style-src 'self' <bundle base> 'unsafe-inline'; img-src blob: data:;
  media-src blob: data:; font-src 'self' <bundle base> data:;
  connect-src 'none'; frame-ancestors 'self'
```

## The dialog

The settings dialog (`web/src/views/settings/ComponentSettingsDialog.tsx`)
shows one section per scope the viewer may set. For a component with a
page, each section shows the page (`SettingsPageFrame.tsx`) instead of the
one form, editing that section's scope only: a member's own on the desktop
(their values stay on their computer), the organization's for an org admin,
the platform's for a super admin. Around the frame is the platform's
chrome: the component's name, the scope, "This page is the component's
own", and Close, so a page cannot pose as the console. The frame is the
previewer's: `<iframe sandbox="allow-scripts">` (never
`allow-same-origin`), `referrerPolicy="no-referrer"`, its src the desktop
scheme URL or the gateway frame URL plus `#host=<app origin>`.

## Host protocol (protocol 1)

Every message is a `postMessage` object with a `type`, defined once in
`web/previewer-sdk/src/settingsProtocol.ts`, which the host
(`web/src/lib/settingsPageFrame.ts`) and the page SDK
(`@bnbloc/previewer-sdk/settings`) both import. The host takes a message
only from the page's frame window with the opaque origin `"null"`
(`fromFrame`, the previewer's rule); the SDK only from `window.parent` with
the origin named in `#host=`.

| direction | message | notes |
|---|---|---|
| page → host | `settings.ready` | once, when the page's handlers are registered |
| host → page | `settings.init {protocol: 1, theme, scope}` | the answer to ready: `light`/`dark`, the scope the page edits |
| page → host | `settings.get {id}` | `id` a non-negative integer the answer carries |
| host → page | `settings.view {id, view}` | the answer to get, and to a save done |
| page → host | `settings.set {id, values, clear?}` | values by key (`""` clears; a secret left out keeps the stored one); clear names stored values to remove |
| host → page | `settings.refused {id, reason, key?}` | a save (or read) refused: the platform's reason (≤ 500 characters) and the setting it names |
| page → host | `settings.check {id}` | the component's own check (PRD §2.5) |
| host → page | `settings.checked {id, supported}` | `supported: false` until checks are built |
| page → host | `settings.close` | the host closes the dialog |

A view:

```json
{
  "scope": "org",
  "fields": [
    {"key": "apiKey", "label": "API key", "type": "secret", "required": true, "set": true},
    {"key": "pageSize", "label": "Page size", "type": "number", "required": false, "set": true, "value": "50"}
  ],
  "readOnly": [{"key": "apiBase", "label": "API base URL", "type": "url", "scope": "platform", "set": false}],
  "missing": ["apiBase"]
}
```

`fields` are the settings of the scope being edited, a secret only as
`set` (never a value); `readOnly` the other scopes' settings the host knows
of, only as set or not (a member's view: every other scope's; an admin's:
those still missing); `missing` the required settings not set. `type` and
`scope` are the platform's words, and a reader keeps a word it does not
know: a page shows an unknown type as text.

What the host enforces (`settingsPageHost`):

- a page edits one scope, through that scope's calls: the settings routes
  for the platform's and an organization's (`PUT …/settings`, checked by
  `pkgmeta.MergeSettings`, sealed and audited like a form's save), the
  desktop's member store for a member's own (checked by its pinned copy);
- a save naming a key that is not a field of the scope (another scope's,
  an undeclared one) is refused before anything is sent;
- a refusal of the platform comes back with its reason and the key it
  names (the settings routes answer `{"error", "field"}`, and so does the
  desktop's store); a key outside the page's fields is never echoed;
- nothing a page receives holds a secret's value, and it reaches no other
  component's values: the views are built per component and scope.

## Building a page

```ts
import { onSettingsInit, getSettings, setSettings, closeSettings, SettingsRefused } from "@bnbloc/previewer-sdk/settings";

onSettingsInit(({ theme }) => document.documentElement.dataset.theme = theme);
render(await getSettings());
save.onclick = async () => {
  try {
    render(await setSettings({ apiBase: url.value }));
  } catch (e) {
    if (e instanceof SettingsRefused) showAt(e.key, e.message);
  }
};
```

Bundle it as a previewer is bundled (classic or module scripts with
relative URLs, no inline script, no network). The smallest example, written
against the protocol by hand, is `core/pkgmeta/testdata/settings-page/`:
the gateway tests serve it and the web host's tests drive it.

## Publishing

```sh
bnbloc cores publish-mcp --package mcp.json [--binary <platform>=<path>...] --settings-page dist/settings
bnbloc components publish-package --package plugin.json --binary <platform>=<path> --settings-page settings.tar
```

The package declares `settingsPage` with an upload placeholder bundle.
`--settings-page` takes the built directory (packed with the previewer's
deterministic packing) or an archive; it is checked before anything is
uploaded (within the bundle limits, holding `entry`), then uploaded through
`POST /v1/platform/artifacts` and pinned into `settingsPage.bundle`, and the
package is signed and published as usual. A package that declares a page
without the flag, or the flag without a declared page, is refused before
any upload.
