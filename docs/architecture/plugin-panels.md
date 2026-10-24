# Plugin panels

A **panel** is a page of a plugin's own that the member opens from its card on
their desktop. It is the one generic way for a plugin to have a screen: the
platform names no use for it. The first plugin to use it is the agent manager
(docs/prd/2026-10-24-agent-manager.md); the same seam serves any plugin that
runs a sidecar and has something to show.

```
 the panel (a bundle the package pins; the card → Open)
      │  panel.* messages only: no network, an opaque origin
      ▼
 web host (web/src/lib/panelFrame.ts, views/panel/PanelDialog.tsx)
      │  Tauri invoke: plugin_panel_call
      ▼
 desktop relay (internal_ext/skills_provider.rs: call_raw, panel_request)
      │  loopback HTTP + the bearer the desktop keeps for this launch
      ▼
 the plugin's sidecar  (its own /v1/ API, whatever it is)
```

## Package

```json
{
  "kind": "bnbloc.plugin.package/v1",
  "name": "agent-manager",
  "sidecar": {"listenEnv": "…_LISTEN_ADDR", "tokenEnv": "…_TOKEN", "dataDirEnv": "…_DATA_DIR", "localUrl": "http://127.0.0.1:7193"},
  "panel": {"entry": "index.html", "bundle": {"url": "<gateway artifact URL>", "sha256": "<hex>"}}
}
```

Validation is `core/pkgmeta/panel.go` and the one page rule it shares with the
settings page (`validatePage`, `core/pkgmeta/settings_page.go`):

- only a plugin may declare one (`kindFields`);
- `entry` is a bundle-relative `.html` path (`ValidBundleEntry`), `bundle` a
  gateway artifact pin (an upload placeholder in a draft, never published);
- `sidecar.tokenEnv` is required: the desktop calls the sidecar with that
  bearer for the page;
- a plugin with a panel and no hook is valid: the panel is its job.

The bundle is a previewer bundle (same archive format and limits). It is pinned
in `panel.bundle` among `Package.Pins`, so the install gate checks it like any
artifact (uploaded, on this gateway, present with that sha256) and like a
frame bundle (`probePages` → `probeFrameBundle`: a valid archive holding the
entry). A member may download the bundle of a component listed to them
(`artifacts.go`, the same rule as a settings page's), by the member catalog's
`panel` block. Publishing: `bnbloc components publish-package … --panel <dir or
tar>` (uploaded and pinned like `--settings-page`).

## Serving

Only on the desktop, because only there does the sidecar run.
`bnbloc-previewer://localhost/_page/{sha256}/{path…}` serves the bundle by its
pin (`previewer_scheme.rs`; `_page` is never a package name; a settings page is
served the same way): downloaded with the member's session, sha256-verified,
kept in the previewer caches, with the previewer's frame policy (`default-src
'none'`, scripts and styles from the bundle only, `connect-src 'none'`). A page
therefore has **no inline script** and no network. A browser shows "open this in
the desktop app".

## Launch

A plugin that declares a panel gets, in the env the package names, a fresh
bearer every launch (kept by the desktop, never handed to the web) and its
private data dir (`job_env`). It runs once per signed-in account on a port the
desktop picks, as every plugin does.

## The relay

`plugin_panel_call(plugin, method, path, body)` (Tauri command, one function
`Adapter::plugin_panel_call`) is the only way a panel reaches its plugin:

- only a plugin that **declares a panel** and runs;
- `panel_request` is the one rule for what a call may be: methods `GET`,
  `POST`, `PUT`, `DELETE`; a path under `/v1/` with no empty or dot segment, no
  escape in the path, no backslash, whitespace, control character or `#`, at
  most 2048 bytes; a request body at most 1 MiB; an answer at most 4 MiB, a call
  at most 60 s;
- the call is `call_raw`, the one loopback call with the bearer (the skills
  provider's calls use it too); a loopback URL only;
- the answer is `{status, body}` whatever the status (a 409 is the plugin's
  answer); a call that never got there is an error, which the web host turns
  into `panel.refused` with its reason.

The web host checks only the message's shape (the SDK's bounds); what a path may
be is the relay's rule alone, so there is no second copy of it.

## Host protocol (protocol 1)

Defined once in `web/previewer-sdk/src/panelProtocol.ts`; the host
(`panelFrame.ts`) and the page SDK (`@bnbloc/previewer-sdk/panel`) both import
it. The host takes a message only from the panel's frame window with the opaque
origin `"null"`; the SDK only from `window.parent` with the origin `#host=` names.

| direction | message | notes |
|---|---|---|
| page → host | `panel.ready` | once, when the page's handlers are registered |
| host → page | `panel.init {protocol: 1, theme}` | the answer to ready |
| page → host | `panel.call {id, method, path, body?}` | `id` a non-negative integer the answer carries |
| host → page | `panel.result {id, status, body}` | the plugin's answer, any status |
| host → page | `panel.refused {id, reason}` | the call never reached the plugin (≤ 500 characters) |
| page → host | `panel.close` | the host closes the dialog |

Around the frame is the platform's chrome (the component's name, "This page is
the component's own.", the dialog's close), so a page cannot pose as the
console.

## Building a page

```ts
import { onPanelInit, callPanel, PanelRefused } from "@bnbloc/previewer-sdk/panel";

onPanelInit(({ theme }) => document.documentElement.dataset.theme = theme);
const { status, body } = await callPanel("GET", "/v1/state");
```

A page without a bundler is a classic script of its own file (no inline script)
speaking the protocol by hand: the agent manager's `manager/page/page.js` is the
smallest real example.

## What it is not

It is not a way for the web or a page to hold a plugin's credential, to reach
another plugin, or to call anything outside the plugin's own `/v1/`. It does not
push events to the page (a page polls its plugin). The skills view
(`skills-provider`) is a different seam with a view the web renders itself; it
publishes to the library through the web's own import, which a sandboxed page
cannot do, so it is not moved here.
