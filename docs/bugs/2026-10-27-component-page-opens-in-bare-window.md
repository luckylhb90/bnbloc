# A component page opened from an embedded app lands in a bare window (2026-10-27)

Status: fixed 2026-10-27 (web).

## What happened

bnbloc runs inside a work chat's in-app browser, registered there as one of
its apps. A member opened the connections page and pressed Connect for a
system whose sign-in asks for consent on the chat's identity page. That page
needs the in-app browser's script bridge, and failed: "Unsupported Platform".

## Why

`openComponentPage` opened `about:blank` inside the click (so no popup
blocker stops it) and pointed the window at the page's address once the
gateway answered. The host decides by the URL a window opens with: an app's
own address opens as a tab of that app, with the bridge it gives the app's
pages; `about:` opens as a bare window, with nothing. The host's log shows
every opening as `about:blank` and a bare window created for it.

## Fix

The new tab opens at once at the app's own address, `#/open-page/<component>/<page>`
(`componentPageRouteHash`), which asks the gateway for the page's address
and replaces itself with it (`views/OpeningComponentPage.tsx`). The page is
in the same tab, so it keeps what the host gave it. In a plain browser the
only difference is that the tab shows the app's spinner instead of a blank
page while it asks. The tab drops `window.opener` before it leaves for the
page. The desktop app is unchanged (system browser).

## Lesson

Opening `about:blank` and then setting its location throws away the URL a
host uses to decide what a window is. Open at a real address of your own.
