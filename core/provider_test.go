package core

import "testing"

// SplitTargetID must invert QualifyTargetID and split only on the first slash,
// so a core-local id may itself contain slashes (acme uses "local:codex" but
// other cores' ids may embed "/").
func TestTargetIDQualifyAndSplit(t *testing.T) {
	cases := []struct{ core, raw string }{
		{"acme", "local:codex"},
		{"orbit", "agent_abc123"},
		{"future", "a/b/c"},
	}
	for _, c := range cases {
		q := QualifyTargetID(c.core, c.raw)
		gotCore, gotRaw := SplitTargetID(q)
		if gotCore != c.core || gotRaw != c.raw {
			t.Fatalf("QualifyTargetID(%q,%q)=%q -> Split=(%q,%q), want (%q,%q)",
				c.core, c.raw, q, gotCore, gotRaw, c.core, c.raw)
		}
	}
}

// A legacy id without a core prefix splits to an empty core so callers can fall
// back to a default core.
func TestSplitTargetIDLegacy(t *testing.T) {
	core, raw := SplitTargetID("local:codex")
	if core != "" || raw != "local:codex" {
		t.Fatalf("legacy split = (%q,%q), want (\"\",\"local:codex\")", core, raw)
	}
}
