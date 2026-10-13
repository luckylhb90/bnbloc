package store

import (
	"flag"
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

// Expand/contract (AGENTS.md, "A migration only adds";
// docs/bugs/2026-10-13-destructive-migration-broke-mixed-deploy.md). Services
// start, and migrate, at different times, so for a while the previous
// release's code runs against the new schema. A migration may therefore only
// add: tables, columns (nullable, or NOT NULL with a DEFAULT), indexes, and
// widenings. Whatever the previous release still uses is dropped, renamed or
// narrowed only in a LATER release than the code that stopped using it.
//
// TestMigrationsAreDeploySafe checks every migration newer than the lock's
// `released` marker (the newest migration id that shipped):
//
//   - a drop of a table or column (DROP TABLE, ALTER TABLE … DROP [COLUMN],
//     or a helper whose SQL does that, such as dropColumnIfExists) passes only
//     when the lock recorded the name as `unreferenced` when the marker was
//     set: no store code of that release read or wrote it. The name must be
//     unreferenced now too, and recorded in retiredTables/retiredColumns with
//     the dropping migration's id. A drop names its table and column as
//     string literals, so the check can see them.
//   - a rename (RENAME TABLE/COLUMN/TO, CHANGE COLUMN) never passes: add the
//     new name, ship, then drop the old one.
//   - a rewrite of an existing column (MODIFY, ensureColumnType, a dropped
//     DEFAULT) or a column that becomes NOT NULL without a DEFAULT passes
//     only with a `// deploy-safe: <why the previous release's code works
//     against it>` comment in the migration entry (a widening, a type the
//     old writers already satisfy). Comments are outside the lock's
//     fingerprint, so a reviewer can read the claim beside the change.
//
// Index changes, new tables and data rewrites are not refused. Every SQL the
// store runs lives in this package, so "referenced" is a whole-word match
// in the string literals of its non-test sources (migrations excluded): a
// table by its name, a column by its name in a file that also names its
// table. False positives only make a drop wait longer.
//
// Cutting a release advances the marker and records the unreferenced names
// of that code:
//
//	go test ./internal/store -run TestMigrationsAreDeploySafe -release
var releaseMigrations = flag.Bool("release", false, "set migrations.lock's released marker to the newest migration and record the schema names no store code references (run on the commit being released)")

// deploySafeHelpers are migration helpers whose SQL rewrites a column but
// keeps what the previous release relies on, with the reason.
var deploySafeHelpers = map[string]string{
	"ensureColumnsCollation": "keeps each column's type, nullability and default; only comparisons change",
}

func TestMigrationsAreDeploySafe(t *testing.T) {
	src, err := os.ReadFile("migrations.go")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseMigrationEntries("migrations.go", src)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readMigrationLock(migrationLockFile)
	if err != nil {
		t.Fatal(err)
	}
	refs := storeReferences(t)
	if *releaseMigrations {
		locked := map[string]bool{}
		for _, m := range lock.migs {
			locked[m.id] = true
		}
		for _, e := range entries {
			if !locked[e.id] {
				t.Fatalf("migration %s is not locked; run go test ./internal/store -run TestMigrationLock -update first", e.id)
			}
		}
		lock.released = entries[len(entries)-1].id
		lock.unreferenced = refs.unreferencedSchemaNames(schemaTableColumns(t))
		if err := writeMigrationLock(migrationLockFile, lock); err != nil {
			t.Fatal(err)
		}
		t.Logf("migrations.lock: released %s, %d unreferenced name(s)", lock.released, len(lock.unreferenced))
		return
	}
	if lock.released == "" {
		t.Fatal("migrations.lock has no released marker; cut one with go test ./internal/store -run TestMigrationsAreDeploySafe -release")
	}
	at := -1
	for i, e := range entries {
		if e.id == lock.released {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("migrations.lock's released marker %s is not a migration in migrations.go", lock.released)
	}
	judge := deployJudge{
		helpers:      migrationHelperHazards(t),
		unreferenced: map[string]bool{},
		referenced:   refs.referenced,
		retiredBy:    retiredBy,
	}
	for _, name := range lock.unreferenced {
		judge.unreferenced[name] = true
	}
	if problems := judge.problems(entries[at+1:]); len(problems) > 0 {
		t.Fatalf("migrations newer than the released %s are not deploy-safe (AGENTS.md, \"A migration only adds\"):\n  %s",
			lock.released, strings.Join(problems, "\n  "))
	}
}

// retiredBy is the migration id retiredTables/retiredColumns record for a
// dropped name ("table" or "table.column").
func retiredBy(name string) string {
	if strings.Contains(name, ".") {
		return retiredColumns[name]
	}
	return retiredTables[name]
}

type hazardKind int

const (
	hazardDrop         hazardKind = iota + 1 // a table or column goes away
	hazardRename                             // a table or column changes name
	hazardRewrite                            // an existing column changes type, nullability or default
	hazardUnverifiable                       // a drop whose names are not literals
)

// hazard is one schema step of a migration that the previous release's code
// may not survive.
type hazard struct {
	kind hazardKind
	name string // the dropped "table" or "table.column" (hazardDrop)
	what string // the statement or call, for the message
}

// deployJudge applies the expand/contract rule to migration entries.
type deployJudge struct {
	helpers      map[string]hazardKind  // migration helpers that drop, rename or rewrite
	unreferenced map[string]bool        // names the released code never used (migrations.lock)
	referenced   func(name string) bool // whether current store code uses a name
	retiredBy    func(name string) string
}

func (j deployJudge) problems(entries []migrationEntry) []string {
	var out []string
	for _, e := range entries {
		var rewrites []string
		seen := map[string]bool{}
		for _, h := range migrationHazards(e, j.helpers) {
			switch h.kind {
			case hazardDrop:
				switch {
				case !j.unreferenced[h.name]:
					out = append(out, fmt.Sprintf("%s drops %s (%s), which the released code may still use: stop using it in one release, cut the release (-release records it unreferenced), and drop it in a later one", e.id, h.name, h.what))
				case j.referenced(h.name):
					out = append(out, fmt.Sprintf("%s drops %s, which store code still uses", e.id, h.name))
				case j.retiredBy(h.name) != e.id:
					out = append(out, fmt.Sprintf("%s drops %s: record it in retiredTables/retiredColumns with %q", e.id, h.name, e.id))
				}
			case hazardRename:
				out = append(out, fmt.Sprintf("%s renames (%s): the released code still uses the old name; add the new one, ship, and drop the old one in a later release", e.id, h.what))
			case hazardRewrite:
				if !seen[h.what] {
					seen[h.what] = true
					rewrites = append(rewrites, h.what)
				}
			case hazardUnverifiable:
				out = append(out, fmt.Sprintf("%s drops through %s: name the table and column as string literals so the drop can be checked", e.id, h.what))
			}
		}
		if len(rewrites) > 0 && !deploySafeAnnotation(e) {
			out = append(out, fmt.Sprintf("%s rewrites existing columns (%s): the released code must work against the new shape; say why in a `// deploy-safe: <reason>` comment in the entry, or add a new column instead", e.id, strings.Join(rewrites, "; ")))
		}
	}
	return out
}

// deploySafeAnnotation reports whether entry carries a `deploy-safe:` comment
// with a reason.
func deploySafeAnnotation(e migrationEntry) bool {
	for _, g := range e.file.Comments {
		if g.Pos() < e.lit.Pos() || g.End() > e.lit.End() {
			continue
		}
		for _, c := range g.List {
			_, reason, ok := strings.Cut(c.Text, "deploy-safe:")
			if ok && strings.TrimSpace(reason) != "" {
				return true
			}
		}
	}
	return false
}

var (
	sqlDropTable  = regexp.MustCompile(`(?is)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(.+)$`)
	sqlRenameStmt = regexp.MustCompile(`(?is)^\s*RENAME\s+TABLE\b`)
	sqlAlterTable = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(\S+)\s+(.*)$`)
	sqlSpecDrop   = regexp.MustCompile(`(?is)^DROP\s+(?:COLUMN\s+)?(\S+)`)
	sqlSpecRename = regexp.MustCompile(`(?is)^(?:RENAME\b|CHANGE\b)`)
	sqlSpecRenIdx = regexp.MustCompile(`(?is)^RENAME\s+(?:INDEX|KEY)\b`)
	sqlSpecModify = regexp.MustCompile(`(?is)^MODIFY\b|^ALTER\s+(?:COLUMN\s+)?\S+\s+DROP\s+DEFAULT\b`)
	sqlIsNotNull  = regexp.MustCompile(`(?is)\bIS\s+NOT\s+NULL\b`)
	sqlNotNull    = regexp.MustCompile(`(?is)\bNOT\s+NULL\b`)
	sqlDefault    = regexp.MustCompile(`(?is)\bDEFAULT\b`)
	sqlCreate     = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\b`)
	sqlIdent      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// sqlDropKeywords follow DROP in an ALTER TABLE spec without naming a column.
var sqlDropKeywords = map[string]bool{"INDEX": true, "KEY": true, "PRIMARY": true, "FOREIGN": true, "CHECK": true, "CONSTRAINT": true}

// migrationHazards lists the schema steps of entry that the previous
// release's code may not survive: its SQL literals and its calls to the
// helpers that drop, rename or rewrite.
func migrationHazards(e migrationEntry, helpers map[string]hazardKind) []hazard {
	var out []hazard
	ast.Inspect(e.lit, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			fn, ok := n.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			kind, ok := helpers[fn.Name]
			if !ok {
				return true
			}
			if kind != hazardDrop {
				out = append(out, hazard{kind: kind, what: callText(fn.Name, n.Args)})
				return true
			}
			name, ok := helperDropName(n.Args)
			if !ok {
				out = append(out, hazard{kind: hazardUnverifiable, what: callText(fn.Name, n.Args)})
				return true
			}
			out = append(out, hazard{kind: hazardDrop, name: name, what: callText(fn.Name, n.Args)})
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(n.Value); err == nil {
				out = append(out, sqlHazards(s)...)
			}
		}
		return true
	})
	return out
}

