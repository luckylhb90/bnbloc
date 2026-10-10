package realtimehttp

import "testing"

func TestParseAgentRefs_sandboxedSubset(t *testing.T) {
	refs := parseAgentRefs("acme/claude,acme/codex,acp:hermes", "ACME/claude, acp:hermes")
	if len(refs) != 3 || !refs[0].Sandboxed || refs[1].Sandboxed || !refs[2].Sandboxed {
		t.Fatalf("refs = %+v", refs)
	}
	if refs := parseAgentRefs("", "x/y"); refs != nil {
		t.Fatalf("no advertisement, refs = %+v", refs)
	}
}
