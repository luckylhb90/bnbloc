package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

const corePkgPath = "github.com/luckylhb90/bnbloc/core"

// bareAssertionPending lists files that still assert an optional capability
// directly, each with the change that converts it. The list only shrinks: an
// entry whose file no longer asserts fails the test until it is deleted.
var bareAssertionPending = map[string]string{}

// Optional capabilities are reached only through core.As (or Supports), which
// consults a data-described provider's CapabilityReporter: a bare type
// assertion to a capability interface would see a manifest or protocol core
// "offer" every optional method whether or not it is declared. This scans
// every Go source outside core/provider.go (As's own file) for a type
// assertion or type-switch case naming a core interface other than Provider.
func TestNoBareCapabilityAssertion(t *testing.T) {
	ifaces := coreInterfaces(t)
	found := map[string][]string{}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if rel == "core/provider.go" {
			return
		}
		hits, err := bareCapabilityAssertions(rel, src, ifaces)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) > 0 {
			found[rel] = hits
		}
	})
	files := make([]string, 0, len(found))
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if _, pending := bareAssertionPending[f]; !pending {
			t.Errorf("%s asserts an optional capability directly (%s): use core.As[T](ctx, p, ep, core.Capability…)", f, strings.Join(found[f], ", "))
		}
	}
	for f := range bareAssertionPending {
		if _, still := found[f]; !still {
			t.Errorf("%s no longer asserts a capability directly: delete its bareAssertionPending entry", f)
		}
	}
}

// coreInterfaces names every interface type package core declares except
// Provider (the one a registry instance is legitimately asserted to).
func coreInterfaces(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]bool{}
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if _, ok := ts.Type.(*ast.InterfaceType); ok && ts.Name.Name != "Provider" {
					out[ts.Name.Name] = true
				}
			}
		}
	}
	if !out["Discoverer"] || !out["TargetResolver"] || !out["WorkspaceFileWriter"] {
		t.Fatalf("capability interfaces not found in package core: %v", out)
	}
	return out
}

// bareCapabilityAssertions reports the core interfaces a file asserts to:
// x.(core.T) (under any import name) or a type-switch case core.T, and the
// unqualified forms inside package core itself.
func bareCapabilityAssertions(rel string, src []byte, ifaces map[string]bool) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
	if err != nil {
		return nil, err
	}
	coreNames := map[string]bool{}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == corePkgPath {
			name := "core"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			coreNames[name] = true
		}
	}
	inCore := strings.HasPrefix(rel, "core/") && !strings.Contains(strings.TrimPrefix(rel, "core/"), "/")
	capability := func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && coreNames[id.Name] && ifaces[x.Sel.Name] {
				return id.Name + "." + x.Sel.Name, true
			}
		case *ast.Ident:
			if inCore && ifaces[x.Name] {
				return x.Name, true
			}
		}
		return "", false
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.TypeAssertExpr:
			if name, ok := capability(x.Type); ok {
				hits = append(hits, name)
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range x.Body.List {
				for _, e := range stmt.(*ast.CaseClause).List {
					if name, ok := capability(e); ok {
						hits = append(hits, name)
					}
				}
			}
		}
		return true
	})
	return hits, nil
}
