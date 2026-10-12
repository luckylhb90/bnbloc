package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// contractPending are store interfaces implemented by both a memory and a
// MySQL store that have no contract test yet. The list only shrinks; Batch
// 4.1 of the code-health plan empties it.
var contractPending = map[string]bool{
	"APITokenStore":               true,
	"AuditLogReader":              true,
	"ComponentInstallStore":       true,
	"ContentComponentStore":       true,
	"ExtensionAuditStore":         true,
	"GrantStore":                  true,
	"NotificationOutboxStore":     true,
	"NotificationPrefsStore":      true,
	"NotificationReplyTokenStore": true,
	"PlatformSettingsStore":       true,
	"QuotaRequestStore":           true,
	"RefreshTokenStore":           true,
	"RetentionStore":              true,
	"SSOHandoffStore":             true,
	"UsageBudgetStore":            true,
	"WebPushSubscriptionStore":    true,
}

// rigPending are store test files that still open MySQL outside the one rig.
// The list only shrinks; Batch 4.1 empties it.
var rigPending = map[string]bool{
	"api_tokens_test.go":                    true,
	"audit_log_test.go":                     true,
	"content_components_test.go":            true,
	"content_drafts_test.go":                true,
	"content_gap_test.go":                   true,
	"content_prompt_uses_test.go":           true,
	"content_sharing_migration_test.go":     true,
	"content_sharing_test.go":               true,
	"content_usage_test.go":                 true,
	"grant_usage_test.go":                   true,
	"mysql_auth_state_test.go":              true,
	"notification_dispatch_test.go":         true,
	"notification_outbox_migration_test.go": true,
	"notification_replies_test.go":          true,
	"notification_reply_refs_test.go":       true,
	"notifications_test.go":                 true,
	"retention_test.go":                     true,
	"run_reservations_test.go":              true,
	"team_billing_test.go":                  true,
	"usage_budgets_test.go":                 true,
	"web_push_test.go":                      true,
}

// TestEveryStoreInterfaceHasContract fails when a store interface with a
// memory and a MySQL implementation (a Backends field, or
// `var _ X = (*Memory…)(nil)` and `var _ X = (*MySQL…)(nil)`) has no
// contract test: a test function named
// TestContract<X>… that runs through forEachBackend. A behaviour only one
// backend has is a bug in the other, and only a shared case finds it.
func TestEveryStoreInterfaceHasContract(t *testing.T) {
	fset := token.NewFileSet()
	impls := map[string]map[string]bool{} // interface -> {"Memory": true, "MySQL": true}
	contracts := map[string]bool{}        // test function name -> runs forEachBackend
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(f, "_test.go") {
			for _, d := range file.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "TestContract") {
					continue
				}
				contracts[fd.Name.Name] = callsFunc(fd.Body, "forEachBackend")
			}
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			// Every Backends field is built by NewBackends from a memory
			// and a MySQL store.
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "Backends" {
				if st, ok := ts.Type.(*ast.StructType); ok {
					for _, field := range st.Fields.List {
						if id, ok := field.Type.(*ast.Ident); ok && id.IsExported() {
							impls[id.Name] = map[string]bool{"Memory": true, "MySQL": true}
						}
					}
				}
				return true
			}
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "_" || vs.Type == nil || len(vs.Values) != 1 {
				return true
			}
			iface, ok := vs.Type.(*ast.Ident)
			if !ok {
				return true
			}
			impl := implName(vs.Values[0])
			for _, kind := range []string{"Memory", "MySQL"} {
				if strings.HasPrefix(impl, kind) {
					if impls[iface.Name] == nil {
						impls[iface.Name] = map[string]bool{}
					}
					impls[iface.Name][kind] = true
				}
			}
			return true
		})
	}
	var missing, stale []string
	seen := map[string]bool{}
	for iface, kinds := range impls {
		if !kinds["Memory"] || !kinds["MySQL"] {
			continue
		}
		seen[iface] = true
		covered := false
		for name, rig := range contracts {
			if rig && strings.HasPrefix(name, "TestContract"+iface) {
				covered = true
			}
		}
		switch {
		case !covered && !contractPending[iface]:
			missing = append(missing, iface)
		case covered && contractPending[iface]:
			stale = append(stale, iface)
		}
	}
	for iface := range contractPending {
		if !seen[iface] {
			stale = append(stale, iface)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("store interfaces without a contract test (add TestContract<Interface>… running forEachBackend):\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("contractPending entries that now have a contract (or are gone); remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

// TestStoreTestsUseOneRig fails when a store test opens MySQL itself instead
// of through forEachBackend (the binary's own database) or testmysql.Fresh
// (a database of the test's own, for a test that alters the schema): a
// MySQL-only test of shared behaviour hides the memory store's drift, and a
// hand-opened pool is one more copy of the rig.
func TestStoreTestsUseOneRig(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	var offenders []string
	for _, f := range files {
		if f == "store_contract_test.go" || f == "main_test.go" {
			continue // the rig
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var why []string
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// Variables holding a testmysql.Fresh DSN.
			fresh := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
					for i, rhs := range as.Rhs {
						if id, ok := as.Lhs[i].(*ast.Ident); ok && isFreshDB(rhs) {
							fresh[id.Name] = true
						}
					}
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch name := calleeName(call.Fun); name {
				case "sharedMySQL", "sharedMySQLDSN", "mysqlTestDSN", "Shared":
					why = append(why, fmt.Sprintf("%s:%d %s", f, fset.Position(call.Pos()).Line, name))
				case "NewBackends", "OpenMySQL":
					if len(call.Args) != 1 || isEmptyString(call.Args[0]) || isFreshDB(call.Args[0]) {
						return true
					}
					if id, ok := call.Args[0].(*ast.Ident); ok && fresh[id.Name] {
						return true
					}
					why = append(why, fmt.Sprintf("%s:%d %s", f, fset.Position(call.Pos()).Line, name))
				}
				return true
			})
		}
		if len(why) == 0 {
			continue
		}
		found[f] = true
		if !rigPending[f] {
			offenders = append(offenders, strings.Join(why, ", "))
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("store tests open MySQL outside the rig; use forEachBackend (or testmysql.Fresh for a schema test):\n  %s", strings.Join(offenders, "\n  "))
	}
	var stale []string
	for f := range rigPending {
		if !found[f] {
			stale = append(stale, f)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("rigPending entries that now use the rig; remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

func callsFunc(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call.Fun) == name {
			found = true
		}
		return !found
	})
	return found
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// implName is Y in (*Y)(nil).
func implName(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	p, ok := call.Fun.(*ast.ParenExpr)
	if !ok {
		return ""
	}
	star, ok := p.X.(*ast.StarExpr)
	if !ok {
		return ""
	}
	if id, ok := star.X.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func isEmptyString(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == ""
}

// isFreshDB reports testmysql.Fresh(...).
func isFreshDB(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Fresh"
}
