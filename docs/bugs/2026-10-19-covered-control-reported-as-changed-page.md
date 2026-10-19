# A covered control was reported as a changed page

Date: 2026-10-19. Component: browser 0.5.2. Fixed in 0.5.3.

## What happened

An agent was taking a 15-question quiz on a learning site in the browser
component. Question 1 went through. On question 2, every click on an
answer came back "the page changed before the action, so nothing was
done". The agent retried four times, then tried to leave and reopen the
page (the site asked "Leave this page?") and stopped to ask the person.

## Why

- The run's window was small (a 512×347 viewport). The answer's center sat
  at the bottom edge, under a bar the page fixes to the window's bottom.
- The executor hit-tests a control's center before input. It scrolled
  only a control whose center was outside the window, so this one was not
  scrolled, the hit test found the bar, and the input was refused.
- That refusal shared `errStale` with a real page change, so the agent was
  told the page had changed. It had no way to know what to do differently.
- The run's history recorded each click before it ran, so the four
  refused clicks read as four clicks.

## Fix

- `targetJS` scrolls a covered control to the window's middle, then uses
  any uncovered point of it or of its label (a 3×3 grid, center first).
- A control that is there but cannot take input says why (`blockedError`):
  disabled, hidden, read-only, does not fit, or covered by what (the
  covering element's label and tag). The agent is told that, not "the page
  changed". A control that is gone is still `errStale`.
- The history records an action only after it ran.
- Real-Chrome test: `TestCoveredControlOnChrome` (a bar over the answer is
  scrolled clear and clicked; an overlay is named).

## Lesson

Two different failures must not share one message. A refusal that tells
the agent the wrong cause makes it retry the same thing: say what blocks
the step.
