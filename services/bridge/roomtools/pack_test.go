package roomtools

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// components save never packs a protected workspace path (the workspace path
// table, core/testdata/workspace_paths.json): room history, the agent
// config the session wears, or project config a harness executes on the
// host would otherwise ship inside a shareable component.
func TestPackDirSkipsProtectedPaths(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "core", "testdata", "workspace_paths.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Paths []struct {
			Path      string `json:"path"`
			Protected bool   `json:"protected"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, c := range table.Paths {
		p := path.Clean(c.Path)
		dir := path.Dir(p)
		if !c.Protected || dir == "." {
			continue // a root file is never under a packable directory
		}
		checked++
		ws := fakeWorkspace{p: "x", dir + "/component.json": `{}`, "drafts/x/component.json": `{}`}
		bundle, _, err := packDir(context.Background(), ws, dir)
		if err != nil {
			continue // nothing packable under dir
		}
		b, err := tarbundle.Parse(bundle, "", tarbundle.ContentLimits)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := b.File(strings.TrimPrefix(p, dir+"/")); ok {
			t.Errorf("packDir(%s) packed %s", dir, p)
		}
	}
	if checked == 0 {
		t.Fatal("the table has no protected path under a directory")
	}
	bundle, n, err := packDir(context.Background(), fakeWorkspace{"drafts/x/component.json": `{}`, "drafts/x/SKILL.md": "s"}, "drafts/x")
	if err != nil || n != 2 || len(bundle) == 0 {
		t.Fatalf("an ordinary draft: n=%d err=%v", n, err)
	}
}
