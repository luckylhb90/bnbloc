# A fenced code block without a language rendered as inline code (2026-10-10)

Status: fixed (web).

## Repro

Ask an agent for a diagram ("Draw a simple diagram of how an online order
reaches the kitchen"). The reply holds an ASCII box diagram in a plain
` ``` ` fence. The feed showed it as wrapped inline-code fragments: line
breaks lost, boxes torn apart.

## Root cause

`web/src/lib/markdown.tsx` decided block vs inline in the `code` component
with `!inline && lang !== null`. react-markdown v9+ no longer passes
`inline`, so only "has a language" counted. A fence without a language
(diagrams, logs, command output) became inline `<code>`, and the `pre`
override returned a bare fragment, so no `<pre>` kept the whitespace.

## Fix

`web/src/lib/markdownCode.ts` `isCodeBlock`: a newline in the text, or a
language, means a block. CommonMark turns line endings inside inline code
into spaces, while block text always ends with a newline. Blocks without a
language render verbatim (no keyword colouring). Unit tests in
`markdownCode.test.ts`.
