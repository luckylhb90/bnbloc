package contentpkg

import (
	"strings"
	"testing"
)

func promptPackFiles(t *testing.T, prompts []map[string]any, extra map[string]string) map[string]string {
	t.Helper()
	files := map[string]string{
		ManifestFile: string(manifestJSON(t, map[string]any{"kind": KindPrompt, "name": "writing", "version": "1.0.0", "displayName": "Writing", "prompts": prompts})),
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

func TestValidatePromptPack(t *testing.T) {
	prompts := []map[string]any{
		{"id": "release-notes", "title": "Release notes", "body": "Write release notes for {{ audience }} in a {{tone}} tone.",
			"variables": []map[string]any{{"name": "audience", "label": "Audience", "default": "users"}, {"name": "tone", "options": []string{"formal", "casual"}, "default": "casual"}},
			"tags":      []string{"writing"}, "slash": "/notes"},
		{"id": "summary", "title": "Summary", "body": "Summarize the thread. Literal {{not a var}} stays."},
	}
	p, err := Validate(bundle(t, promptPackFiles(t, prompts, map[string]string{"README.md": "pack"})), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Prompts) != 2 || p.Manifest.Prompts[0].Slash != "/notes" || p.Manifest.NeedsSandbox() {
		t.Fatalf("manifest = %+v", p.Manifest)
	}
	if pr, ok := p.Manifest.FindPrompt("summary"); !ok || pr.Title != "Summary" {
		t.Fatalf("find = %+v %v", pr, ok)
	}
	if got := PromptPlaceholders("{{a}} {{ b }} {{a}} {{c d}} {{1x}}"); strings.Join(got, ",") != "a,b" {
		t.Fatalf("placeholders = %v", got)
	}
	if k, ok := Lookup("prompt"); !ok || k.Kind != KindPrompt || !k.MemberUse || k.Limits != PromptLimits {
		t.Fatalf("lookup = %+v", k)
	}
	if k, _ := Lookup("expert"); k.MemberUse {
		t.Fatal("experts are picked per message, not turned on for a member")
	}
}

func TestValidatePromptPackRefuses(t *testing.T) {
	ok := func() map[string]any {
		return map[string]any{"id": "p", "title": "P", "body": "Hello {{name}}", "variables": []map[string]any{{"name": "name"}}}
	}
	with := func(edit func(m map[string]any)) []map[string]any {
		m := ok()
		edit(m)
		return []map[string]any{m}
	}
	cases := map[string]struct {
		prompts []map[string]any
		extra   map[string]string
		want    string
	}{
		"no prompts":     {nil, nil, "at least one prompt"},
		"bad id":         {with(func(m map[string]any) { m["id"] = "Bad Id" }), nil, "id \"Bad Id\""},
		"duplicate id":   {[]map[string]any{ok(), ok()}, nil, "used twice"},
		"no title":       {with(func(m map[string]any) { m["title"] = " " }), nil, "needs a title"},
		"two-line title": {with(func(m map[string]any) { m["title"] = "a\nb" }), nil, "one line"},
		"no body":        {with(func(m map[string]any) { m["body"] = ""; m["variables"] = nil }), nil, "needs a body"},
		"long body":      {with(func(m map[string]any) { m["body"] = strings.Repeat("x", maxPromptBody+1); m["variables"] = nil }), nil, "longer than"},
		"undeclared":     {with(func(m map[string]any) { m["variables"] = nil }), nil, "{{name}} is used in the body but not declared"},
		"unused":         {with(func(m map[string]any) { m["body"] = "Hello" }), nil, "declared but not used"},
		"declared twice": {with(func(m map[string]any) { m["variables"] = []map[string]any{{"name": "name"}, {"name": "name"}} }), nil, "declared twice"},
		"bad variable":   {with(func(m map[string]any) { m["body"] = "x"; m["variables"] = []map[string]any{{"name": "1x"}} }), nil, "variable name"},
		"default not option": {with(func(m map[string]any) {
			m["variables"] = []map[string]any{{"name": "name", "options": []string{"a"}, "default": "b"}}
		}), nil, "not one of its options"},
		"repeated option": {with(func(m map[string]any) {
			m["variables"] = []map[string]any{{"name": "name", "options": []string{"a", "a"}}}
		}), nil, "twice"},
		"bad slash": {with(func(m map[string]any) { m["slash"] = "notes" }), nil, "slash command"},
		"duplicate slash": {func() []map[string]any {
			a, b := ok(), ok()
			b["id"] = "q"
			a["slash"], b["slash"] = "/x", "/x"
			return []map[string]any{a, b}
		}(), nil, "slash command /x is used by"},
		"bad tag":     {with(func(m map[string]any) { m["tags"] = []string{"Bad!"} }), nil, "tag"},
		"extra file":  {[]map[string]any{ok()}, map[string]string{"notes.txt": "x"}, "holds only"},
		"skills here": {[]map[string]any{ok()}, map[string]string{"x/SKILL.md": reviewSkill}, "holds only"},
	}
	for name, c := range cases {
		_, err := Validate(bundle(t, promptPackFiles(t, c.prompts, c.extra)), "", Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	many := make([]map[string]any, maxPromptsInPack+1)
	for i := range many {
		many[i] = map[string]any{"id": "p" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + strings.Repeat("y", i/26), "title": "P", "body": "b"}
	}
	if _, err := Validate(bundle(t, promptPackFiles(t, many, nil)), "", Options{}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many prompts: %v", err)
	}
	// Other kinds refuse prompts.
	if _, err := Validate(bundle(t, expertFiles(t, map[string]any{"prompts": []map[string]any{ok()}})), "", Options{}); err == nil || !strings.Contains(err.Error(), "prompts") {
		t.Errorf("expert with prompts: %v", err)
	}
}

func TestExpertQuickPromptRef(t *testing.T) {
	refQP := []map[string]string{{"ref": "writing/release-notes"}}
	if _, err := Validate(bundle(t, expertFiles(t, map[string]any{"quickPrompts": refQP, "requires": map[string]any{"prompts": []string{"writing@^1"}}})), "", Options{}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		qp   []map[string]string
		req  []string
		want string
	}{
		"pack not required": {refQP, nil, "requires.prompts"},
		"bad ref":           {[]map[string]string{{"ref": "writing"}}, []string{"writing"}, "pack-name/prompt-id"},
		"both":              {[]map[string]string{{"ref": "writing/x", "prompt": "y", "title": "t"}}, []string{"writing"}, "not both"},
		"neither":           {[]map[string]string{{"title": "t"}}, nil, "needs a title and a prompt"},
	}
	for name, c := range cases {
		extra := map[string]any{"quickPrompts": c.qp}
		if c.req != nil {
			extra["requires"] = map[string]any{"prompts": c.req}
		}
		if _, err := Validate(bundle(t, expertFiles(t, extra)), "", Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if pack, id, ok := ParsePromptRef(" writing/release-notes "); !ok || pack != "writing" || id != "release-notes" {
		t.Fatalf("parse = %s %s %v", pack, id, ok)
	}
}

func TestBuildPromptPackForm(t *testing.T) {
	f := Form{Kind: "prompt", Name: "writing", DisplayName: "Writing", Prompts: []Prompt{
		{ID: "notes", Title: " Notes ", Body: " For {{audience}} ", Variables: []PromptVariable{{Name: "audience", Label: "Audience"}}, Slash: "/notes"},
	}}
	p, raw, err := Build(f, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || p.Manifest.Prompts[0].Title != "Notes" || p.Manifest.Prompts[0].Body != "For {{audience}}" {
		t.Fatalf("built = %+v", p.Manifest.Prompts)
	}
	f.Instructions = "x"
	if _, _, err := Build(f, "1.0.1", Options{}, nil); err == nil {
		t.Fatal("prompt pack with instructions accepted")
	}
	if _, _, err := Build(Form{Kind: "skill", Name: "k", Prompts: f.Prompts, Skills: []FormSkill{{Name: "a", Description: "d", Body: "b"}}}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("skill pack with prompts accepted")
	}
}

func TestImportPromptsMarkdown(t *testing.T) {
	md := "---\nname: team-writing\ndescription: Prompts for writing\n---\n" +
		"# Team writing\n\nShared writing prompts.\n\n" +
		"## Release notes\nslash: /notes\ntags: Writing, ops\n\nWrite notes for {{audience}}.\n\n```\n## not a heading\n```\n\n" +
		"## Release notes\n\nA second one with the same title.\n"
	p, _, err := Import([]byte(md), "prompt", "", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := p.Manifest
	if m.Name != "team-writing" || m.DisplayName != "Team writing" || m.Description != "Prompts for writing" || len(m.Prompts) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	first := m.Prompts[0]
	if first.ID != "release-notes" || first.Slash != "/notes" || strings.Join(first.Tags, ",") != "writing,ops" ||
		len(first.Variables) != 1 || first.Variables[0].Name != "audience" || first.Variables[0].Label != "Audience" ||
		!strings.Contains(first.Body, "## not a heading") {
		t.Fatalf("first = %+v", first)
	}
	if m.Prompts[1].ID != "release-notes-2" {
		t.Fatalf("second id = %q", m.Prompts[1].ID)
	}
	// A file without headings is one prompt; the name comes from the caller.
	p, _, err = Import([]byte("Explain this code to a beginner.\n"), "prompt", "explain", "1.0.0", Options{})
	if err != nil || p.Manifest.Name != "explain" || len(p.Manifest.Prompts) != 1 || p.Manifest.Prompts[0].Title != "Explain this code to a beginner." {
		t.Fatalf("plain = %+v %v", p, err)
	}
}

func TestImportPromptsJSONAndArchive(t *testing.T) {
	list := `[{"title":"Translate","prompt":"Translate to {{lang}}","variables":[{"name":"lang","options":["French","German"],"default":"French"},{"name":"unused"}],"command":"translate"},{"name":"Empty"}]`
	p, _, err := Import([]byte(list), "prompt", "lang-kit", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	pr := p.Manifest.Prompts
	if len(pr) != 1 || pr[0].ID != "translate" || pr[0].Slash != "/translate" || len(pr[0].Variables) != 1 || pr[0].Variables[0].Default != "French" {
		t.Fatalf("prompts = %+v", pr)
	}
	doc := `{"name":"doc-kit","displayName":"Doc kit","prompts":[{"id":"x","title":"X","body":"body"}]}`
	archive := zipOf(t, map[string]string{"kit/prompts.json": doc, "kit/more.md": "## Y\nbody y\n", "kit/README.md": "readme"})
	p, _, err = Import(archive, "prompt", "", "2.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Name != "doc-kit" || len(p.Manifest.Prompts) != 2 || p.Manifest.Version != "2.0.0" {
		t.Fatalf("archive = %+v", p.Manifest)
	}
	if _, ok := p.Bundle.File(ReadmeFile); !ok {
		t.Fatal("README dropped")
	}
	for name, bad := range map[string][]byte{
		"no prompts": []byte("[]"),
		"bad json":   []byte("{nope"),
		"other file": zipOf(t, map[string]string{"a.png": "x"}),
	} {
		if _, _, err := Import(bad, "prompt", "x", "1.0.0", Options{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A prompt pack package re-imports as is.
	files := promptPackFiles(t, []map[string]any{{"id": "a", "title": "A", "body": "b"}}, nil)
	if p, _, err := Import(bundle(t, files), "prompt", "", "1.2.0", Options{}); err != nil || p.Manifest.Version != "1.2.0" {
		t.Fatalf("package import = %v", err)
	}
}

func TestNormalizePrompts(t *testing.T) {
	got := NormalizePrompts([]Prompt{
		{Title: "", Body: "# Heading line\nmore", Slash: "Bad Slash"},
		{ID: "heading-line", Title: "T", Body: "{{x}}", Tags: []string{"A", "a", "b c", "bad!"}, Slash: "x"},
	})
	if got[0].Title != "Heading line" || got[0].ID != "heading-line" || got[0].Slash != "" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].ID != "heading-line-2" || got[1].Slash != "/x" || strings.Join(got[1].Tags, ",") != "a,b c" || got[1].Variables[0].Label != "X" {
		t.Fatalf("second = %+v", got[1])
	}
	if id := UniquePromptID("Heading line", got); id != "heading-line-3" {
		t.Fatalf("unique = %q", id)
	}
	if l := variableLabel("target_audience"); l != "Target audience" {
		t.Fatalf("label = %q", l)
	}
}

// A pack prompt flagged starter is offered among a room's starter prompts:
// the flag round-trips through the manifest, a json import and a markdown
// "starter: yes" line ("Gap closure" 7).
func TestPromptStarterFlag(t *testing.T) {
	files := promptPackFiles(t, []map[string]any{{"id": "a", "title": "A", "body": "b", "starter": true}, {"id": "c", "title": "C", "body": "d"}}, nil)
	p, err := Validate(bundle(t, files), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Manifest.Prompts[0].Starter || p.Manifest.Prompts[1].Starter {
		t.Fatalf("manifest prompts = %+v", p.Manifest.Prompts)
	}
	p, _, err = Import([]byte(`[{"title":"Kick off","prompt":"Plan the week","starter":true}]`), "prompt", "kit", "1.0.0", Options{})
	if err != nil || !p.Manifest.Prompts[0].Starter {
		t.Fatalf("json import = %+v %v", p, err)
	}
	p, _, err = Import([]byte("## Kick off\nstarter: yes\n\nPlan the week.\n\n## Other\nstarter: no\n\nSomething.\n"), "prompt", "kit", "1.0.0", Options{})
	if err != nil || !p.Manifest.Prompts[0].Starter || p.Manifest.Prompts[1].Starter || strings.Contains(p.Manifest.Prompts[0].Body, "starter") {
		t.Fatalf("markdown import = %+v %v", p.Manifest.Prompts, err)
	}
}

// A prompt may be scoped to files (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 2), validated by the one file-match validator.
func TestPromptFilesScope(t *testing.T) {
	ok := []map[string]any{{"id": "tighten", "title": "Tighten the wording", "body": "Tighten the wording.", "starter": true,
		"files": map[string]any{"extensions": []string{".pptx"}, "mimeTypes": []string{"application/vnd.openxmlformats-officedocument.presentationml.presentation"}}}}
	p, err := Validate(bundle(t, promptPackFiles(t, ok, nil)), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Manifest.Prompts[0].Files; f == nil || f.Extensions[0] != ".pptx" {
		t.Fatalf("files = %+v", f)
	}
	for _, bad := range []map[string]any{{}, {"mimeTypes": []string{"*/*"}}, {"extensions": []string{"pptx"}}} {
		prompts := []map[string]any{{"id": "x", "title": "X", "body": "x", "files": bad}}
		if _, err := Validate(bundle(t, promptPackFiles(t, prompts, nil)), "", Options{}); err == nil || !strings.Contains(err.Error(), "prompt x files") {
			t.Errorf("files %v: got %v, want an error naming prompt x files", bad, err)
		}
	}
}

// A prompt may be scoped to selection kinds, from the one vocabulary
// (docs/prd/2026-10-14-select-edit-review.md rule 2).
func TestPromptSelectionScope(t *testing.T) {
	ok := []map[string]any{{"id": "rewrite", "title": "Rewrite this", "body": "Rewrite it.", "starter": true, "selection": []string{"text", "block"}}}
	p, err := Validate(bundle(t, promptPackFiles(t, ok, nil)), "", Options{})
	if err != nil || len(p.Manifest.Prompts[0].Selection) != 2 {
		t.Fatalf("selection = %+v, %v", p.Manifest.Prompts, err)
	}
	for _, bad := range [][]string{{"galaxy"}, {"text", "text"}} {
		prompts := []map[string]any{{"id": "x", "title": "X", "body": "x", "selection": bad}}
		if _, err := Validate(bundle(t, promptPackFiles(t, prompts, nil)), "", Options{}); err == nil || !strings.Contains(err.Error(), "prompt x selection") {
			t.Errorf("selection %v: got %v", bad, err)
		}
	}
}
