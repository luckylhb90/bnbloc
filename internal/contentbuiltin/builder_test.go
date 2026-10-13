package contentbuiltin

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

var updateLimits = flag.Bool("update", false, "rewrite the limit table of the builder's package-format skill from contentpkg.Limits")

// The builder's on-demand format skill: the package format and every limit
// (SKILL.md), and an example package per kind (references/examples.md).
// INSTRUCTIONS.md keeps only what always applies.
const (
	formatSkill     = "package-format"
	formatSkillFile = "skills/" + formatSkill + "/SKILL.md"
	examplesFile    = "skills/" + formatSkill + "/references/examples.md"
	limitsBegin     = "<!-- limits:begin (rendered from contentpkg.Limits) -->\n"
	limitsEnd       = "<!-- limits:end -->"
	// maxAlwaysOnTokens bounds the builder's INSTRUCTIONS.md, re-sent with
	// every request of a builder session (tokens as chars/4).
	maxAlwaysOnTokens = 1500
)

func builder(t *testing.T) *Package {
	t.Helper()
	p, ok := Get(BuilderID)
	if !ok {
		t.Fatalf("builder %s missing; have %d built-ins", BuilderID, len(All()))
	}
	return p
}

func builderFile(t *testing.T, name string) string {
	t.Helper()
	raw, ok := builder(t).Validated.Bundle.File(name)
	if !ok {
		t.Fatalf("the builder has no %s", name)
	}
	return string(raw)
}

func TestBuilderShipsAndValidates(t *testing.T) {
	p := builder(t)
	if p.Component.Scope != store.ContentScopeBuiltin || p.Component.Kind != contentpkg.KindExpert || p.Component.Owner != "" {
		t.Fatalf("builder component = %+v", p.Component)
	}
	if p.Version.BundleBlobKey != BundleKeyPrefix+p.Version.SHA256 || p.Version.CreatedBy != Author {
		t.Fatalf("builder version = %+v", p.Version)
	}
	// The served bytes are the validated bundle, re-validated by the same
	// validator a bridge uses on download.
	raw, ok := Bundle(p.Version.BundleBlobKey)
	if !ok {
		t.Fatal("bundle not served by key")
	}
	pkg, err := contentpkg.Validate(raw, p.Version.SHA256, contentpkg.Options{})
	if err != nil {
		t.Fatalf("served bundle does not validate: %v", err)
	}
	if pkg.Manifest.NeedsSandbox() || len(pkg.Manifest.Permissions()) != 0 {
		t.Fatal("the builder must declare no permissions")
	}
	for _, want := range []string{"save_component", "get_component", "try_draft", "publish_component", "drafts/<name>/", "Save as prompt pack", "starter", "`" + formatSkill + "`", "person's language"} {
		if !strings.Contains(pkg.Instructions, want) {
			t.Errorf("builder instructions do not mention %q", want)
		}
	}
	var skill *contentpkg.Skill
	for i := range pkg.Skills {
		if pkg.Skills[i].Name == formatSkill {
			skill = &pkg.Skills[i]
		}
	}
	if skill == nil {
		t.Fatalf("the builder does not embed the %s skill", formatSkill)
	}
	format := builderFile(t, formatSkillFile)
	for _, want := range []string{contentpkg.KindExpert, contentpkg.KindSkill, contentpkg.KindPrompt, "requires.prompts", "`mcp`", "`team`", contentpkg.PermMCPStdio, contentpkg.PermMCPHTTPPrefix, contentpkg.PermScripts, "`starter: true`", "`ref`"} {
		if !strings.Contains(format, want) {
			t.Errorf("the %s skill does not mention %q", formatSkill, want)
		}
	}
	if _, ok := Bundle("builtin/0000"); ok {
		t.Fatal("unknown built-in key served")
	}
	if _, ok := Bundle(strings.TrimPrefix(p.Version.BundleBlobKey, BundleKeyPrefix)); ok {
		t.Fatal("a plain blob key must not resolve to a built-in bundle")
	}
}

