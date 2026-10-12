package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// storeRuleCopies are the re-spellings of a rule owned in rules.go (or, for
// bridge liveness and job statuses, in bridge_jobs.go) that still exist, per
// rule and file. The list only shrinks: a new copy fails the test, and an
// entry whose count dropped must be lowered (or removed) with the change
// that removed the copy. Batch 4.3 of the code-health plan empties it.
var storeRuleCopies = map[string]map[string]int{
	"expiry-inline": {
		"api_tokens.go":              1,
		"content_drafts.go":          1,
		"notification_replies.go":    1,
		"notification_reply_refs.go": 1,
		"refresh_tokens.go":          4,
		"sso_handoffs.go":            2,
	},
	"limit-clamp": {
		"audit_log.go":                2,
		"content_components.go":       1,
		"content_publish_requests.go": 1,
		"grants.go":                   2,
		"mysql_grant.go":              2,
		"mysql_notifications.go":      2,
		"notification_replies.go":     1,
		"notifications.go":            1,
		"quota_requests.go":           1,
		"retention.go":                1,
	},
	"mysql-error": {
		"api_tokens.go":           1,
		"mysql_quota_requests.go": 2,
		"sso_handoffs.go":         1,
	},
	"null-helper": {
		"mysql.go":       1,
		"mysql_grant.go": 4,
	},
}

// storeRuleOwners are the files allowed to spell each rule.
var storeRuleOwners = map[string][]string{
	"limit-clamp":   {"rules.go"},
	"mysql-error":   {"rules.go"},
	"bridge-live":   {"bridge_jobs.go"},
	"job-status":    {"bridge_jobs.go"},
	"not-deleted":   {"rules.go"},
	"human-member":  {"rules.go"},
	"null-helper":   {"rules.go"},
	"expiry-inline": {"rules.go"},
}

var (
	jobStatusSQL   = regexp.MustCompile(`IN\s*\(\s*'(queued|claimed|running|reflowing|replied|failed|timed_out|cancelled)'`)
	humanMemberSQL = regexp.MustCompile(`agent_host\s*=\s*0`)
)

// TestStoreRulesSingleSource fails when a store rule is spelled outside its
// owner: a page-size clamp (clampLimit), a driver error number (isDupEntry /
// dupAs), bridge liveness (BridgeLive / liveBridgePred), a job-status SQL
// IN-list literal (the lists generated from the Go status sets), the soft-delete and human-member
// predicates (sqlNotDeleted / sqlHumanMember), a hand-rolled NULL helper
// (nullIfZero / nullPtr) or an inline expiry comparison on an ExpiresAtMs
// field (liveAt).
func TestStoreRulesSingleSource(t *testing.T) {
	got := map[string]map[string]int{}
	add := func(rule, file string) {
		for _, owner := range storeRuleOwners[rule] {
			if file == owner {
				return
			}
		}
		if got[rule] == nil {
			got[rule] = map[string]int{}
		}
		got[rule][file]++
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "migrations.go" || f == "schema_ddl.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !strings.Contains(strings.ToLower(fd.Name.Name), "limit") {
				continue
			}
			// A named page-size helper (contentLimit, …) clamps with
			// clampLimit, never its own comparison.
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if b, ok := n.(*ast.BinaryExpr); ok && (b.Op == token.LEQ || b.Op == token.LSS) {
					if lit, ok := b.Y.(*ast.BasicLit); ok && lit.Kind == token.INT && !strings.Contains(strings.ToLower(exprName(b.X)), "limit") {
						add("limit-clamp", f)
					}
				}
				return true
			})
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if name := n.Name.Name; n.Recv == nil && strings.HasPrefix(name, "null") {
					add("null-helper", f)
				}
			case *ast.BinaryExpr:
				if lit, ok := n.Y.(*ast.BasicLit); ok && lit.Kind == token.INT && (n.Op == token.LEQ || n.Op == token.LSS) {
					if name := exprName(n.X); strings.Contains(strings.ToLower(name), "limit") || strings.Contains(strings.ToLower(name), "batch") {
						add("limit-clamp", f)
					}
				}
				if isComparison(n.Op) && (strings.HasSuffix(exprName(n.X), "ExpiresAtMs") || strings.HasSuffix(exprName(n.Y), "ExpiresAtMs")) {
					add("expiry-inline", f)
				}
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "mysql" && n.Sel.Name == "MySQLError" {
					add("mysql-error", f)
				}
			case *ast.Ident:
				if n.Name == "BridgeOfflineAfterMs" {
					add("bridge-live", f)
				}
			case *ast.BasicLit:
				if n.Kind == token.INT && n.Value == "1062" {
					add("mysql-error", f)
				}
				if n.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				for range jobStatusSQL.FindAllString(v, -1) {
					add("job-status", f)
				}
				for range strings.Count(v, "deleted_at_ms IS NULL") {
					add("not-deleted", f)
				}
				for range humanMemberSQL.FindAllString(v, -1) {
					add("human-member", f)
				}
			}
			return true
		})
	}
	var problems []string
	for rule, byFile := range got {
		for file, n := range byFile {
			if allowed := storeRuleCopies[rule][file]; n > allowed {
				problems = append(problems, fmt.Sprintf("%s: %d new copies of the %q rule (allowed %d); use its owner (%s)", file, n-allowed, rule, allowed, strings.Join(storeRuleOwners[rule], ", ")))
			}
		}
	}
	for rule, byFile := range storeRuleCopies {
		for file, allowed := range byFile {
			if n := got[rule][file]; n < allowed {
				problems = append(problems, fmt.Sprintf("storeRuleCopies[%q][%q] = %d but only %d remain: lower it (the list only shrinks)", rule, file, allowed, n))
			}
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("store rules spelled outside their owner:\n  %s", strings.Join(problems, "\n  "))
	}
}

func isComparison(op token.Token) bool {
	switch op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

// exprName is the trailing name of an identifier or selector (q.Limit →
// "Limit"), or "".
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}
