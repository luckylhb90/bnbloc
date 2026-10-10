# Mobile / install

The browser client works on phones and can be installed as an app (a
Progressive Web App). This is the browser path only: the desktop app embeds
the same web bundle but never installs a service worker or uses the manifest.

## Installing

- **Android (Chrome, Edge, …):** open the gateway URL, then use the browser's
  *Install app* / *Add to Home screen* menu entry.
- **iPhone / iPad (Safari):** tap **Share → Add to Home Screen**. Open bnbloc
  from the Home Screen icon afterwards — notifications only work there (see
  below).
- **Desktop browsers (Chrome, Edge):** the install icon in the address bar.

The installed app opens full-screen at the gateway root and uses the same
sign-in, rooms and settings as the browser tab.

## Notifications on a phone

Settings → Notifications → **Browser notifications on this device** turns on
web push for that device (the deployment needs the VAPID keys described in
[notifications.md](notifications.md#browser-push)). On iOS, web push is
available only in the Home Screen app (iOS 16.4 and later); in a Safari tab
the setting explains how to install instead. Tapping a notification opens the
installed app (or focuses an open tab) on the room and scrolls to the
message.

## What works offline

The app starts without a network connection: the service worker keeps the
last loaded app shell (the page and its hashed `assets/*` files). Rooms,
messages and every other piece of data come from the gateway API, which is
never cached — offline, the app shows its usual "reconnecting" state until
the connection returns. A new deploy is picked up on the next start while
online, and an open tab still shows the "New version — reload" notice.

## For operators

- The gateway serves `/manifest.webmanifest` (`application/manifest+json`,
  `no-cache`), `/icons/*`, and the service worker `/push-sw.js` (`no-cache`)
  from the web root. Installing and web push need **https** (or localhost).
- Serve the app at the origin root or a stable path prefix: the manifest
  `scope`/`start_url` and the worker scope are relative to where the web
  client is served.
- Icons in `web/public/icons/` are generated from the desktop app icon
  (`apps/desktop/src-tauri/icons`), no separate artwork:
  `icon-192.png` / `icon-512.png` resize `icon.png`; the full-bleed
  `icon-maskable-512.png` and `apple-touch-icon.png` (180 px) crop the centre
  824 px of `ios/AppIcon-512@2x.png` (inside the rounded corners, glyph within
  the maskable safe zone) and resize it, e.g. with
  `sips -c 824 824 … && sips -z 512 512 …`.
- The manifest's theme/background colour is the app's `--color-bg` token;
  `web/scripts/pwa.test.mjs` fails if they drift apart.

## How it is built

- One service worker, `web/public/push-sw.js`, handles both push and the
  shell cache (two workers on one scope would replace each other).
  `web/src/lib/appWorker.ts` is the only place that registers it, once per
  page load and never in the desktop shell.
- Cache routing (`shellRoute` in the worker): a top-level navigation to the
  app root is **network first** (the gateway serves `index.html` `no-store`;
  the cached copy answers only when the network fails); `assets/*` is
  **cache first** (content-hashed names); everything else — `/v1/*`, `/api/*`,
  SSE streams, downloads, uploads, other origins — is not handled by the
  worker at all. Caches are named `bnbloc-shell-<version>`; activating a new
  worker version deletes the others, and a new deployed `index.html` drops
  the previous build's assets.
- Phone layout: the sidebar becomes a drawer, content dialogs go full-screen
  under 640 px, room actions move to a row under the room title, page headers
  wrap their actions, and the shell follows the visual viewport on touch
  devices (`--app-height`, `web/src/lib/viewport.ts`) so the composer stays
  above the on-screen keyboard. Text fields use 16 px on iOS to stop
  focus-zoom.