// The builder's always-on part stays small: the format lives in the
// on-demand skill.
func TestBuilderAlwaysOnBudget(t *testing.T) {
	if tokens := len(builder(t).Validated.Instructions) / 4; tokens > maxAlwaysOnTokens {
		t.Fatalf("the builder's INSTRUCTIONS.md is about %d tokens, the budget is %d: move what is needed only while drafting into the %s skill", tokens, maxAlwaysOnTokens, formatSkill)
	}
}

// The format skill's reference carries example packages; they must validate with the
// one validator, or the builder teaches agents a format that fails on save.
func TestBuilderExamplesValidate(t *testing.T) {
	re := regexp.MustCompile("(?s)<!-- example: (\\S+) -->\\s*```[a-z]*\\n(.*?)\\n```")
	byPkg := map[string]map[string][]byte{}
	for _, m := range re.FindAllStringSubmatch(builderFile(t, examplesFile), -1) {
		parts := strings.SplitN(m[1], "/", 3) // drafts/<name>/<file>
		if len(parts) != 3 || parts[0] != "drafts" {
			t.Fatalf("example path %q is not drafts/<name>/<file>", m[1])
		}
		if byPkg[parts[1]] == nil {
			byPkg[parts[1]] = map[string][]byte{}
		}
		byPkg[parts[1]][parts[2]] = []byte(m[2] + "\n")
	}
	kinds := map[string]bool{}
	team := false
	for name, files := range byPkg {
		raw, err := tarbundle.PackFiles(files, tarbundle.ContentLimits)
		if err != nil {
			t.Fatalf("%s: pack: %v", name, err)
		}
		pkg, err := contentpkg.Validate(raw, "", contentpkg.Options{})
		if err != nil {
			t.Fatalf("example %s does not validate: %v", name, err)
		}
		if pkg.Manifest.Name != name {
			t.Fatalf("example %s names %q", name, pkg.Manifest.Name)
		}
		kinds[pkg.Manifest.Kind] = true
		team = team || pkg.Manifest.Team != nil && len(pkg.Manifest.Requires.Prompts) > 0 && len(pkg.Manifest.Requires.MCP) > 0
	}
	if !kinds[contentpkg.KindExpert] || !kinds[contentpkg.KindSkill] || !kinds[contentpkg.KindPrompt] || !team {
		t.Fatalf("example kinds = %v, team expert with prompt packs and an MCP tool = %v", kinds, team)
	}
}

// renderLimits is the format skill's limit table, from the validator's own
// table.
func renderLimits() string {
	var b strings.Builder
	b.WriteString("| Applies to | Field | At most |\n|---|---|---|\n")
	for _, l := range contentpkg.Limits() {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", l.Applies, l.Field, limitAmount(l))
	}
	return b.String()
}

func limitAmount(l contentpkg.Limit) string {
	if l.Unit == contentpkg.UnitBytes {
		switch {
		case l.Max%(1<<20) == 0:
			return fmt.Sprintf("%d MiB", l.Max>>20)
		case l.Max%(1<<10) == 0:
			return fmt.Sprintf("%d KiB", l.Max>>10)
		}
	}
	return fmt.Sprintf("%d %s", l.Max, l.Unit)
}

// statedLimitRE finds a limit stated in prose ("at most 120", "8000
// characters", "64 KiB"): every number the builder states lives in the
// rendered table.
var statedLimitRE = regexp.MustCompile(`(?i)\b(at most|up to|no more than|maximum( of)?|limit(ed)? (of|to))\s+\d|\b\d[\d,.]*\s*(characters|chars|bytes|KiB|MiB|KB|MB|files|entries|items|tags|skills|prompts|variables|options|roles|members)\b`)

var fenceRE = regexp.MustCompile("(?s)```.*?```")

