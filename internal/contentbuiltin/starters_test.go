package contentbuiltin

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// The empty room's general-purpose starters are a built-in prompt pack:
// the web shows the starter prompts of the built-in packs after the room's
// own, one mechanism for every starter.
func TestStartersShipAsStarterPromptPack(t *testing.T) {
	p, ok := Get(IDPrefix + "starters")
	if !ok {
		t.Fatal("the starters pack is missing")
	}
	if p.Component.Kind != contentpkg.KindPrompt || p.Component.Scope != store.ContentScopeBuiltin {
		t.Fatalf("starters component = %+v", p.Component)
	}
	prompts := p.Validated.Manifest.Prompts
	if len(prompts) < 3 {
		t.Fatalf("%d starters, want at least 3", len(prompts))
	}
	for _, pr := range prompts {
		if !pr.Starter {
			t.Errorf("prompt %s is not flagged starter", pr.ID)
		}
		if len(pr.Variables) > 0 {
			t.Errorf("starter %s asks for variables: a first message should need no form", pr.ID)
		}
	}
}

// Built-in prompt texts live only in their packages: the web reads the
// builder's quick prompts (the workshop prefills) and the starters from the
// content API, never from a copy that drifts. A copy is a needle of a
// built-in text's first characters in any web source.
func TestBuiltinPromptsStayOutOfTheWeb(t *testing.T) {
	const needleLen = 40
	var needles []string
	for _, p := range All() {
		m := p.Validated.Manifest
		texts := []string{}
		for _, q := range m.QuickPrompts {
			texts = append(texts, q.Prompt)
		}
		for _, pr := range m.Prompts {
			texts = append(texts, pr.Body)
		}
		for _, s := range texts {
			s = strings.TrimSpace(s)
			if len(s) > needleLen {
				s = s[:needleLen]
			}
			if len(s) >= 16 {
				needles = append(needles, s)
			}
		}
	}
	if len(needles) == 0 {
		t.Fatal("no built-in prompt texts found")
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.TS, Tests: true, Roots: []string{"web/src"}}, func(path string, src []byte) {
		for _, n := range needles {
			if strings.Contains(string(src), n) {
				t.Errorf("%s copies the built-in prompt %q: read it from the built-in component instead", path, n)
			}
		}
	})
}
