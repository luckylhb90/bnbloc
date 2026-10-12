package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCleanRelPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"a/b.txt", "a/b.txt", true},
		{"./a/b.txt", "a/b.txt", true},
		{"a//b.txt", "a/b.txt", true},
		{`a\b.txt`, "a/b.txt", true},
		{"nested/deep/file.go", "nested/deep/file.go", true},
		{"", "", false},
		{".", "", false},
		{"./", "", false},
		{"..", "", false},
		{"a/../b", "", false}, // any ".." segment is rejected outright
		{"a/../../escape", "", false},
		{"/abs/path", "", false},
		{"/etc/passwd", "", false},
		{`..\windows`, "", false},
		{`a\..\..\escape`, "", false},
		{`C:\Windows\system32`, "", false},
		{"c:relative", "", false},
		{`\\host\share\x`, "", false},
	}
	for _, c := range cases {
		got, ok := CleanRelPath(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CleanRelPath(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	// The strict callers (workspace file writes, handoff entries) accept
	// only a path already clean.
	for _, p := range []string{".room/x.md", ".room//x.md", "./.room/x.md", `.room\x.md`} {
		if got := WorkspaceFileAllowed(p); got != (p == ".room/x.md") {
			t.Errorf("WorkspaceFileAllowed(%q) = %v", p, got)
		}
	}
}

// A supplied relative path is checked only by CleanRelPath (and bundle
// paths by ValidBundlePath): no other non-test file tests for a "../"
// prefix or a ".." path by hand (four copies drifted: only one refused
// Windows drive letters).
func TestCleanRelPathSingleOwner(t *testing.T) {
	owners := map[string]bool{"core/relpath.go": true, "core/profile.go": true}
	forbidden := regexp.MustCompile(`"\.\./"|[=!]= "\.\."`)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "target", ".git", "web", "apps":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if owners[rel] {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if s := strings.TrimSpace(line); strings.HasPrefix(s, "//") {
				continue
			}
			if forbidden.MatchString(line) {
				t.Errorf("%s:%d checks a relative path by hand (%s): use core.CleanRelPath", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
