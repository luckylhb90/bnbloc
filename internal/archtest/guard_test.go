package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestRepoScannersUseArchtest: a test reads the repo's own sources only
// through this package. Every filepath.Walk, filepath.WalkDir or fs.WalkDir
// in a _test.go file elsewhere must start at a scratch tree the test made:
// its root expression (through the identifiers assigned to it in the file)
// is a t.TempDir() / os.MkdirTemp result, a "testdata" path, or an
// in-memory fstest.MapFS. Anything else, a "../.." root, a found go.mod, a
// parameter, fails: each hand-rolled walk had its own skip list, and the one
// that missed dot directories scanned the git worktrees under .claude/.
func TestRepoScannersUseArchtest(t *testing.T) {
	var problems []string
	Scan(t, Opts{Lang: Go, Tests: true}, func(rel string, src []byte) {
		if !strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "internal/archtest/") {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range repoWalks(f) {
			problems = append(problems, fset.Position(call.Pos()).String())
		}
	})
	if len(problems) > 0 {
		t.Fatalf("a test walks a tree it did not make; read the repo with archtest.Files / archtest.Scan:\n  %s", strings.Join(problems, "\n  "))
	}
}

// repoWalks returns the walk calls in f whose root is not a scratch tree.
func repoWalks(f *ast.File) []*ast.CallExpr {
	assigned := map[string][]ast.Expr{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						assigned[id.Name] = append(assigned[id.Name], n.Rhs[i])
					}
				}
			} else if len(n.Rhs) == 1 { // root, err := filepath.Abs(…)
				for _, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						assigned[id.Name] = append(assigned[id.Name], n.Rhs[0])
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if i < len(n.Values) {
					assigned[id.Name] = append(assigned[id.Name], n.Values[i])
				}
			}
		}
		return true
	})
	var scratch func(e ast.Expr, seen map[string]bool) bool
	scratch = func(e ast.Expr, seen map[string]bool) bool {
		found, names := false, []string{}
		ast.Inspect(e, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name == "TempDir" || n.Sel.Name == "MkdirTemp" || n.Sel.Name == "MapFS" {
					found = true
				}
			case *ast.BasicLit:
				if v, err := strconv.Unquote(n.Value); err == nil && (v == "testdata" || strings.HasPrefix(v, "testdata/")) {
					found = true
				}
			case *ast.Ident:
				names = append(names, n.Name)
			}
			return true
		})
		if found {
			return true
		}
		for _, name := range names {
			if seen[name] || len(assigned[name]) == 0 {
				continue
			}
			seen[name] = true
			all := true
			for _, rhs := range assigned[name] {
				all = all && scratch(rhs, seen)
			}
			if all {
				return true
			}
		}
		return false
	}
	var out []*ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		walk := ok && ((pkg.Name == "filepath" && (sel.Sel.Name == "Walk" || sel.Sel.Name == "WalkDir")) || (pkg.Name == "fs" && sel.Sel.Name == "WalkDir"))
		if walk && !scratch(call.Args[0], map[string]bool{}) {
			out = append(out, call)
		}
		return true
	})
	return out
}

// The detector itself: scratch roots pass, a repo root in any spelling fails.
func TestRepoWalksDetector(t *testing.T) {
	for body, flagged := range map[string]bool{
		`filepath.WalkDir(t.TempDir(), fn)`:                                          false,
		`dir := t.TempDir(); sub := filepath.Join(dir, "a"); filepath.Walk(sub, fn)`: false,
		`root, _ := os.MkdirTemp("", "x"); filepath.WalkDir(root, fn)`:               false,
		`filepath.WalkDir(filepath.Join("testdata", "golden"), fn)`:                  false,
		`fs.WalkDir(fstest.MapFS{}, ".", fn)`:                                        false,
		`filepath.WalkDir("..", fn)`:                                                 true,
		`root := filepath.Join("..", ".."); filepath.WalkDir(root, fn)`:              true,
		`root, err := filepath.Abs("../.."); _ = err; filepath.Walk(root, fn)`:       true,
		`filepath.WalkDir(".", fn)`:                                                  true,
		`fs.WalkDir(os.DirFS("../.."), ".", fn)`:                                     true,
		`filepath.WalkDir(dir, fn)`:                                                  true, // a parameter: unknown, so not scratch
	} {
		src := "package x\nfunc f(t *testing.T, dir string) {\n" + body + "\n}\n"
		f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got := len(repoWalks(f)) > 0; got != flagged {
			t.Errorf("%s: flagged = %v, want %v", body, got, flagged)
		}
	}
}