// helperDropName is the name a drop helper removes: its table (third
// argument, after ctx and db) and, for a column helper, its column (fourth).
func helperDropName(args []ast.Expr) (string, bool) {
	if len(args) < 3 {
		return "", false
	}
	var parts []string
	for _, a := range args[2:] {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !sqlIdent.MatchString(s) {
			return "", false
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "."), true
}

func callText(fn string, args []ast.Expr) string {
	var parts []string
	for _, a := range args[min(2, len(args)):] {
		if lit, ok := a.(*ast.BasicLit); ok {
			parts = append(parts, lit.Value)
		} else {
			parts = append(parts, "…")
		}
	}
	return fn + "(" + strings.Join(parts, ", ") + ")"
}

// sqlHazards classifies one SQL literal.
func sqlHazards(sql string) []hazard {
	what := strings.Join(strings.Fields(sql), " ")
	var out []hazard
	if m := sqlDropTable.FindStringSubmatch(sql); m != nil {
		for _, name := range strings.Split(m[1], ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if !sqlIdent.MatchString(name) {
				return append(out, hazard{kind: hazardUnverifiable, what: what})
			}
			out = append(out, hazard{kind: hazardDrop, name: name, what: what})
		}
		return out
	}
	if sqlRenameStmt.MatchString(sql) {
		return []hazard{{kind: hazardRename, what: what}}
	}
	if m := sqlAlterTable.FindStringSubmatch(sql); m != nil {
		table := strings.ToLower(m[1])
		for _, spec := range splitSpecs(m[2]) {
			switch {
			case sqlSpecRenIdx.MatchString(spec):
			case sqlSpecRename.MatchString(spec):
				out = append(out, hazard{kind: hazardRename, what: what})
			case sqlSpecModify.MatchString(spec):
				out = append(out, hazard{kind: hazardRewrite, what: what})
			default:
				d := sqlSpecDrop.FindStringSubmatch(spec)
				if d == nil || sqlDropKeywords[strings.ToUpper(d[1])] || strings.EqualFold(d[1], "DEFAULT") {
					continue
				}
				col := strings.ToLower(d[1])
				if !sqlIdent.MatchString(table) || !sqlIdent.MatchString(col) {
					out = append(out, hazard{kind: hazardUnverifiable, what: what})
					continue
				}
				out = append(out, hazard{kind: hazardDrop, name: table + "." + col, what: what})
			}
		}
	}
	// A column added or changed to NOT NULL without a DEFAULT: the
	// previous release's inserts do not name it.
	if !sqlCreate.MatchString(sql) {
		rest := sqlIsNotNull.ReplaceAllString(sql, "")
		if sqlNotNull.MatchString(rest) && !sqlDefault.MatchString(rest) {
			out = append(out, hazard{kind: hazardRewrite, what: what + " (NOT NULL without a DEFAULT)"})
		}
	}
	return out
}

// splitSpecs splits an ALTER TABLE's specifications on the commas outside
// parentheses.
func splitSpecs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// migrationHelperHazards classifies the package's functions by the SQL they
// run, directly or through another classified function: dropping a table or
// column, renaming, or rewriting a column (deploySafeHelpers excepted).
func migrationHelperHazards(t *testing.T) map[string]hazardKind {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]hazardKind{}
	calls := map[string][]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok {
						calls[name] = append(calls[name], id.Name)
					}
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					if k := helperSQLKind(s); k != 0 && (out[name] == 0 || k < out[name]) {
						out[name] = k
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			for _, c := range callees {
				if k := out[c]; k != 0 && out[fn] == 0 {
					out[fn], changed = k, true
				}
			}
		}
	}
	for name := range deploySafeHelpers {
		if _, ok := out[name]; !ok {
			t.Errorf("deploySafeHelpers names %s, which runs no rewriting SQL", name)
		}
		delete(out, name)
	}
	return out
}

