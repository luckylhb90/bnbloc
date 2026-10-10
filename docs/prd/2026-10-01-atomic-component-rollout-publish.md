# Atomic component rollout publish

Date: 2026-10-01  
Status: implemented — the canonical publish path sets rollout atomically; the
temporary `/v1/platform/core-packages` compatibility route named below was
removed in the Task 6 clean cutover and now 404s.

## Intent

Close the residual Task 5 backend gap before Task 6 by making the canonical
component publish endpoint set the initial rollout policy as part of the same
install lifecycle. The UI must not publish first and rely on a compensating
rollout update to reach the requested distribution state.

## Requirements

- `POST /v1/platform/components` accepts a typed `rollout` field with values
  `auto`, `optin`, or `off`.
- Missing rollout defaults to `auto`.
- Invalid rollout fails before any extension record is persisted.
- Requested rollout is persisted by `extpipe.Install` before the install returns
  success; activation failure leaves no persisted record.
- The temporary `/v1/platform/core-packages` compatibility route (kept only
  until the Task 6 web cutover) is gone; the component route is the sole
  implementation.
- The upload UI relies on the atomic publish response and no longer uninstalls a
  newly-published component to compensate for rollout-update failure. The
  follow-up rollout `PUT` remains only for editing an existing component card.

## Acceptance

- Tests cover publish default `auto`, publish `optin`, publish `off`, invalid
  rollout with no record, and activation failure with no record.
- Focused gateway/extpipe tests, Go formatting/vet, and web type/build checks
  are run before handoff.
