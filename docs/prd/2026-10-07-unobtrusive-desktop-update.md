# Unobtrusive desktop update notice

Date: 2026-10-07
Status: implemented

## Intent

An available, optional desktop update must not interrupt work. The user decides when to
look at it.

## Behavior

- Optional update found by the background check: no dialog, no toast, no OS notification.
  The sidebar footer (bottom-left) shows an "Update <version>" entry; in the collapsed
  sidebar it is an icon with a dot. Clicking opens the existing install dialog
  ("Later" closes the dialog; the entry stays until the app is updated).
- Forced update (current version below the configured minimum): unchanged, the blocking
  dialog opens immediately.
- `autoInstall` policy: unchanged, installs without prompting.
- Manual "Check for updates" in Agents → Desktop: unchanged.

## Acceptance

- One check per policy interval; no repeated prompts for the same version.
- Optional update shows only the sidebar entry; clicking it opens the dialog.
- Forced update still blocks with the dialog.
