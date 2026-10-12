# Builder format skill and built-in starter prompts (2026-10-13)

Status: in progress. Items 8 and 10 of the 2026-10-13 prompt audit (every
piece of model-facing text, its owner and its size).

## Intent

**The Expert builder (item 8).** The builder's `INSTRUCTIONS.md` is about
3.5k tokens, re-sent with every request of a builder session. Most of it is
the package format and the validator's limits, which the builder needs only
while it drafts. It also:

- forbids `requires.mcp`, `requires.prompts` and `team`, which the
  validator accepts, so the builder cannot author an expert that uses an
  MCP tool, a prompt pack or a team;
- lists harness paths (`CLAUDE.md`, `.claude/`, ...) that are
  `core/workspace_paths.go`'s to know;
- hard-codes Chinese confirmation words and a Chinese welcome sentence;
- restates every validator number in prose, and no test ties them to
  `contentpkg`;
- is copied by the web: `workshop.ts` repeats its quick prompts verbatim.

**Starter prompts (item 10).** An empty room shows two kinds of starters:
the `starter` prompts of the room's prompt packs, and the web's own
`STARTER_PROMPTS` list. Two mechanisms for one thing.

## Design

- The format, limits and examples move into an embedded skill of the
  builder, `skills/package-format/SKILL.md`, loaded on demand. The always-on
  part keeps the method, the quality bar and the rules (about 1k tokens).
- The skill's limit table is rendered from `contentpkg.Limits()`, the
  validator's own bounds as one table; `TestBuilderLimitsMatchValidator`
  fails when the file drifts (`-update` rewrites it) and when a limit is
  stated anywhere else in the builder.
- The skill teaches `requires.prompts` (quick prompt `ref`s),
  `requires.mcp` (MCP tool components by name, env names, the transport's
  permission) and `team` (roles, experts by `name@range`, default agents,
  the SOP).
- Paths are referred to generically ("files bnbloc writes for the agent and
  harness configuration"); a test fails on any path of
  `core.ProtectedWorkspacePaths()` in the builder's text.
- Language: "answer in the person's language", no hard-coded words in any
  language; `welcome` and `quickPrompts` are neutral English (an
  organization authors localized copies).
- The web reads the workshop prefills from the builder's `quickPrompts`
  (`GET /v1/content-components/cc_builtin_expert-builder`); a Go boundary
  test fails when a built-in prompt text appears in `web/src`.
- A built-in prompt pack `starters` holds the general-purpose starters, all
  flagged `starter`. The empty room shows the room's packs' starters, then
  the built-in packs' starters (in every member's reach, like every
  built-in); `STARTER_PROMPTS` is deleted.

## Acceptance

- Built-in packages validate; the builder's `INSTRUCTIONS.md` is at most
  1,500 tokens (chars/4); `TestBuilderLimitsMatchValidator` passes.
- A web test shows the empty room's starters come from the pack.
- The boundary rule fails on a copied built-in prompt.
- `scripts/ci.sh go` and `scripts/ci.sh web` pass.