var (
	helperDropSQL    = regexp.MustCompile(`(?i)\bDROP\s+(?:TABLE|COLUMN)\b`)
	helperRenameSQL  = regexp.MustCompile(`(?i)\bRENAME\s+(?:TABLE|COLUMN|TO|AS)\b|\bCHANGE\s+COLUMN\b`)
	helperRewriteSQL = regexp.MustCompile(`(?i)\bMODIFY\s+COLUMN\b|\bDROP\s+DEFAULT\b`)
)

// helperSQLKind is what a helper's SQL template does, by keyword (its names
// are parameters).
func helperSQLKind(sql string) hazardKind {
	switch {
	case helperDropSQL.MatchString(sql):
		return hazardDrop
	case helperRenameSQL.MatchString(sql):
		return hazardRename
	case helperRewriteSQL.MatchString(sql):
		return hazardRewrite
	}
	return 0
}

// schemaRefs is the store's SQL text, per source file.
type schemaRefs map[string]string

func storeReferences(t *testing.T) schemaRefs { return schemaRefs(storeLiterals(t)) }

// referenced reports whether store code uses name: a table by its name, a
// column ("table.column") by its name in a file that also names its table.
func (r schemaRefs) referenced(name string) bool {
	table, col, isCol := strings.Cut(name, ".")
	tableRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(table) + `\b`)
	colRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(col) + `\b`)
	for _, text := range r {
		if tableRE.MatchString(text) && (!isCol || colRE.MatchString(text)) {
			return true
		}
	}
	return false
}

