package contentpkg

import (
	"strings"
	"testing"
)

// The limits a pattern enforces (names, slash commands, variable names)
// agree with the table guidance renders: the table's bound passes, one more
// fails.
func TestLimitsTableAgreesWithPatterns(t *testing.T) {
	max := map[string]int{}
	for _, l := range Limits() {
		if l.Field == "" || l.Max <= 0 || l.Unit == "" || l.Applies == "" {
			t.Fatalf("incomplete limit %+v", l)
		}
		max[l.Applies+"/"+l.Field] = l.Max
	}
	cases := []struct {
		key   string
		valid func(n int) bool
	}{
		{"every package/name", func(n int) bool { return ValidName(strings.Repeat("a", n)) }},
		{"prompt pack/prompts[].slash (after the /)", func(n int) bool { return slashRE.MatchString("/" + strings.Repeat("a", n)) }},
		{"prompt pack/variables[].name", func(n int) bool { return varNameRE.MatchString(strings.Repeat("a", n)) }},
		{"prompt pack/variables[].name", func(n int) bool {
			return len(PromptPlaceholders("{{"+strings.Repeat("a", n)+"}}")) == 1
		}},
	}
	for _, c := range cases {
		n, ok := max[c.key]
		if !ok {
			t.Fatalf("no limit %q", c.key)
		}
		if !c.valid(n) || c.valid(n+1) {
			t.Errorf("%s: the pattern does not stop at %d", c.key, n)
		}
	}
}
