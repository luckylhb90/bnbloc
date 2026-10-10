# Notifications never queued in production: outbox table kept its old shape

Status: fixed

## Repro

After deploying the notifier-component change (6e45b9e) on a database that had
run batch 1 (8bc43f1): `bnbloc alert …` → `500 alert failed`; mentions /
agent results / borrow requests produced no messenger notification. Only
`bnbloc components test` worked (it bypasses the outbox).

## Root cause

6e45b9e made the outbox one row per (event, notifier) by editing the existing
migration `2026_10_notification_outbox` in place (adding `notifier` and
widening the primary key), on the belief that it had never run outside dev.
It had: batch 1 was already deployed. The ledger recorded that id, so the
edited body never ran there, the table kept `PRIMARY KEY (tenant_id, id)` with
no `notifier` column, and every `INSERT … notifier …` failed. The emitter
error was swallowed (gateway alert handler had no log).

## Fix

- New migration `2026_10_notification_outbox_per_notifier`: when the table
  lacks `notifier`, replace it with the schema.sql definition (the outbox is a
  transient queue; its old rows belonged to the removed tenant-webhook
  delivery). Current-shape tables are untouched. MySQL test reproduces the old
  shape, shows the failing enqueue, and verifies the convergence.
- The gateway logs admin-alert failures.

## Prevention

A migration id that exists in any deployed commit is immutable; a schema
change always gets a new migration id. Checked every migration id present in
the deployed commits (8bc43f1, 6e45b9e, 3aacb71, 86b706a) against HEAD: this
was the only edited body.
