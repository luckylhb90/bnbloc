package sync

import (
	"path/filepath"
	"strings"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// defaultIgnore are always excluded from sync: VCS internals, dependency and
// build outputs, and the engine's own sidecar. Syncing these causes churn and
// conflicts with no collaboration value.
var defaultIgnore = []string{
	".git", ".hg", ".svn",
	"node_modules", ".venv", "venv", "__pycache__",
	"target", "dist", "build", ".next", ".turbo",
	".DS_Store", sidecarDir,
}

// ignoreMatcher decides whether a relative path is excluded.
type ignoreMatcher struct {
	globs []string
}

func newIgnoreMatcher(extra []string) *ignoreMatcher {
	globs := append([]string{}, defaultIgnore...)
	globs = append(globs, extra...)
	return &ignoreMatcher{globs: globs}
}

// match reports whether rel (a slash-separated relative path) is ignored. A
// glob matches if it equals any path segment (directory prune) or matches the
// base name or the full relative path.
func (m *ignoreMatcher) match(rel string) bool {
	rel = filepath.ToSlash(rel)
	// The agent-config paths (an expert's instructions and skills, MCP
	// config) are bnbloc-owned per session: never part of the code a room
	// shares.
	if corepkg.IsAgentConfigPath(rel) {
		return true
	}
	segments := strings.Split(rel, "/")
	base := segments[len(segments)-1]
	for _, g := range m.globs {
		for _, seg := range segments {
			if seg == g {
				return true
			}
		}
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
	}
	return false
}
