package core

import (
	"path"
	"strings"
)

// Workspace path policy. An agent workspace holds two kinds of files that
// are not the room's project: what bnbloc writes for the agent, and the
// project config a harness executes on the member's host. Both are kept out
// of everything that moves workspace files between people or machines, and
// out of reach of a sandboxed agent. These lists and their predicates are
// the one owner of that rule (TestBnblocOwnedPathAgreesEverywhere checks
// every consumer); a trailing "/" names a directory tree.

// RoomContextDir is the bridge's pull-based room context tree.
const RoomContextDir = ".room/"

// BnblocOwnedPaths are the workspace paths bnbloc writes for the agent: the
// .room/ context and the agent config a session wears (an expert's
// instructions files, skills directories, MCP config). They are the only
// paths a WorkspaceFileWriter accepts.
var BnblocOwnedPaths = []string{
	RoomContextDir,
	"CLAUDE.md",
	"AGENTS.md",
	".claude/skills/",
	".agents/skills/",
	".opencode/",
	".mcp.json",
}

// HostExecutableProjectConfigPaths are the project config files the known
// harnesses load from a workspace and act on on the member's host: Claude
// Code's .claude/ (settings with hooks, env, permissions and MCP approval;
// agents; commands; hook scripts; project memory) and CLAUDE.local.md,
// Codex's .codex/ (config.toml: MCP servers, sandbox policy), opencode's
// opencode.json(c) (plugins, MCP servers, commands). A file planted there
// runs (or is obeyed) unconfined in the owner's next own session, so no one
// but the owner writes it: never synced from a peer, never published, never
// packed into a shareable component, write-denied in sandboxed drives.
var HostExecutableProjectConfigPaths = []string{
	".claude/",
	"CLAUDE.local.md",
	".codex/",
	"opencode.json",
	"opencode.jsonc",
}

// BnblocOwnedPath reports whether a workspace-relative path is (or lies
// under) one of BnblocOwnedPaths.
func BnblocOwnedPath(rel string) bool {
	return matchWorkspacePath(BnblocOwnedPaths, normalizeWorkspacePath(rel), true)
}

// HostExecutableProjectConfig reports whether a workspace-relative path is
// (or lies under) one of HostExecutableProjectConfigPaths.
func HostExecutableProjectConfig(rel string) bool {
	return matchWorkspacePath(HostExecutableProjectConfigPaths, normalizeWorkspacePath(rel), true)
}

// ProtectedWorkspacePath reports a path that is not the room's project: a
// BnblocOwnedPath or a HostExecutableProjectConfig. Sync, deliverables,
// sites and component packing skip it.
func ProtectedWorkspacePath(rel string) bool {
	return BnblocOwnedPath(rel) || HostExecutableProjectConfig(rel)
}

// ProtectedWorkspacePaths lists both kinds (directories keep their trailing
// "/"): the sandbox's deny-write list and the per-session reset walk it.
func ProtectedWorkspacePaths() []string {
	out := make([]string, 0, len(BnblocOwnedPaths)+len(HostExecutableProjectConfigPaths))
	out = append(out, BnblocOwnedPaths...)
	return append(out, HostExecutableProjectConfigPaths...)
}

// MayHoldProtectedPath reports whether a workspace-relative directory that
// is not itself protected may hold a protected path below it (".agents"
// holds ".agents/skills/"): a walker over a workspace descends only into
// such directories.
func MayHoldProtectedPath(relDir string) bool {
	rel := normalizeWorkspacePath(relDir)
	for _, p := range ProtectedWorkspacePaths() {
		if strings.HasPrefix(strings.ToLower(p), rel+"/") {
			return true
		}
	}
	return false
}

// WorkspaceFileAllowed is the WorkspaceFileWriter path contract: a clean
// relative file path that is a BnblocOwnedPath (a file inside a listed
// directory, never the directory itself). Cores enforce it on every write;
// callers never write anywhere else.
func WorkspaceFileAllowed(rel string) bool {
	if rel == "" || strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") {
		return false
	}
	clean := path.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return matchWorkspacePath(BnblocOwnedPaths, strings.ToLower(clean), false)
}

// normalizeWorkspacePath cleans a workspace-relative path to slash form
// ("./.room/x", ".room\\x" and ".room/x" agree) and folds case: on a
// case-insensitive filesystem ".Claude/settings.json" IS the harness's
// settings file, so a peer cannot slip one past the policy by spelling.
func normalizeWorkspacePath(rel string) string {
	return strings.ToLower(strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/"))
}

// matchWorkspacePath matches a normalized path against a list. dirItself
// says whether a listed directory matches its own path.
func matchWorkspacePath(list []string, rel string, dirItself bool) bool {
	for _, p := range list {
		p = strings.ToLower(p)
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if strings.HasPrefix(rel, p) || (dirItself && rel == dir) {
				return true
			}
			continue
		}
		if rel == p {
			return true
		}
	}
	return false
}
