// Package component is what an installable component built outside this
// repository shares with the platform (docs/prd/2026-10-18-components-in-own-repos.md):
// the shapes and rules on both sides of a component contract, owned here
// once and used by the platform and the components alike.
//
//   - relay.go: the room relay's answer about the turn a room-credential
//     tool is driving;
//   - live.go: a job's live view as the tool posts it and reads it back;
//   - fence.go: the fence around text someone else wrote, for an agent;
//   - content.go: the content name rule, SKILL.md frontmatter, the
//     content bundle caps and the paths of the contract files a room's
//     records hold (the app SDK, its declarations, the content limits);
//   - mounts.go: the sandbox mounts descriptor a component writes.
//
// It imports only the standard library and core (TestComponentContractIsPublic).
// Changes are additive, like the protocol packages': a component pinned to an
// older version keeps building.
package component
