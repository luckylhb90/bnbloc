# Attachments that never reach the agent are dropped silently (2026-10-13)

Status: fixed.

## Symptom

A member sends a message with a 1.7 MB PNG to an agent on a desktop core
and asks it to read the image first. The agent answers that the message has
no attachment. The member sees the image on their message and nothing else:
no error, no notice.

## Repro

1. Run a core whose `writeFile` server caps request bodies near 2 MB and
   states no `limits.maxInputBytes` in discovery.
2. Send its agent a message with a 1.7 MB image.
3. The agent says there is no attachment; the bridge log has only a Debug
   line.

## Root cause

The bridge writes each attachment into the agent's workspace with one
binary `writeFile` request (`contentBase64`): a 1.7 MB file is a 2.3 MB
body. The core's server cut the body and answered `400 invalid json body`
(a 1.0 MB file worked, 5 MB reset the connection). `materializeAttachments`
logged the failure at Debug and skipped the file, so the turn's prompt named
no attachment, the agent truthfully saw none, and no notice reached the
room. A core without `writeFile` dropped every attachment the same way. The
protocol said nothing about how large a write must be, and a core could not
say how large a body it takes in a way the bridge read.

## Why the tests missed it

Every fake core accepted writes of any size, and no test sent an attachment
a core refuses: the only checks were that a delivered file is named in the
prompt. The conformance suite wrote only a few bytes.

## Fix

- `materializeAttachments` returns what it delivered and what it did not,
  with why (`turnAttachments`): over the core's stated limit (never fetched),
  refused by the core (its error, cut), a failed download, or a core that
  takes no files. Failures log at Warn.
- The turn's prompt names undelivered files in their own block
  (`roomlog.UndeliveredAttachment`), so the agent says which file it could
  not open and why instead of claiming there is none.
- One `attachment-undelivered` notice per job tells the member, under the
  job's own attachments session so the job's refusal or failure notice
  still posts.
- Core Protocol: a `writeFile` core accepts a file up to the chat attachment
  cap (`protocol.MinWriteFileBytes`) or states `limits.maxInputBytes`
  (`core.InputLimiter`, read by the bridge); the live conformance suite
  writes a file at the cap. `TestUndeliveredAttachmentsAreNamed`,
  `TestCheckLive_writeFile_takesAnAttachmentAtTheCap`.
- The core that capped bodies has to raise its limit, or state it, in its
  own repository.
