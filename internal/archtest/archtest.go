// Package archtest is the one way an architecture test reads the repo's own
// source files. Every static scan ("no file outside the owner spells X")
// lists its files here, so all scans agree on what the repo's sources are:
// the tree under the directory holding go.mod, without dot directories (git
// worktrees nested under .claude/ once made a scan read other branches'
// copies), dependencies, build outputs or test fixtures.
package archtest

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Lang selects the source files a scan reads.
type Lang string

const (
	Go   Lang = "go"
	TS   Lang = "ts"
	Rust Lang = "rust"
)

// langs gives each language its file suffixes and the suffixes that mark
// one of its test files.
var langs = map[Lang]struct{ exts, tests []string }{
	Go:   {exts: []string{".go"}, tests: []string{"_test.go"}},
	TS:   {exts: []string{".ts", ".tsx"}, tests: []string{".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"}},
	Rust: {exts: []string{".rs"}},
}

// skipNames are directories never read, at any depth, besides every dot
// directory: dependencies, build outputs and test fixtures.
var skipNames = map[string]bool{"node_modules": true, "target": true, "dist": true, "testdata": true}

// skipPaths are build outputs at a fixed place (slash paths from the root).
var skipPaths = map[string]bool{"services/gateway/webui/static": true}

// Opts chooses the files of a scan.
type Opts struct {
	Lang Lang
	// Tests includes the language's test files (Go: _test.go).
	Tests bool
	// Roots limits the scan to these slash paths under the repo root
	// (e.g. "services", "internal"); none means the whole repo. A root is
	// read even when its own name would be skipped (".github").
	Roots []string
}

// Root is the absolute repo root: the nearest directory at or above the
// working directory that holds go.mod.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("archtest: no go.mod at or above the working directory")
		}
		dir = parent
	}
}

// Files lists the repo's source files o selects, as sorted slash paths
// relative to the repo root.
func Files(t testing.TB, o Opts) []string {
	t.Helper()
	out, err := files(Root(t), o)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Scan calls fn with every file Files lists and its contents.
func Scan(t testing.TB, o Opts, fn func(rel string, src []byte)) {
	t.Helper()
	root := Root(t)
	rels, err := files(root, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		fn(rel, src)
	}
}

func files(root string, o Opts) ([]string, error) {
	lang, ok := langs[o.Lang]
	if !ok {
		return nil, errors.New("archtest: unknown language " + string(o.Lang))
	}
	roots := o.Roots
	if len(roots) == 0 {
		roots = []string{"."}
	}
	var out []string
	for _, r := range roots {
		start := filepath.Join(root, filepath.FromSlash(r))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if p != start && (strings.HasPrefix(d.Name(), ".") || skipNames[d.Name()] || skipPaths[rel]) {
					return filepath.SkipDir
				}
				return nil
			}
			if hasSuffix(rel, lang.exts) && (o.Tests || !hasSuffix(rel, lang.tests)) {
				out = append(out, path.Clean(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}

func hasSuffix(s string, suffixes []string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}
