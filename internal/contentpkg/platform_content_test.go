package contentpkg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// The platform ships no content (docs/prd/2026-10-20-no-built-in-content.md):
// its experts, skill packs, prompt packs and assists are the default
// content, published from its own repository like any platform content.
// No content package is kept in the repository outside test data (so no
// binary can embed one), and nothing names the retired built-in scope or
// its ids.
func TestNoContentInThePlatform(t *testing.T) {
	root := archtest.Root(t)
	isContent := func(raw []byte) (string, bool) {
		var head struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &head) != nil {
			return "", false
		}
		return head.Kind, IsContentKind(head.Kind)
	}
	if out, err := exec.Command("git", "-C", root, "ls-files", "*.json").Output(); err == nil {
		for _, rel := range strings.Fields(string(out)) {
			if strings.Contains("/"+rel, "/testdata/") || strings.Contains("/"+rel, "/node_modules/") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || len(raw) > 1<<20 {
				continue
			}
			if kind, ok := isContent(raw); ok {
				t.Errorf("%s is a content package (%s): it is default content, published from its own repository", rel, kind)
			}
		}
	}
	// Nothing names the built-in scope, its ids or its source.
	self := "internal/contentpkg/platform_content_test.go"
	goNeedles := []string{"cc_" + "builtin_", "content" + "builtin", "ContentScope" + "Builtin", "BuiltinContent" + "IDPrefix"}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Tests: true}, func(rel string, src []byte) {
		for _, n := range goNeedles {
			if rel != self && strings.Contains(string(src), n) {
				t.Errorf("%s names %q: there is no built-in content", rel, n)
			}
		}
	})
	tsNeedles := []string{"cc_" + "builtin_", "isBuiltin" + "(", `scope === "` + `builtin"`, `scope: "` + `builtin"`}
	archtest.Scan(t, archtest.Opts{Lang: archtest.TS, Tests: true, Roots: []string{"web/src"}}, func(rel string, src []byte) {
		for _, n := range tsNeedles {
			if strings.Contains(string(src), n) {
				t.Errorf("%s names %q: there is no built-in content (platform scope is the default content)", rel, n)
			}
		}
	})
}

// Every starter prompt is a prompt pack's prompt flagged starter (the web's
// starterChoices: the room's packs, then the packs offered in every room).
// A room package names prompt packs; nothing else keeps a starter list: no
// Go field or JSON tag of starter texts, no starter column, no starter list
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
