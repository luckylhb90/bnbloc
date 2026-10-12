package sync

import "testing"

// The agent-config paths a session wears (an expert's instructions and
// skills, MCP config) are never part of the shared code; ordinary files
// with similar names deeper in the tree still sync.
func TestIgnoreMatcherExcludesAgentConfig(t *testing.T) {
	m := newIgnoreMatcher(nil)
	for _, p := range []string{"CLAUDE.md", "AGENTS.md", ".claude/skills/review/SKILL.md", ".agents/skills", ".opencode/opencode.json", ".mcp.json", "node_modules/x.js"} {
		if !m.match(p) {
			t.Errorf("%s syncs", p)
		}
	}
	for _, p := range []string{"src/main.go", "docs/AGENTS.md", "README.md", ".claude/settings.json"} {
		if m.match(p) {
			t.Errorf("%s is ignored", p)
		}
	}
}
