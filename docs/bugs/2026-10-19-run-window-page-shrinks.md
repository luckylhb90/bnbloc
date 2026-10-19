# A run window's page shrank until nothing fit

Date: 2026-10-19. Component: browser 0.5.4. Fixed in 0.5.5.

## What happened

An agent taking a quiz in the browser component stopped making progress
on question 3: for four minutes it scrolled, pressed Escape, Tab and
Enter, and the room's live view showed a white page. The replay's
pictures of those steps were blank, 256 pixels wide.

## Why

- The run's tab lives in its own window, opened behind the member's
  others (`Target.createTarget` with `newWindow` and `background`).
- That window was 1200×900, but its page was laid out at 512×347 at
  question 2 and at 128×87 at question 3 (`innerWidth`/`innerHeight`;
  the visual viewport's scale was 1, so it was not a pinch zoom). The
  member's other window on the same page was laid out at 1200×813.
- On a 128×87 page the Submit button was off the page, so the agent had
  nothing to click. Pictures of the page were 128×87 of white.
- Why Chrome shrank that window's page is not known. It halved twice
  while the window stayed behind others on macOS.

## Fix

- When the component attaches to a run's tab, it compares the page's
  size with its window's. A page under half the window's width, or half
  its height without the window's own bars, is laid out again by
  resizing the window by one pixel and back (`fitViewport`). A minimized
  window is left alone. Resizing the live window by hand that way brought
  the page back to 1200×813 at once, and the agent submitted question 3
  in the next step.
- Tests: `TestShrunkViewport` (the rule), and a real-Chrome check that a
  page that fills its window is left alone.

## Lesson

When the agent repeats odd moves (scroll, Escape, Tab) on a page a person
reads fine, compare what the page reports about itself (its size, scale
and visibility) with the window holding it before blaming the agent.
