# Installable components live in their own repositories (2026-10-18)

Status: decided with the product owner and built (2026-10-18).

## Intent

An installable component (an MCP tool, a plugin sidecar, a previewer, a
core adapter, a sandbox runtime) is maintained in a repository of its own,
like `bnbloc-acp` already is. The platform repository holds the platform and
the public contracts components build against; it holds no component's code.

The product owner: "组件都放单独仓库维护吧，代码直接拆了吧 … review还有没有类似逻辑、
模块，都拆出去吧，不要污染主仓库".

## What moves

| Was here | Repository | What it is |
|---|---|---|
| `cmd/bnbloc-browser`, `packages/browser` | `luckylhb90/bnbloc-browser` | MCP tool: a room agent operates web pages |
| `cmd/bnbloc-media-tools`, `packages/media-tools` | `luckylhb90/bnbloc-media-tools` | MCP tool: image generation, transcription |
| `cmd/bnbloc-local-skills`, `internal/localskills`, `packages/local-skills` | `luckylhb90/bnbloc-local-skills` | plugin sidecar: a member's own skills in the sandbox and library |
| `examples/previewers/*` | `luckylhb90/bnbloc-previewers` | previewer bundles: Word, Excel, PowerPoint, Mermaid, draw.io, sprite sheets |

Each keeps its history (`git filter-repo` over the paths), its package
manifest, its tests and its CI, and releases on its own version.

What stays, and why:

- `cmd/bnbloc-vmexec`: generic Sandbox Protocol tooling the platform's
  hybrid execution defines; any sandbox runtime package ships it as a file.
- ~~`internal/contentbuiltin`: built-in content embedded in the binaries by
  design (scope `builtin`), versioned with the release.~~ Superseded
  2026-10-20 by docs/prd/2026-10-20-no-built-in-content.md: the content
  left the platform too, for its own repository (`bnbloc-default-content`),
  published at platform scope like any content.
- The host side of a component contract: Settings → Skills
  (`web/src/lib/localSkills.ts`, `apps/desktop/.../skills_provider.rs`), the
  previewer host and SDK (`web/previewer-sdk`), the room relay, the live view.
- `adapter/`: the library a host runtime links to report activity.

## The public contracts (`component/`)

A component outside this module cannot import `internal/`. What the moved
components used from it is a contract, so it moves to a public package,
`github.com/luckylhb90/bnbloc/component`, and the platform uses it from
there (one owner, not a copy):

- the room relay's session (`RelaySession`, `RelaySessionPath`; the env
  names stay in `core/pkgmeta`);
- the live view (`LiveFramePost`, `LiveFrameReply`, `LiveInput` and its
  kinds, the frame bounds);
- the fence around untrusted text a tool returns (`Fence`);
- the content name rule and SKILL.md frontmatter (`ValidName`,
  `ParseFrontmatter`) and the content bundle caps (`ContentMaxBytes`,
  `ContentMaxFiles`);
- the sandbox mounts descriptor (`MountsFile`, `SandboxMountsContract`).

It imports only the standard library and `core`. `TestComponentContractIsPublic`
keeps `internal/` out of it, and `TestNoComponentCodeInThePlatform` keeps
component code from coming back.

The previewers depend on `@bnbloc/previewer-sdk` from this repository
(`web/previewer-sdk`, a pnpm git dependency pinned to a commit), and bring
their own esbuild and TypeScript.

## Design review

1. **Actors.** Publishers (the super-admin running `publish-mcp`,
   `publish-package`, `previewers publish`) build from the component repo
   instead of this one; members see no change. The internal build scripts
   point at the new checkouts.
2. **Time and lifecycle.** Installed packages keep running: the package names,
   versions, env names and artifacts do not change. A contract change now
   lands here first, then in a component at a new pinned version.
3. **Scale and cost.** Four more repositories with their own CI. The
   platform's CI drops the previewer stage (its installs were the slowest
   part of `scripts/ci.sh web`).
4. **Failure and recovery.** A component pinned to an older contract keeps
   building; the gateway still validates every post and package, so a stale
   component is refused, never trusted.
5. **Boundaries and privacy.** All five repositories are public, like this
   one; the pre-push leak hook is configured in each checkout.
6. **Abuse.** No new surface: the contracts were already wire formats.
7. **One owner.** Each moved rule has one owner in `component/`; the platform
   and the components both use it. Formats a component writes in another
   language (a previewer's tar) are clients of the platform's validator, as
   before.
8. **Defaults.** None change.
9. **External contracts.** `component/` is now an import path others build
   against: changes to it follow the protocol packages' rule (additive, or a
   new name).
10. **Load on users and agents.** None.
11. **Release.** No platform behaviour changes; nothing to migrate. Package
    versions are not bumped for the move.
12. **Observability.** Unchanged.
13. **AI capability.** Not applicable: a repository layout.
14. **Component or platform.** This is the lens applied to the code layout.

Pre-mortem: a component's tests depended on platform internals (the bridge's
mounts validation, the web's unit diff); those checks become contract tests on
the component side, against the public types.
