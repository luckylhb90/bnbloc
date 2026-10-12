package contentpkg

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

func manifestJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func bundle(t *testing.T, files map[string]string) []byte {
	t.Helper()
	in := map[string][]byte{}
	for k, v := range files {
		in[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(in, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const reviewSkill = "---\nname: review\ndescription: Review a diff for correctness. Use when asked to review.\n---\n\nCheck every changed line.\n"

func expertFiles(t *testing.T, extra map[string]any) map[string]string {
	m := map[string]any{"kind": KindExpert, "name": "reviewer", "version": "1.0.0", "displayName": "Reviewer",
		"quickPrompts": []map[string]string{{"title": "Review my diff", "prompt": "Review the latest diff."}}, "welcome": "Hi, I review code."}
	for k, v := range extra {
		m[k] = v
	}
	return map[string]string{
		ManifestFile:              string(manifestJSON(t, m)),
		InstructionsFile:          "You are a meticulous reviewer.\n",
		"skills/review/SKILL.md":  reviewSkill,
		"skills/review/notes.txt": "x",
	}
}

func TestValidateExpert(t *testing.T) {
	p, err := Validate(bundle(t, expertFiles(t, nil)), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "You are a meticulous reviewer.\n" || len(p.Skills) != 1 || p.Skills[0].Name != "review" || p.Scripts {
		t.Fatalf("package = %+v", p)
	}
	if p.Skills[0].Description != "Review a diff for correctness. Use when asked to review." {
		t.Fatalf("description = %q", p.Skills[0].Description)
	}
	ps := p.ProfileSkills()
	if len(ps) != 1 || len(ps[0].Files) != 2 || ps[0].Files[0].Path != "SKILL.md" || string(ps[0].Files[0].Content) != reviewSkill {
		t.Fatalf("profile skills = %+v", ps)
	}
	if p.Manifest.NeedsSandbox() {
		t.Fatal("expert without scripts needs a sandbox")
	}
}

func TestValidateSkillPack(t *testing.T) {
	files := map[string]string{
		ManifestFile:             string(manifestJSON(t, map[string]any{"kind": KindSkill, "name": "review-kit", "version": "0.2.0", "skills": []string{"review"}, "requires": map[string]any{"permissions": []string{"scripts"}}})),
		"review/SKILL.md":        reviewSkill,
		"review/scripts/lint.sh": "#!/bin/sh\necho ok\n",
		"README.md":              "kit",
	}
	p, err := Validate(bundle(t, files), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Scripts || !p.Manifest.NeedsSandbox() || len(p.Skills) != 1 || len(p.Skills[0].Files) != 2 {
		t.Fatalf("package = %+v", p)
	}
}

func TestValidateRefuses(t *testing.T) {
	cases := map[string]func(map[string]string){
		"no manifest":        func(f map[string]string) { delete(f, ManifestFile) },
		"no instructions":    func(f map[string]string) { delete(f, InstructionsFile) },
		"empty instructions": func(f map[string]string) { f[InstructionsFile] = " \n" },
		"stray top-level":    func(f map[string]string) { f["notes.md"] = "x" },
		"skill name mismatch": func(f map[string]string) {
			f["skills/review/SKILL.md"] = strings.Replace(reviewSkill, "name: review", "name: other", 1)
		},
		"skill without desc": func(f map[string]string) { f["skills/review/SKILL.md"] = "---\nname: review\n---\nbody" },
		"loose skill file":   func(f map[string]string) { f["skills/loose.md"] = "x" },
		"undeclared scripts": func(f map[string]string) { f["skills/review/scripts/run.sh"] = "echo" },
		"non utf8":           func(f map[string]string) { f[InstructionsFile] = "\xff\xfe" },
	}
	for name, mutate := range cases {
		f := expertFiles(t, nil)
		mutate(f)
		if _, err := Validate(bundle(t, f), "", Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	manifestCases := map[string]map[string]any{
		"unknown kind":       {"kind": "bnbloc.other.package/v1"},
		"bad name":           {"name": "Reviewer!"},
		"bad version":        {"version": "1.0"},
		"v version":          {"version": "v1.0.0"},
		"unknown permission": {"requires": map[string]any{"permissions": []string{"network"}}},
		"stdio without mcp":  {"requires": map[string]any{"permissions": []string{"mcp.stdio"}}},
		"bad skill ref":      {"requires": map[string]any{"skills": []string{"x@^banana"}}},
		"unknown field":      {"surprise": true},
		"skills on expert":   {"skills": []string{"review"}},
		"multiline display":  {"displayName": "a\nb"},
		"empty quick prompt": {"quickPrompts": []map[string]string{{"title": "x"}}},
	}
	for name, extra := range manifestCases {
		if _, err := Validate(bundle(t, expertFiles(t, extra)), "", Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// References must name an MCP tool component (when the caller can look
	// them up: a save), only its variables, with the transport's permission.
	tools := Options{MCPTool: func(n string) (MCPToolInfo, bool) {
		switch n {
		case "browser":
			return MCPToolInfo{Transport: "stdio", Env: []string{"BROWSER_TOKEN"}}, true
		case "search":
			return MCPToolInfo{Transport: "http", Host: "tools.example.com", Env: []string{"TOOLS_TOKEN"}}, true
		}
		return MCPToolInfo{}, false
	}}
	mcpReq := func(mcp []map[string]any, perms ...string) map[string]any {
		return map[string]any{"requires": map[string]any{"mcp": mcp, "permissions": perms}}
	}
	for name, c := range map[string]struct {
		extra map[string]any
		want  string
	}{
		"known stdio tool":         {mcpReq([]map[string]any{{"name": "browser", "env": []string{"BROWSER_TOKEN"}}}, "mcp.stdio"), ""},
		"known http tool":          {mcpReq([]map[string]any{{"name": "search"}}, "mcp.http:tools.example.com"), ""},
		"unknown tool":             {mcpReq([]map[string]any{{"name": "ghost"}}, "mcp.stdio"), "not an MCP tool component"},
		"undeclared env":           {mcpReq([]map[string]any{{"name": "browser", "env": []string{"OTHER"}}}, "mcp.stdio"), "not one of its variables"},
		"stdio without permission": {mcpReq([]map[string]any{{"name": "browser"}}, "scripts"), `needs the "mcp.stdio" permission`},
		"http wrong host":          {mcpReq([]map[string]any{{"name": "search"}}, "mcp.http:other.example.com"), `"mcp.http:tools.example.com"`},
		"named twice":              {mcpReq([]map[string]any{{"name": "browser"}, {"name": "browser"}}, "mcp.stdio"), "named twice"},
	} {
		_, err := Validate(bundle(t, expertFiles(t, c.extra)), "", tools)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// Reading stored content checks references by syntax only: a tool
	// uninstalled since never makes it invalid.
	if _, err := Validate(bundle(t, expertFiles(t, mcpReq([]map[string]any{{"name": "ghost"}}, "mcp.stdio"))), "", Options{}); err != nil {
		t.Fatalf("stored content with a gone tool: %v", err)
	}
	// The digest is verified.
	if _, err := Validate(bundle(t, expertFiles(t, nil)), strings.Repeat("0", 64), Options{}); err == nil {
		t.Fatal("wrong digest accepted")
	}
}

func TestBuildFormAndCarryOver(t *testing.T) {
	f := Form{Kind: "expert", Name: "reviewer", DisplayName: "Reviewer", Instructions: "Be strict.",
		QuickPrompts: []QuickPrompt{{Title: "Go", Prompt: "Review it"}},
		Skills:       []FormSkill{{Name: "review", Description: "Use when: reviewing a diff", Body: "Check it."}}}
	p, raw, err := Build(f, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "Be strict.\n" || p.Skills[0].Description != "Use when: reviewing a diff" || len(raw) == 0 {
		t.Fatalf("package = %+v", p)
	}
	// A form edit on a base keeps the base's extra skill files.
	files := expertFiles(t, nil)
	base, err := Validate(bundle(t, files), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := Build(f, "1.0.1", Options{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Bundle.File("skills/review/notes.txt"); !ok {
		t.Fatalf("carried files = %v", next.Bundle.Paths())
	}
	if next.Manifest.Version != "1.0.1" {
		t.Fatalf("version = %s", next.Manifest.Version)
	}
	// A skill pack form.
	sp, _, err := Build(Form{Kind: "skill", Name: "kit", Skills: []FormSkill{{Name: "a", Description: "does a", Body: "a"}, {Name: "b", Description: "does b", Body: "b"}}}, "1.0.0", Options{}, nil)
	if err != nil || len(sp.Skills) != 2 || len(sp.Manifest.Skills) != 2 {
		t.Fatalf("skill pack = %+v, %v", sp, err)
	}
	if _, _, err := Build(Form{Kind: "skill", Name: "kit", Instructions: "x", Skills: []FormSkill{{Name: "a", Description: "d"}}}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("skill pack with instructions accepted")
	}
	if _, _, err := Build(Form{Kind: "skill", Name: "kit"}, "1.0.0", Options{}, base); err == nil {
		t.Fatal("kind change accepted")
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestImportSkillDirectory(t *testing.T) {
	arch := tarGz(t, map[string]string{
		"pdf-tools/SKILL.md":              "---\nname: PDF Tools\ndescription: >\n  Fill and split PDFs.\n  Use for any PDF task.\n---\nSteps.\n",
		"pdf-tools/scripts/split.py":      "print(1)",
		"pdf-tools/reference/forms.md":    "forms",
		"pdf-tools/.DS_Store":             "junk",
		"__MACOSX/pdf-tools/._SKILL.md":   "junk",
		"pdf-tools/references/limits.txt": "x",
	})
	p, _, err := Import(arch, "skill", "", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := p.Manifest
	if m.Name != "pdf-tools" || len(m.Skills) != 1 || m.Skills[0] != "pdf-tools" || !m.NeedsSandbox() {
		t.Fatalf("manifest = %+v", m)
	}
	if p.Skills[0].Description != "Fill and split PDFs. Use for any PDF task." {
		t.Fatalf("description = %q", p.Skills[0].Description)
	}
	if _, ok := p.Bundle.File("pdf-tools/scripts/split.py"); !ok {
		t.Fatalf("paths = %v", p.Bundle.Paths())
	}
}

func TestImportPluginAsSkillPackAndExpert(t *testing.T) {
	plugin := map[string]string{
		".claude-plugin/plugin.json": `{"name":"Review Suite","description":"Code review helpers"}`,
		"skills/review/SKILL.md":     reviewSkill,
		"skills/style/SKILL.md":      "---\nname: style\ndescription: Style rules\n---\nUse tabs.\n",
		"agents/code-reviewer.md":    "---\nname: code-reviewer\ndescription: Reviews code like a senior engineer\n---\nYou review code. Be direct.\n",
		"commands/review.md":         "Review the diff",
		"README.md":                  "readme",
	}
	sp, _, err := Import(zipOf(t, plugin), "skill", "", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sp.Manifest.Name != "review-suite" || len(sp.Skills) != 2 || sp.Manifest.Description != "Code review helpers" {
		t.Fatalf("skill pack = %+v", sp.Manifest)
	}
	ex, _, err := Import(zipOf(t, plugin), "expert", "", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Manifest.Name != "code-reviewer" || strings.TrimSpace(ex.Instructions) != "You review code. Be direct." || len(ex.Skills) != 2 || ex.Manifest.Description != "Reviews code like a senior engineer" {
		t.Fatalf("expert = %+v / %q", ex.Manifest, ex.Instructions)
	}
	// An expert import without instructions explains what is missing.
	if _, _, err := Import(zipOf(t, map[string]string{"skills/review/SKILL.md": reviewSkill}), "expert", "x", "1.0.0", Options{}); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportBnblocPackageAndRefusals(t *testing.T) {
	files := expertFiles(t, nil)
	in := map[string]string{}
	for k, v := range files {
		in["reviewer-1.0.0/"+k] = v
	}
	p, _, err := Import(tarGz(t, in), "expert", "", "2.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Version != "2.0.0" || p.Manifest.Name != "reviewer" {
		t.Fatalf("manifest = %+v", p.Manifest)
	}
	if _, _, err := Import(tarGz(t, in), "skill", "", "1.0.0", Options{}); err == nil {
		t.Fatal("expert package imported as a skill pack")
	}
	if _, _, err := Import(zipOf(t, map[string]string{"my skill/SKILL.md": reviewSkill}), "skill", "", "1.0.0", Options{}); err == nil {
		t.Fatal("unsafe file name accepted")
	}
	if _, _, err := Import([]byte("not an archive at all"), "skill", "", "1.0.0", Options{}); err == nil {
		t.Fatal("garbage accepted")
	}
	// An archive bomb stops at the cap.
	big := strings.Repeat("a", tarbundle.ContentLimits.MaxBytes+1)
	if _, _, err := Import(zipOf(t, map[string]string{"SKILL.md": reviewSkill, "huge.txt": big}), "skill", "", "1.0.0", Options{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bomb err = %v", err)
	}
}

func TestParseFrontmatter(t *testing.T) {
	fm, body := ParseFrontmatter("---\nname: \"x: y\"\ndescription: |\n  line one\n  line two\nallowed-tools: Read\n---\nBody\n")
	if fm["name"] != "x: y" || fm["description"] != "line one\nline two" || fm["allowed-tools"] != "Read" || body != "Body\n" {
		t.Fatalf("fm = %#v body = %q", fm, body)
	}
	if fm, body := ParseFrontmatter("no frontmatter"); len(fm) != 0 || body != "no frontmatter" {
		t.Fatalf("plain = %#v %q", fm, body)
	}
}

func TestReferencesAndLookup(t *testing.T) {
	if r, err := ParseReference("review@^1.2"); err != nil || r.Name != "review" || r.Range != "^1.2" {
		t.Fatalf("ref = %+v %v", r, err)
	}
	if _, err := ParseReference("Bad@1"); err == nil {
		t.Fatal("bad name accepted")
	}
	if k, ok := Lookup("expert"); !ok || k.Kind != KindExpert {
		t.Fatal("lookup by short name")
	}
	if k, ok := Lookup(KindSkill); !ok || k.Short != "skill" {
		t.Fatal("lookup by kind")
	}
	if len(Kinds()) != 3 {
		t.Fatalf("kinds = %+v", Kinds())
	}
}