// Every limit the builder states equals the validator's: the format
// skill's table is rendered from contentpkg.Limits (-update rewrites it),
// and no limit is stated anywhere else in the builder.
func TestBuilderLimitsMatchValidator(t *testing.T) {
	format := builderFile(t, formatSkillFile)
	start := strings.Index(format, limitsBegin)
	end := strings.Index(format, limitsEnd)
	if start < 0 || end < start {
		t.Fatalf("the %s skill has no %q ... %q region", formatSkill, strings.TrimSpace(limitsBegin), limitsEnd)
	}
	want := renderLimits()
	if got := format[start+len(limitsBegin) : end]; got != want {
		if !*updateLimits {
			t.Fatalf("the %s skill's limit table differs from contentpkg.Limits; run go test ./internal/contentbuiltin -run TestBuilderLimitsMatchValidator -update (and bump the builder's version)\ngot:\n%s\nwant:\n%s", formatSkill, got, want)
		}
		path := filepath.Join("packages", "expert-builder", filepath.FromSlash(formatSkillFile))
		if err := os.WriteFile(path, []byte(format[:start+len(limitsBegin)]+want+format[end:]), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", path)
	}
	prose := map[string]string{
		contentpkg.InstructionsFile: builder(t).Validated.Instructions,
		formatSkillFile:             format[:start] + format[end:],
		examplesFile:                builderFile(t, examplesFile),
		contentpkg.ManifestFile:     builderFile(t, contentpkg.ManifestFile),
	}
	for name, text := range prose {
		for _, m := range statedLimitRE.FindAllString(fenceRE.ReplaceAllString(text, ""), -1) {
			t.Errorf("%s states a limit outside the rendered table: %q", name, m)
		}
	}
}

// No built-in names a workspace path policy of its own: what bnbloc writes
// for the agent and what a harness executes are core's lists
// (core/workspace_paths.go), and the builder's drafts are never in them.
func TestBuilderNamesNoWorkspacePaths(t *testing.T) {
	for _, p := range All() {
		for _, f := range p.Validated.Bundle.Paths() {
			raw, _ := p.Validated.Bundle.File(f)
			for _, path := range core.ProtectedWorkspacePaths() {
				if strings.Contains(string(raw), path) {
					t.Errorf("%s/%s names the workspace path %q: refer to bnbloc's and the harness's files generically", p.Component.Name, f, path)
				}
			}
		}
	}
	for _, rel := range []string{"drafts/contract-reviewer/component.json", "drafts/contract-reviewer/INSTRUCTIONS.md", "drafts/contract-reviewer/skills/clause-check/SKILL.md"} {
		if core.ProtectedWorkspacePath(rel) {
			t.Errorf("the draft path %s is protected: it would never be packed", rel)
		}
	}
}

// Built-in content hard-codes no human language besides its neutral
// English: the agent answers in the person's language, and an organization
// authors localized copies.
func TestBuiltinsHardCodeNoLanguage(t *testing.T) {
	for _, p := range All() {
		for _, f := range p.Validated.Bundle.Paths() {
			raw, _ := p.Validated.Bundle.File(f)
			for _, r := range string(raw) {
				if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Cyrillic, unicode.Arabic) {
					t.Errorf("%s/%s hard-codes %q", p.Component.Name, f, r)
					break
				}
			}
		}
	}
}

// The workshop (web/src/lib/workshop.ts) opens with the builder's quick
// prompts: per kind the one titled "Create … <kind>", and for an existing
// component the one titled "Improve …", whose first blank names it.
func TestBuilderQuickPromptsServeTheWorkshop(t *testing.T) {
	qps := builder(t).Validated.Manifest.QuickPrompts
	for _, k := range contentpkg.Kinds() {
		if k.Snapshot {
			continue // a work is published from a deliverable, not drafted in the workshop
		}
		n := 0
		for _, q := range qps {
			title := strings.ToLower(q.Title)
			if strings.HasPrefix(title, "create") && strings.Contains(title, k.Short) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d quick prompts titled \"Create … %s\", want 1", n, k.Short)
		}
	}
	n := 0
	for _, q := range qps {
		if strings.HasPrefix(strings.ToLower(q.Title), "improve") {
			n++
			if !strings.Contains(q.Prompt, "___") {
				t.Errorf("the improve quick prompt %q has no ___ blank for the component", q.Prompt)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d quick prompts titled \"Improve …\", want 1", n)
	}
}
