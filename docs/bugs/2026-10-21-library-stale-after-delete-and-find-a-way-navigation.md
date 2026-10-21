# The Library kept a deleted component; a find-a-way card was lost when the page left the room (2026-10-21)

Seen on a test deployment during an E2E run, by a member (here `maya`)
driving the web app through the browser component.

## What happened

1. **Stale Library.** Maya published a personal copy of the default AI
   assist `find-a-way` to her organization, then removed the organization
   publication and her personal copy through the API (both 204). The
   Library's AI assists tab kept showing the organization copy ("In your
   organization") across hash navigations into the Library; only a full
   reload with a cache-busting URL dropped it. The API was clean at once.
2. **Find a way led nowhere.** In a room, Maya clicked an agent reply's
   "Didn't work? Find a way". The card said "Finding a way…". About twelve
   seconds later the page was on the Library (AI assists tab); back in the
   room there was no card under the reply.

## Root cause

- **Stale Library.** Each Library tab read its list once on mount and again
  only when that tab itself deleted, turned on or off, or a dialog told it
  to (`onChanged`, `onSaved`, `onImported`, `PROMPT_PACKS_CHANGED`). A
  change made anywhere else (an organization admin tab, another tab, the
  API, a room tool) never reached it, and an open Library stays mounted
  across hash changes into it. Every view remembered (or forgot) to reload
  on its own.
- **The lost card.** The card lived in `useDeadEnd`'s component state.
  Leaving the room unmounted it; the lookup finished into a dead component,
  and nothing remembered it, so coming back showed no card. A failure in
  the catalog read before the assist call left the card on "Finding a way…"
  for good.
- **The navigation itself: not reproduced.** The browser's history shows
  an in-page link navigation to `#/library` at the time, while neither
  agent in the room acted on the browser. The Library opened on the AI
  assists tab, which a fresh mount does only with a queued tab, and only
  "Open in Library" on the save toast of an AI assist queues that one
  (`openInLibrary`). No code path in the room or the card navigates on its
  own; a mocked repro never left the room. The likeliest explanation is
  such a click (a person in the live view, or a toast from an earlier
  save), but it is not confirmed.

## Fix

- `web/src/lib/contentChanges.ts` is the one owner of "content changed":
  every content mutation in the API modules (`contentApi`,
  `contentSharingApi`, `promptPacksApi`, `workGalleryApi`,
  `roomComponentsApi`) is wrapped in `changing(promise, kind)` and
  announces itself once the server took it (`null` when the call names no
  kind: every list reads again). Lists subscribe with `useContentChanged`;
  the room's `content_saved` events announce too. The per-view `onChanged`
  / `onSaved` / `onImported` callbacks and `PROMPT_PACKS_CHANGED` are gone.
- `useContentLibrary` reads again on every change of its kind, every 30 s
  while visible (`useVisiblePoll`) and on a hash change into the Library;
  the latest read wins, and a failed re-read keeps the shown list.
- The find-a-way cards live in a session store (`deadEndCards`,
  `lib/deadEnd.ts`), read with `useSyncExternalStore`: a lookup still
  running when the member leaves the room shows its card when they come
  back. Every failure, including the catalog read, ends as the card's note.
  `findStep` keeps one lookup per reply at a time.

Tests: `contentChanges.test.ts` (each API mutation announces the right
kind; a scan holds every POST/PUT/DELETE in the API objects to `changing`
unless listed as quiet; a throwing listener does not stop the others);
`deadEnd.test.ts` (the store survives an unmount, stable snapshots,
`findStep`).

## Why it was not caught

- Lens: One rule, one owner (each view reloaded what it remembered to; no
  owner for "content changed") and Time and lifecycle (a request that
  outlives the view that started it).
- Missing test: no test changed content outside the tab that shows it; no
  test left the room while a lookup ran.
- Open: the navigation's trigger. If it shows again, the browser
  component's live-view input and the toast clicks around it are the first
  things to check.
