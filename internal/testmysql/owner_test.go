package testmysql

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestMySQLTestsUseTestmysql fails when Go code outside this package reads
// BNBLOC_TEST_MYSQL_DSN: such a test would open the DSN's own database,
// which every package running in parallel would share again.
func TestMySQLTestsUseTestmysql(t *testing.T) {
	var offenders []string
	fset := token.NewFileSet()
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Tests: true}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/testmysql/") {
			return
		}
		file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && v == EnvDSN {
				offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line))
			}
			return true
		})
	})
	if len(offenders) > 0 {
		t.Fatalf("%s is read outside internal/testmysql; use testmysql.Fresh / testmysql.Shared (a database of the test's own):\n  %s",
			EnvDSN, strings.Join(offenders, "\n  "))
	}
}
