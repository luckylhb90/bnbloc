package contentbuiltin

import (
	"os"
	"path/filepath"
	"regexp"
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

// Every starter prompt is a prompt pack's prompt flagged starter (the web's
// starterChoices: the room's packs, then the built-in ones). A room
// template names prompt packs; nothing else keeps a starter list: no Go
// field or JSON tag of starter texts, no starter column, no starter list
// in the web (docs/bugs/2026-10-13-starters-and-shared-use-counts.md).
func TestOneStarterMechanism(t *testing.T) {
	goList := regexp.MustCompile(`(?i)\w*starter\w*\s*(:=|=)?\s*\[\]string\b`)
	goTag := regexp.MustCompile(`(?i)json:"(\w*starter\w*)`)
	tsList := regexp.MustCompile(`(?i)\w*starter\w*\??\s*[:=]\s*(readonly\s+)?(\[|string\[\]|Array<string>)`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(path string, src []byte) {
		for _, m := range goList.FindAll(src, -1) {
			t.Errorf("%s: %q keeps a starter list: starters are prompt packs' (flag a pack's prompt starter)", path, m)
		}
		for _, m := range goTag.FindAllSubmatch(src, -1) {
			if string(m[1]) != "starter" {
				t.Errorf("%s: JSON field %q: the only starter field is a prompt's starter flag", path, m[1])
			}
		}
	})
	for _, schema := range []string{"internal/store/schema.sql", "deploy/schema.sql"} {
		src, err := os.ReadFile(filepath.Join(archtest.Root(t), schema))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") && strings.Contains(strings.ToLower(line), "starter") {
				t.Errorf("%s:%d stores starters: %s", schema, i+1, strings.TrimSpace(line))
			}
		}
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.TS, Roots: []string{"web/src"}}, func(path string, src []byte) {
		for _, m := range tsList.FindAll(src, -1) {
			t.Errorf("%s: %q keeps a starter list: read starters through starterChoices", path, m)
		}
	})
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

// File suggestions are the same mechanism, scoped: the built-in file
// prompts are starters with files, and the room starters have none
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 2).
func TestFilePromptsAreScopedStarters(t *testing.T) {
	files, ok := Get(IDPrefix + "file-prompts")
	if !ok {
		t.Fatal("the file-prompts pack is missing")
	}
	selections := 0
	for _, pr := range files.Validated.Manifest.Prompts {
		// A file suggestion is scoped to files, a selection suggestion to
		// selection kinds (and maybe files); never neither.
		if !pr.Starter || (pr.Files == nil && len(pr.Selection) == 0) {
			t.Errorf("file prompt %s: starter %v, files %v, selection %v", pr.ID, pr.Starter, pr.Files, pr.Selection)
		}
		if len(pr.Selection) > 0 {
			selections++
		}
		if len(pr.Variables) > 0 {
			t.Errorf("file prompt %s asks for variables: a suggestion should need no form", pr.ID)
		}
	}
	if selections == 0 {
		t.Error("no selection suggestions ship")
	}
	starters, _ := Get(IDPrefix + "starters")
	for _, pr := range starters.Validated.Manifest.Prompts {
		if pr.Files != nil || len(pr.Selection) > 0 {
			t.Errorf("room starter %s is scoped to files", pr.ID)
		}
	}
}
