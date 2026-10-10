# Bug: dialog content overflowed with no scroll

Status: done
Commits: 9afa0a0

## Repro

Open a dialog with more content than the viewport (e.g. the borrow-authorization
panel listing many providers): the header is clipped, the body cannot scroll,
and the footer / Close button are unreachable.

## Root cause

The shared `Dialog` panel had no max height and no scroll container, so
tall content overflowed off-screen.

## Fix

Make the dialog a flex column capped at `max-h-[85vh]`: header and footer stay
pinned (`shrink-0`) while the body scrolls (`min-h-0 + overflow-y-auto`). Pad the
header past the close button. Applies to every `Dialog`.