// unreferencedSchemaNames lists the schema.sql tables, and the columns of the
// referenced ones, that no store code uses.
func (r schemaRefs) unreferencedSchemaNames(tables map[string][]string) []string {
	var out []string
	for table, cols := range tables {
		if !r.referenced(table) {
			out = append(out, table)
			continue
		}
		for _, c := range cols {
			if !r.referenced(table + "." + c) {
				out = append(out, table+"."+c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestDeploySafetyRule pins the judge on synthetic migrations: everything
// additive passes, every drop, rename and unexplained rewrite is refused,
// and a drop passes only after a release recorded its name unreferenced.
func TestDeploySafetyRule(t *testing.T) {
	judge := deployJudge{
		helpers:      migrationHelperHazards(t),
		unreferenced: map[string]bool{"old_table": true, "t.old_col": true},
		referenced:   func(name string) bool { return name == "t.live_col" || name == "t.live_col2" },
		retiredBy: func(name string) string {
			return map[string]string{"old_table": "m", "t.old_col": "m", "t.live_col": "m"}[name]
		},
	}
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"add nullable column", `return addColumnIfMissing(ctx, db, "t", "c", "BIGINT NULL")`, false},
		{"add NOT NULL with a default", `return addColumnIfMissing(ctx, db, "t", "c", "VARCHAR(64) NOT NULL DEFAULT '' AFTER b")`, false},
		{"add columns in a loop", `for _, c := range [][2]string{{"a", "INT NOT NULL DEFAULT 0"}, {"b", "JSON NULL"}} {
				if err := addColumnIfMissing(ctx, db, "t", c[0], c[1]); err != nil { return err }
			}
			return nil`, false},
		{"new table", `return createSchemaTable(ctx, db, "t")`, false},
		{"raw new table", `_, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS t (a INT NOT NULL)"); return err`, false},
		{"indexes", `if err := addIndexIfMissing(ctx, db, "t", "ix", "ADD UNIQUE KEY ix (a, b)"); err != nil { return err }
			if err := dropIndexIfExists(ctx, db, "t", "old_ix"); err != nil { return err }
			return ensureIndexColumns(ctx, db, "t", "ix2", []string{"a", "b"})`, false},
		{"rename an index", `_, err := db.ExecContext(ctx, "ALTER TABLE t RENAME INDEX a TO b"); return err`, false},
		{"collation", `return ensureColumnsCollation(ctx, db, "t", "utf8mb4", "utf8mb4_bin", "a")`, false},
		{"backfill", `_, err := db.ExecContext(ctx, "UPDATE t SET c = b WHERE b IS NOT NULL"); return err`, false},

		{"drop a released-unreferenced column", `return dropColumnIfExists(ctx, db, "t", "old_col")`, false},
		{"drop a released-unreferenced table", `_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS old_table"); return err`, false},
		{"drop a column the released code used", `return dropColumnIfExists(ctx, db, "t", "c")`, true},
		{"raw drop column", `_, err := db.ExecContext(ctx, "ALTER TABLE t DROP COLUMN c"); return err`, true},
		{"raw drop without COLUMN", `_, err := db.ExecContext(ctx, "ALTER TABLE t ADD COLUMN d INT NULL, DROP c"); return err`, true},
		{"drop a table the released code used", `_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS t"); return err`, true},
		{"drop a column current code uses", `return dropColumnIfExists(ctx, db, "t", "live_col")`, true},
		{"drop without a retired record", `return dropColumnIfExists(ctx, db, "t", "live_col2")`, true},
		{"drop names hidden in a loop", `for _, c := range []string{"old_col"} { if err := dropColumnIfExists(ctx, db, "t", c); err != nil { return err } }
			return nil`, true},
		{"rename a column", `_, err := db.ExecContext(ctx, "ALTER TABLE t RENAME COLUMN a TO b"); return err`, true},
		{"rename a table", `_, err := db.ExecContext(ctx, "RENAME TABLE t TO u"); return err`, true},
		{"change a column", `_, err := db.ExecContext(ctx, "ALTER TABLE t CHANGE COLUMN a b INT NULL"); return err`, true},
		{"modify a column", `_, err := db.ExecContext(ctx, "ALTER TABLE t MODIFY COLUMN a BIGINT NULL"); return err`, true},
		{"retype a column", `return ensureColumnType(ctx, db, "t", "a", "varchar(64)", "VARCHAR(64) NULL")`, true},
		{"drop a default", `return dropColumnDefault(ctx, db, "t", "a")`, true},
		{"add NOT NULL without a default", `return addColumnIfMissing(ctx, db, "t", "c", "JSON NOT NULL")`, true},
		{"explained retype", `// deploy-safe: widening VARCHAR(48) to VARCHAR(64); the old writers fit
			return ensureColumnType(ctx, db, "t", "a", "varchar(64)", "VARCHAR(64) NOT NULL")`, false},
		{"explained drop is still a drop", `// deploy-safe: trust me
			return dropColumnIfExists(ctx, db, "t", "c")`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package store\n\nvar migrations = []migration{\n\t{\n\t\tid: \"m\",\n\t\trun: func(ctx context.Context, db sqlConn) error {\n\t\t\t" + tc.body + "\n\t\t},\n\t},\n}\n"
			entries, err := parseMigrationEntries("synthetic.go", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			problems := judge.problems(entries)
			if refused := len(problems) > 0; refused != tc.refused {
				t.Fatalf("refused = %v, want %v: %v", refused, tc.refused, problems)
			}
		})
	}
}
