package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	createTableRe = regexp.MustCompile("(?s)CREATE TABLE IF NOT EXISTS `?(\\w+)`?\\s*\\((.*?)\\n\\)")
	tableRefRe    = regexp.MustCompile("(?i)\\b(?:FROM|JOIN|UPDATE)\\s+`?(\\w+)`?")
	tenantColRe   = regexp.MustCompile(`(?m)^\s*tenant_id\s`)
)

// TestTenantTablesFilterTenant fails when a store function's SQL reads,
// updates or deletes rows of a table that has a tenant_id column without
// naming tenant_id, unless the function says why it is cross-tenant with a
// `// cross-tenant: <reason>` comment (a claim across tenants, a global
// token, retention, …). A forgotten tenant filter is a cross-tenant leak
// that no single-tenant test sees.
func TestTenantTablesFilterTenant(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	tenantTables := map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatch(string(schema), -1) {
		if tenantColRe.MatchString(m[2]) {
			tenantTables[m[1]] = true
		}
	}
	if len(tenantTables) < 10 {
		t.Fatalf("found only %d tenant tables in schema.sql; the parser is broken", len(tenantTables))
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "migrations.go" || f == "schema_ddl.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			var sqlText strings.Builder
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						sqlText.WriteString(v)
						sqlText.WriteByte(' ')
					}
				}
				return true
			})
			text := sqlText.String()
			if strings.Contains(text, "tenant_id") {
				continue
			}
			var tables []string
			for _, m := range tableRefRe.FindAllStringSubmatch(text, -1) {
				if tenantTables[m[1]] {
					tables = append(tables, m[1])
				}
			}
			if len(tables) == 0 || hasCrossTenantNote(file, fd) {
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%s (%s)", f, funcKey(fd), strings.Join(tables, ", ")))
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("SQL on a tenant table without tenant_id; filter by tenant, or say why it is cross-tenant with a `// cross-tenant: <reason>` comment in the function:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// hasCrossTenantNote reports a `cross-tenant:` comment in fd's doc or body.
func hasCrossTenantNote(file *ast.File, fd *ast.FuncDecl) bool {
	if fd.Doc != nil && strings.Contains(fd.Doc.Text(), "cross-tenant:") {
		return true
	}
	for _, cg := range file.Comments {
		if cg.Pos() >= fd.Body.Lbrace && cg.End() <= fd.Body.Rbrace && strings.Contains(cg.Text(), "cross-tenant:") {
			return true
		}
	}
	return false
}

// funcKey is Recv.Name or Name.
func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if s, ok := typ.(*ast.StarExpr); ok {
		typ = s.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
