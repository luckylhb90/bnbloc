# An HTML file cannot be attached (2026-10-15)

Status: fixed (this commit).

## What happened

A member attached a generated HTML report to ask the agent to publish it.
The upload was refused, and the composer said so twice:

> report.html can't be attached: report.html can't be attached: images,
> PDF, Office and OpenDocument files, text, audio, video and zip can

## Why

1. **The type list refused HTML, SVG and XML by name** ("served from the app
   origin they would run script"). That premise stopped holding when every
   attachment came to be served the same way: anything but a sniff-verified
   raster image is an opaque `application/octet-stream` download with
   `Content-Disposition: attachment`, `nosniff` and a `sandbox` CSP
   (`writeAttachmentHeaders`). Text whose content is markup was already
   accepted under a `.txt` name, so the refusal protected nothing and only
   blocked real work (reports, diagrams, feeds).
2. **The message was prefixed twice.** The server's reason already names
   the file; the composer prefixed its own "<name> can't be attached".

## Fix

- `.html`/`.htm`, `.svg` and `.xml` are attachment types with the text
  content check. Serving is unchanged: they download as opaque files.
  Where they render, they render as agent-made files do: HTML in the
  sandboxed runner (opaque origin, no network), SVG as an `<img>` (no
  script).
- A test now holds the actual guarantee for each of them: the download is
  opaque and sandboxed whatever the stored type.
- The composer shows the server's reason as is.

## Why it was not caught

The rule was tested for what it refused, not for what members need to
attach, and the serving headers that made it safe were never tied to it.
