# The browser component closed the member's browser from inside the agent's sandbox (2026-10-19)

Found on QA, within the hour of browser 0.4.8.

## What happened

0.4.8 added "a browser an older version started is closed and started
again with this version's flags, when it shows no web page". The member's
browser had run since the day before without those flags, and showed no
page, so the first call closed it. The call ran in the agent's sandbox
(`bnbloc-vmexec` confinement), which denies writes outside the workspace
and temp directories:

- the new browser could not write its profile and exited at once;
- the earlier browser's `DevToolsActivePort` could not be removed, and
  `startChrome` read it as the new browser's;
- the endpoint record named a browser that was gone, and every later call
  failed with "connection refused". The agent could do nothing about it.

The member's browser was started again from outside the sandbox by hand.

## Fix (browser 0.4.9)

- A browser is closed only by a process that can write its profile
  (`profileWritable`); elsewhere an outdated browser is used as it is.
- Starting a browser where the profile is not writable fails with that
  reason; an earlier port file that cannot be removed is an error, never
  read as the new browser's.

Test: `TestConfinedProcessKeepsTheBrowser` (a read-only profile stands for
the sandbox, whose `deny file-write*` keeps the profile out).

## Why it was not caught

- Lens: Release and compatibility (and External contracts: the runtime a
  component's process runs under).
- Missing question or test: the restart was tested from an unconfined test
  process only; the component's calls run inside the agent's sandbox, which
  cannot do what the test process could.
- Added to docs/design-review.md: lens 11 now asks what the change does
  when it runs where it will really run (an agent's sandbox, a member's
  machine), and never to stop something that cannot be started again from
  there.
