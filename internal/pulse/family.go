package pulse

import "strings"

// CoreFamily maps a provider to the agent family the board shows. Core ids are
// deployment-specific, so the family comes from the provider; anything unknown is
// "other". This is the one owner of that mapping (activity and realtime both use it).
func CoreFamily(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "claude-code", "claude_code":
		return "claude-code"
	case "codex":
		return "codex"
	case "opencode":
		return "opencode"
	case "acp":
		return "acp"
	}
	return "other"
}
