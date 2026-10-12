package realtimehttp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestOnePublishPath: realtime publishes a room event it accepted only
// through s.publish, which runs the post-publish taps (plugin hooks, the
// notification observer, the member-join share step) on what was delivered.
// The taps used to be wired per call site (PostChat, the internal ingest),
// so a new publish path had to remember them.
func TestOnePublishPath(t *testing.T) {
	allowed := map[string]string{
		"service.go:publish":       "the owner",
		"scaleout.go:publishOwned": "presence and the roster: no request, announced by the room's owner",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := f + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Publish" && sel.Sel.Name != "PublishCommit") {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Hub" && allowed[key] == "" {
					problems = append(problems, key+": Hub."+sel.Sel.Name)
				}
				return true
			})
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("publish through s.publish:\n  %s", strings.Join(problems, "\n  "))
	}
}
