package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The repo's sources are the tree under go.mod without dot directories,
// dependencies, build outputs or fixtures; a scan picks a language, whether
// test files count, and its roots.
func TestFiles(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"go.mod",
		"core/a.go", "core/a_test.go", "core/sub/b.go",
		"services/x/c.go", "internal/d.go",
		".claude/worktrees/x/core/a.go", ".git/e.go", ".github/tool.go",
		"node_modules/m/f.go", "apps/desktop/target/g.rs", "web/dist/h.ts",
		"core/testdata/i.go", "services/gateway/webui/static/j.ts",
		"web/src/k.ts", "web/src/l.tsx", "web/src/k.test.ts", "web/src/m.d.ts",
		"apps/desktop/src/n.rs", "README.md",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name string
		opts Opts
		want []string
	}{
		{"go", Opts{Lang: Go}, []string{"core/a.go", "core/sub/b.go", "internal/d.go", "services/x/c.go"}},
		{"go with tests", Opts{Lang: Go, Tests: true}, []string{"core/a.go", "core/a_test.go", "core/sub/b.go", "internal/d.go", "services/x/c.go"}},
		{"roots", Opts{Lang: Go, Roots: []string{"services", "internal", "services/x"}}, []string{"internal/d.go", "services/x/c.go"}},
		{"a dot root asked for", Opts{Lang: Go, Roots: []string{".github"}}, []string{".github/tool.go"}},
		{"ts", Opts{Lang: TS}, []string{"web/src/k.ts", "web/src/l.tsx", "web/src/m.d.ts"}},
		{"ts with tests", Opts{Lang: TS, Tests: true, Roots: []string{"web"}}, []string{"web/src/k.test.ts", "web/src/k.ts", "web/src/l.tsx", "web/src/m.d.ts"}},
		{"rust", Opts{Lang: Rust}, []string{"apps/desktop/src/n.rs"}},
	} {
		got, err := files(root, c.opts)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: files = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	if _, err := files(root, Opts{Lang: Go, Roots: []string{"missing"}}); err == nil {
		t.Error("a missing root must fail the scan, not read as empty")
	}
	if _, err := files(root, Opts{}); err == nil {
		t.Error("a scan without a language must fail")
	}
}
