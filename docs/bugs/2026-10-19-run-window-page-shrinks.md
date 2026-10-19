# A run window's page shrank until nothing fit

Date: 2026-10-19. Component: browser 0.5.4. Recovered in 0.5.5, root cause fixed in 0.6.1.

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
- **Root cause (found later the same day).** Pictures were captured with
  `Page.captureScreenshot` and a clip with a scale (one image pixel per
  CSS pixel, at most 1024 wide). Chrome applies that scale by resizing the
  page for the capture and restoring it after. Two captures at once (the
  room's live view and a step's picture, from two sessions) restored each
  other's scaled size, so the page kept it. The sizes fit exactly: 1200 ×
  1024/1200/2 = 512 (the 1024-wide cap on a 2× screen), then 0.5 each
  time, 256 and 128; heights 813 → 347 → 173 → 87. On a test Chrome, two
  sessions capturing at once shrank a 1120×693 page to 1024×634.

## Fix

- Chrome now captures at the screen's pixels with no clip or scale, and
  the picture is scaled in the component (an average of the pixels each
  target pixel covers, in the JPEG's own YCbCr planes: about 35 ms for a
  2400×1626 picture). Browser 0.6.1. Real-Chrome test:
  `TestCapturesAtOnceKeepThePageSizeOnChrome`.
- Kept as recovery for a page already shrunk (0.5.5): when the component attaches to a run's tab, it compares the page's
  size with its window's. A page under half the window's width, or half
  its height without the window's own bars, is laid out again by
  resizing the window by one pixel and back (`fitViewport`). A minimized
  window is left alone. Resizing the live window by hand that way brought
  the page back to 1200×813 at once, and the agent submitted question 3
  in the next step.
- Tests: `TestShrunkViewport` (the rule), and a real-Chrome check that a
  page that fills its window is left alone.

## Lessons

- When the agent repeats odd moves (scroll, Escape, Tab) on a page a
  person reads fine, compare what the page reports about itself (its
  size, scale and visibility) with the window holding it before blaming
  the agent.
- When a size changes by exact factors, look for the code that computes
  that factor: here the screenshot's scale, which explained 512, 256 and
  128 to the pixel. A recovery that hides the symptom is not the fix.
