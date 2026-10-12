package store

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// Subjects (user ids, owners, authors, actors, requesters) are case-sensitive
// and compare exactly, everywhere (code-health plan D2). Before, MySQL's
// case-insensitive collation and scattered EqualFold/ToLower calls made
// "José" and "jose" the same author on one backend and different authors on
// the other. These two tests keep the rule from being re-implemented.

// identityWord matches an identifier that names a subject.
var identityWord = regexp.MustCompile(`(?i)(user|owner|author|subject|actor|requester|borrower|grantee|recipient|caller|creator|member|uploader)`)

// notIdentity strips identifiers that contain an identity word but are not one.
var notIdentity = regexp.MustCompile(`(?i)useragent|uservisible`)

// identityColumn matches a schema.sql column that holds a subject.
var identityColumn = regexp.MustCompile(`^(user_id|subject|author|owner|recipient|uploader|actor|requester|scope_id)$|_(user_id|author|owner_id)$|_by$`)

// TestIdentityColumnsBinary: every identity column in schema.sql compares
// byte-exact (utf8mb4_bin), like the Go code and the memory store. A column
// under the table default collation (utf8mb4_0900_ai_ci) would fold case and
// accents on MySQL only.
func TestIdentityColumnsBinary(t *testing.T) {
	head := regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS (\w+) \(`)
	var bad []string
	n := 0
	for _, m := range head.FindAllStringSubmatchIndex(schemaSQL, -1) {
		table := schemaSQL[m[2]:m[3]]
		body := schemaSQL[m[1]:]
		body = body[:strings.Index(body, ";")]
		for _, line := range strings.Split(body, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || !identityColumn.MatchString(f[0]) || !strings.Contains(f[1], "CHAR(") {
				continue
			}
			n++
			if !strings.Contains(line, "COLLATE utf8mb4_bin") {
				bad = append(bad, table+"."+f[0])
			}
		}
	}
	if n < 40 {
		t.Fatalf("matched only %d identity columns; the column pattern no longer fits schema.sql", n)
	}
	if len(bad) > 0 {
		t.Fatalf("identity columns without COLLATE utf8mb4_bin (subjects are case-sensitive; add the collation and carry it with a new migration): %v", bad)
	}
}

// TestNoIdentityFold forbids strings.EqualFold / ToLower / ToUpper (and the
// bytes twins) on a subject anywhere in the Go code: an argument naming an
// identity, or a call inside a function whose name does (the old bridge
// sameUserID). A fold of something that only looks like one (an email
// address, a header) carries an "identity-fold: <reason>" comment on its line
// or the line above.
func TestNoIdentityFold(t *testing.T) {
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "cmd", "core"}}, func(rel string, src []byte) {
		found, err := identityFolds(rel, src)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			problems = append(problems, rel+":"+f)
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("%d case fold(s) on a subject; subjects are case-sensitive, compare with ==:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

var foldFuncs = map[string]bool{"EqualFold": true, "ToLower": true, "ToUpper": true}

// identityFolds returns "<line>: <call>" for every case fold on a subject.
func identityFolds(rel string, code []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, code, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	allowed := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "identity-fold:") {
				line := fset.Position(c.Pos()).Line
				allowed[line], allowed[line+1] = true, true
			}
		}
	}
	src := func(n ast.Node) string {
		var b strings.Builder
		_ = printer.Fprint(&b, fset, n)
		return notIdentity.ReplaceAllString(b.String(), "")
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		identityFunc := identityWord.MatchString(notIdentity.ReplaceAllString(fn.Name.Name, ""))
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !foldFuncs[sel.Sel.Name] {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || (pkg.Name != "strings" && pkg.Name != "bytes") {
				return true
			}
			line := fset.Position(call.Pos()).Line
			if allowed[line] {
				return true
			}
			hit := identityFunc
			for _, a := range call.Args {
				if identityWord.MatchString(src(a)) {
					hit = true
				}
			}
			if hit {
				out = append(out, strconv.Itoa(line)+": "+src(call))
			}
			return true
		})
	}
	return out, nil
}
