package bridge

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// workspacePathCase is one row of core/testdata/workspace_paths.json.
type workspacePathCase struct {
	Path      string `json:"path"`
	Protected bool   `json:"protected"`
	Project   bool   `json:"project"`
}

func loadWorkspacePathTable(t *testing.T) []workspacePathCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "core", "testdata", "workspace_paths.json"))
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

// onDisk is a row's clean path and whether the filesystem checks (reset)
// use it. A mixed-case variant of a dot-directory (".Claude/…") is skipped:
// on a case-insensitive filesystem it is the same file as the lower-case
// row, and on a case-sensitive one a different file no harness loads.
func onDisk(c workspacePathCase) (string, bool) {
	p := path.Clean(c.Path)
	return p, p == strings.ToLower(p) || !strings.HasPrefix(p, ".")
}

// One path table gives one verdict everywhere in the bridge: a protected
// path (core.ProtectedWorkspacePath) is never a deliverable or site content,
// is covered by the sandbox's deny-write list, is part of the turn fence's
// fingerprint, and is cleared from a sandbox workdir by its reset; an
// ordinary project file is none of those.
// The sync ignore and save_component's packer read the same table in their
// own packages.
func TestBnblocOwnedPathAgreesEverywhere(t *testing.T) {
	const workdir = "/w/space"
	deny := protectedDenyWrite(workdir)
	covered := func(p string) bool {
		abs := strings.ToLower(workdir + "/" + p)
		for _, d := range deny {
			d = strings.ToLower(d)
			if abs == d || (strings.HasSuffix(d, "/") && strings.HasPrefix(abs, d)) {
				return true
			}
		}
		return false
	}
	scratch := t.TempDir()
	rows := loadWorkspacePathTable(t)
	// The fence fingerprint moves when a protected path is written, never
	// for a project file.
	fenced := t.TempDir()
	for _, c := range rows {
		p, disk := onDisk(c)
		if !disk {
			continue
		}
		before, err := protectedFingerprint(fenced)
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(fenced, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("planted"), 0o644); err != nil {
			t.Fatal(err)
		}
		after, err := protectedFingerprint(fenced)
		if err != nil {
			t.Fatal(err)
		}
		if c.Protected && after == before {
			t.Errorf("%s: writing it left the sandbox fence fingerprint unchanged", c.Path)
		}
		if c.Project && after != before {
			t.Errorf("%s: a project file moved the sandbox fence fingerprint", c.Path)
		}
	}
	for _, c := range rows {
		if p, ok := onDisk(c); ok {
			full := filepath.Join(scratch, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	resetProtectedPaths(scratch)

	for _, c := range rows {
		_, site := siteContentPath("", c.Path)
		p, disk := onDisk(c)
		_, statErr := os.Stat(filepath.Join(scratch, filepath.FromSlash(p)))
		kept := statErr == nil
		switch {
		case c.Protected:
			if !ignoredDeliverable(c.Path) {
				t.Errorf("%s publishes as a deliverable", c.Path)
			}
			if site {
				t.Errorf("%s is site content", c.Path)
			}
			if !covered(path.Clean(c.Path)) {
				t.Errorf("%s is writable in a sandboxed drive (deny-write %v)", c.Path, deny)
			}
			if disk && kept {
				t.Errorf("%s survives the sandbox workdir reset", c.Path)
			}
		case c.Project:
			if ignoredDeliverable(c.Path) {
				t.Errorf("%s is not published", c.Path)
			}
			if covered(c.Path) {
				t.Errorf("%s is write-denied in a sandboxed drive", c.Path)
			}
			if !kept {
				t.Errorf("%s was removed by the sandbox workdir reset", c.Path)
			}
		}
	}
}
