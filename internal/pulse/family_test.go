package pulse

import "testing"

func TestCoreFamilies(t *testing.T) {
	cases := map[string]string{"Claude": "claude-code", " claude_code ": "claude-code", "codex": "codex",
		"opencode": "opencode", "acp": "acp", "gemini": "other", "": "other"}
	for in, want := range cases {
		if got := CoreFamily(in); got != want {
			t.Errorf("CoreFamily(%q) = %q, want %q", in, got, want)
		}
	}
}
