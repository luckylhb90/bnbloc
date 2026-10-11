# Welcome card asks to install the desktop app that is already installed

Status: fixed 2026-10-11.

## Repro

A member who has the desktop app installed opens the web app in a browser
when no platform agent is offered. The welcome card says "install the desktop
app to use your own agents" and shows the install button, whether the desktop
app is running or not.

## Root cause

Two layers:

1. `WelcomeCard` decided with `inDesktopShell()` alone: in any browser it
   offered the install, ignoring the app-wide `DesktopPresence` the sidebar and
   room banner already use.
2. `DesktopPresence` itself could not tell the three states apart. Its only
   source, `GET /v1/my/agents`, lists agents of **online** bridges, so "app
   installed but closed" and "never installed" both read as an empty list,
   and "app running with no agent core yet" read as disconnected too.

## Fix

- activity: `GET /v1/my/agents` also answers `desktop: {online, lastSeenAtMs}`
  from the member's most recent bridge heartbeat
  (`BridgeJobStore.LatestBridgeForOwner`; `lastSeenAtMs` 0 = no desktop app
  ever connected). Heartbeat rows are never deleted, so this is a durable
  "installed" signal.
- web: `DesktopPresence` exposes `installed` and derives `connected` from the
  bridge being online, not from advertised agents.
- `WelcomeCard` reads the presence:
  - **connected** → no desktop button;
  - **installed but closed** → "Open the desktop app" (the deep link), with
    the download offered only after opening fails;
  - **never connected** → install;
  - **still checking** → no button (no flash of the wrong one).
