# Managed sidecar core shows offline and needs a manual "Authenticate" click

Date: 2026-10-08
Status: fixed

## Reproduction

Desktop app with an installed Core Protocol sidecar core: after start the core card
reads "offline — no reachable address"; it only starts working after clicking
"Authenticate", and later drops to offline again.

## Root cause

1. The Agents page scanned sidecar targets once on mount; at app start the sidecar was
   not `running` yet, so it found nothing and never re-scanned. "Authenticate" only
   appeared to fix it because it scheduled a re-scan 4 s later. The button was shown
   for every running sidecar, regardless of sign-in state (the sidecar's own CLI login
   was valid all along).
2. The sidecar answered target listings in 4–5 s per call (uncached catalog discovery,
   another cache refreshed under its lock), so callers with short budgets timed out.
   `internalAdapterTargets` swallowed errors, turning failures into "no agents".
3. A sidecar orphaned by an earlier desktop session (started before the package
   declared `supervisedEnv`) kept the fixed port; the compatible-listener reuse kept it
   forever, so newer artifacts and supervision never took effect.

## Fix

- Web: one shared sidecar poller (`web/src/lib/useSidecarAgents.ts`, pure rules in
  `sidecarState.ts`) re-lists targets on every state transition and while unsettled,
  shows precise states (starting / sign-in required / no agents / ready) and surfaces
  listing errors.
- Sidecar contract: optional `GET {localUrl}/v1/auth/status` →
  `{authenticated, user, checkedAtMs}` (`checkedAtMs: 0` = still checking; 404 = no
  sign-in concept). The desktop asks for sign-in only when signed out, starts it
  automatically once per session and polls until signed in.
- Desktop: spawn records (pid, artifact sha256, app pid) per sidecar; a listener that
  this app spawned in an earlier session (record or the app's own cached binary) is
  stopped and replaced; foreign listeners are never signalled.
- The sidecar (in its own repository) serves targets from a background-refreshed
  snapshot and implements the auth status endpoint.
