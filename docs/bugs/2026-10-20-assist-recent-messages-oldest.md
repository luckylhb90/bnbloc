# An assist's "recent messages" were the oldest of the page, newest first (2026-10-20)

Found while building the find-a-way assist's turn
(`docs/prd/2026-10-20-never-a-dead-end.md`), by reading the code.

## What happened

The room context of an assist (`enhance-prompt`, a schedule run's room)
reads a channel's history page of 30 rows and keeps 10 lines. The code
reversed the page because a comment said "the history answers newest
first", then kept the last 10. Realtime's history (both stores) answers
the newest page in ascending order, so the reverse made it newest first
and the cut kept the page's oldest 10 lines, in reverse order. In a busy
channel `enhance-prompt` never saw the last messages ("this", "the last
version" resolved against old ones), and the order read backwards.

## Fix

`assistRecent` keeps the page's order (oldest first) and its last 10
lines. The history read is one function now (`assistHistory`, since
renamed `roomHistory` when `read_records` began reading through it too),
which the turn reads too, and says the order it answers.

Test: `TestAssistRecentMessagesAreTheLatest` (14 messages: the prompt holds
messages 5 to 14 in order); it fails with the reverse put back.

## Why it was not caught

- Lens: External contracts (a reader assumed an order the source did not
  promise; the source is ours, which made the assumption feel safe).
- Missing question or test: the only test stored one message, where every
  order is right; no test held more messages than the cut.
- Added to docs/design-review.md: lens 9 asks whether a reader relies on
  an order, and whether a test has more rows than the reader keeps.
