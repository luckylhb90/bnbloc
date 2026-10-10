# Admin local publisher hides selected files and delays version validation

Status: fixed (merged in 1be3204) and locally verified

## Reproduction

In Platform → Updates, select the signed `bnbloc.app.tar.gz` archive and its paired `.sig`. The native file inputs continue to display “no file selected” while a separate line shows the selected file names. The release version is on the separate Release card below the publisher, so clicking publish with an empty version fails later with a semver error.

## Root cause

`web/src/views/PlatformUpdateConfig.tsx` copies each selected `File` into React state and immediately assigns `event.currentTarget.value = ""`. That clears the browser's native selection but leaves the copied React `File` intact, creating contradictory UI state. Version and notes are outside the publish flow. Validation of the version currently occurs when `publishDesktopRelease` starts, after the user has already tried to publish.

## Fix

Replace the inline picker/publish block with the approved four-step wizard: select/validate files, enter release metadata, review gateway and policy, publish/show result. Keep native input selection aligned with the `File` held in component state; clear it only when the user explicitly replaces/discards a selected file. Validate required semver before entering upload and preserve files and step on recoverable failure.

## Verification

- `pnpm --dir web build` passed after the final result-step transition; Tauri rebuilt the internal DMG at `apps/desktop/src-tauri/target/release/bundle/dmg/bnbloc_0.1.3_aarch64.dmg` (16,623,915 bytes, SHA-256 `f93594659f70702afdd595f2c1d26ead4d5b4e2882822a5b4c2744c154c6987f`).
- Focused local tests passed: 16 across publisher preflight, release sequencing, and artifact upload; the invalid-version regression asserted that upload is never called.
- Browser smoke against the local super-admin gateway selected both files, checked that invalid signature content blocks step advancement, checked version entry, advanced to review, and published a real 16,290,389-byte signed updater archive. The local gateway accepted the upload and both Release Hub and updater settings returned 200; the UI advanced to “4. Result” and displayed SHA-256 `827243ae6249269e9849d21ba55da8638b3252d84bd153a5a92c69fed0bfc34e`. Temporary local release settings were then reset and the smoke release deleted (200/204).
- The final DMG was built with `--bundles dmg`; the updater archive/signature used for the local publishing smoke were built immediately before this final step and are unchanged because the wizard-only TypeScript/web changes do not affect the Rust app bundle code. No production gateway was changed.

## Landing commits

Pending.
