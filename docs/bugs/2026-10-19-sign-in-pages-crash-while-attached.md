# Sign-in pages crashed in the browser component's browser (2026-10-19)

Found on QA after a day of "the page keeps refreshing", "Verify does
nothing" and "nothing loads" reports from one member signing in to an HR
system through the browser component.

## What happened

- Chrome's crash reports for the component's profile show six renderer
  crashes that day, every one during that sign-in, the last two on the
  HR system's SAML return page right after the person submitted their
  one-time code.
- The component did not know a page could crash. Its calls to the page
  timed out, the keeper reported "the page does not answer", and the room's
  view kept the last picture before the crash (the code form), so the
  person saw a button that did nothing.
- Earlier guesses (throttled background windows, an unanswered dialog)
  were wrong for this; the fixes made for them stand on their own.
- With the keeper stopped (nothing attached to the page), the person signed
  in in the same window and the page went through.

## Fix (browser 0.4.10, platform contract)

- "Open on this computer" lets go of the page (hands off) while the person
  works there; readiness by the address alone; the room's view takes the
  page back on the person's next action there.
- A crash is tracked from `Inspector.targetCrashed`; calls to the page fail
  at once; the view says it and offers Reload (`LiveInputReload`); a run
  reloads once and stops at `blocked` on a second crash.
- No focus emulation.

The exact trigger (which attached call makes the renderer crash) is not
known yet; the remote path (a phone) still attaches to the page.

## Why it was not caught

- Lens: External contracts (a renderer can crash under CDP) and Defaults
  and fallbacks (a crash shown as a frozen picture).
- Missing question or test: what the view shows when the page is gone;
  checking Chrome's own crash reports before guessing at causes.
- Added to docs/design-review.md: lens 9 asks what a tab's crash looks
  like to the person.
