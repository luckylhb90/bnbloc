package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// WorkspacePathCase is one row of testdata/workspace_paths.json, the path
// table every consumer of the workspace path policy is checked against.
type workspacePathCase struct {
	Path      string `json:"path"`
	Protected bool   `json:"protected"`
	Writable  bool   `json:"writable"`
	Project   bool   `json:"project"`
}

func loadWorkspacePathTable(t *testing.T) []workspacePathCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "workspace_paths.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Paths []workspacePathCase `json:"paths"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	return table.Paths
}

func TestWorkspacePathTable(t *testing.T) {
	for _, c := range loadWorkspacePathTable(t) {
		if got := ProtectedWorkspacePath(c.Path); got != c.Protected {
			t.Errorf("ProtectedWorkspacePath(%q) = %v, want %v", c.Path, got, c.Protected)
		}
		if got := WorkspaceFileAllowed(c.Path); got != c.Writable {
			t.Errorf("WorkspaceFileAllowed(%q) = %v, want %v", c.Path, got, c.Writable)
		}
	}
}

// The workspace path policy has one owner (workspace_paths.go). Production
// code elsewhere never decides "is this under .room/ / a harness's config
// dir" with its own prefix check; it calls the predicates.
func TestWorkspacePathPolicySingleOwner(t *testing.T) {
	forbidden := regexp.MustCompile(`HasPrefix\([^)]*(RoomContextDir|"\.(room|claude|codex|agents|opencode)/)`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if rel == "core/workspace_paths.go" {
			return
		}
		if m := forbidden.Find(src); m != nil {
			t.Errorf("%s decides a workspace path kind itself (%s): use core.BnblocOwnedPath / HostExecutableProjectConfig / ProtectedWorkspacePath", rel, m)
		}
	})
}
