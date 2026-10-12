package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The sync ignore gives the workspace path table's verdict
// (core/testdata/workspace_paths.json, shared with the bridge's other
// consumers): a protected path — .room/ history, the agent config a session
// wears, project config a harness executes on the host — is never sent and
// never accepted from a peer; an ordinary project file syncs.
func TestIgnoreMatcherExcludesProtectedPaths(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "core", "testdata", "workspace_paths.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Paths []struct {
			Path      string `json:"path"`
			Protected bool   `json:"protected"`
			Project   bool   `json:"project"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	m := newIgnoreMatcher(nil)
	g := &goSync{ignore: m}
	for _, c := range table.Paths {
		_, accepted := g.acceptPath(c.Path)
		switch {
		case c.Protected:
			if !m.match(c.Path) {
				t.Errorf("%s syncs", c.Path)
			}
			if accepted {
				t.Errorf("%s is accepted from a peer", c.Path)
			}
		case c.Project:
			if m.match(c.Path) || !accepted {
				t.Errorf("%s does not sync", c.Path)
			}
		}
	}
	if !m.match("node_modules/x.js") {
		t.Error("node_modules syncs")
	}
}
