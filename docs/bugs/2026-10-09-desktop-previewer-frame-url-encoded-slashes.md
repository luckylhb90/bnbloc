# Desktop previewer frames requested a single percent-encoded path (2026-10-09)

Status: fixed (with the share-links / site-previews change).

## Repro

Desktop 0.1.19 with an installed previewer component (e.g. the reference
sprite-sheet previewer). Open a matching file: the previewer frame stays
blank. No previewer was installed in production yet, so nobody hit it.

## Root cause

`previewerFrameUrl` built the desktop frame URL with Tauri's
`convertFileSrc("<name>/<version>/<entry>", "bnbloc-previewer")`.
`convertFileSrc` percent-encodes its whole argument, slashes included, so the
frame loaded `bnbloc-previewer://localhost/<name>%2F<version>%2F<entry>`.
`previewer_scheme::parse_path` splits the raw path on `/`, found one segment
and refused it. Even if it had decoded, the document's relative URLs
(`main.js`) would have resolved against the scheme root. The unit test's fake
`convertFileSrc` did not encode, which hid it.

## Fix

One helper, `web/src/lib/schemeUrl.ts`: take only the scheme root from
`convertFileSrc` (it still picks the platform form, `<scheme>://localhost/`
or Windows `http://<scheme>.localhost/`) and append the path verbatim. The
artifact runner, previewer and site frames all use it; the tests fake
`convertFileSrc` the way Tauri implements it (encoding).
