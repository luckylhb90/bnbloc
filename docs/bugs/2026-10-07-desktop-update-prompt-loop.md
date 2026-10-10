# Desktop update check repeats and stacks prompts

Date: 2026-10-07
Status: fixed

## Reproduction

Desktop 0.1.2 signed in to a gateway publishing 0.1.3 with update checks enabled. Within
seconds the window showed the "Desktop update available" dialog plus five stacked
"Update 0.1.3 is available" toasts (and matching system notifications).

## Root cause

`useToast()` built a new `{ success, error, info }` object on every render.
`DesktopUpdateWatcher`'s check effect listed `toast` as a dependency, so every re-render
of the watcher (setting the found update, progress, installing state) tore down and
re-ran the effect: a fresh policy fetch, a fresh check, another toast, another system
notification, another re-render. The same unstable reference affected every hook that
depended on `toast`.

Independently, every successful check of a non-forced update interrupted the user with a
modal, a toast and an OS notification at once.

## Fix

- `useToast()` returns a memoized object keyed on the stable provider callback, so hooks
  that depend on it no longer re-run on each render.
- Non-forced updates no longer open a dialog, toast or notify; they surface as a quiet
  "Update available" entry in the sidebar footer that the user clicks to open the
  install dialog (`docs/prd/2026-10-07-unobtrusive-desktop-update.md`). Forced updates
  keep the blocking dialog.
