package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The migration lock (migrations.lock) pins what deployed databases already
// ran. A migration id recorded in any deployed schema_migrations ledger never
// runs again, so editing its body (or the schema.sql table a
// createSchemaTable body reads) silently forks deployed databases from fresh
// ones — the 2026-10-08 notification-outbox incident. The lock records:
//
//	migration <id> <sha256 of the entry's Go token stream>
//	table <name> <sha256 of its normalized schema.sql CREATE TABLE>
//
// The migration fingerprint hashes tokens, not text: comments, blank lines
// and gofmt layout may change freely; any code change (a literal, a call, an
// argument) changes the hash. The table fingerprint closes the gap the token
// hash cannot see: a schema.sql table that changed since the lock must be
// named by a migration that is not locked yet (the new migration that carries
// the change to deployed databases).
//
// New ids are appended with:
//
//	go test ./internal/store -run TestMigrationLock -update
var updateMigrationLock = flag.Bool("update", false, "append new migration ids to migrations.lock and refresh its table fingerprints")

const migrationLockFile = "migrations.lock"

const migrationLockHeader = `# Migration lock: deployed migration ids are immutable (see AGENTS.md).
# migration <id> <sha256 of the entry's Go tokens, comments/layout excluded>
# table <name> <sha256 of its normalized schema.sql CREATE TABLE>
# Never edit by hand. Append new ids / refresh tables with:
#   go test ./internal/store -run TestMigrationLock -update
`

type lockedMigration struct {
	id, sum string
	strs    []string // string literals in the entry body (table references)
}

func TestMigrationLock(t *testing.T) {
	current, err := migrationFingerprints("migrations.go")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := schemaTableFingerprints(schemaSQL)
	if err != nil {
		t.Fatal(err)
	}
	lockMigs, lockTables, err := readMigrationLock(migrationLockFile)
	bootstrap := os.IsNotExist(err) && *updateMigrationLock
	if err != nil && !bootstrap {
		t.Fatalf("%v (create it with: go test ./internal/store -run TestMigrationLock -update)", err)
	}

	var problems []string
	byID := map[string]lockedMigration{}
	pos := map[string]int{}
	for i, m := range current {
		if _, dup := byID[m.id]; dup {
			problems = append(problems, fmt.Sprintf("migration id %s appears twice in migrations.go", m.id))
		}
		byID[m.id] = m
		pos[m.id] = i
	}
	locked := map[string]bool{}
	last := -1
	for _, l := range lockMigs {
		locked[l.id] = true
		m, ok := byID[l.id]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("migration %s is locked but no longer in migrations.go; deployed ids are permanent — restore it", l.id))
			continue
		case m.sum != l.sum:
			problems = append(problems, fmt.Sprintf("migration %s is deployed-immutable; add a new migration instead (revert the edit to its body)", l.id))
		}
		if pos[l.id] < last {
			problems = append(problems, fmt.Sprintf("migration %s moved before a migration locked after it; never reorder shipped migrations", l.id))
		}
		last = pos[l.id]
	}
	var fresh []lockedMigration
	for _, m := range current {
		if !locked[m.id] {
			fresh = append(fresh, m)
		}
	}

	// A changed, added or removed schema.sql table must be carried to deployed
	// databases by a migration that is not locked yet.
	touched := func(table string) bool {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(table) + `([^A-Za-z0-9_]|$)`)
		for _, m := range fresh {
			for _, s := range m.strs {
				if re.MatchString(s) {
					return true
				}
			}
		}
		return false
	}
	names := map[string]bool{}
	for name := range tables {
		names[name] = true
	}
	for name := range lockTables {
		names[name] = true
	}
	for _, name := range sortedKeys(names) {
		if bootstrap || tables[name] == lockTables[name] || touched(name) {
			continue
		}
		switch {
		case lockTables[name] == "":
			problems = append(problems, fmt.Sprintf("schema.sql table %s is new but no new migration creates it; add one (createSchemaTable)", name))
		case tables[name] == "":
			problems = append(problems, fmt.Sprintf("schema.sql table %s was removed but no new migration drops it; add one", name))
		default:
			problems = append(problems, fmt.Sprintf("schema.sql table %s changed since the lock but no new migration touches it; deployed databases keep the old shape — add a new migration (never edit a locked one)", name))
		}
	}

	if len(problems) > 0 {
		t.Fatalf("migration lock violations:\n  %s", strings.Join(problems, "\n  "))
	}
	tablesChanged := len(tables) != len(lockTables)
	for name, sum := range tables {
		if lockTables[name] != sum {
			tablesChanged = true
		}
	}
	if len(fresh) == 0 && !tablesChanged {
		return
	}
	if !*updateMigrationLock {
		ids := make([]string, len(fresh))
		for i, m := range fresh {
			ids[i] = m.id
		}
		t.Fatalf("migrations.lock is behind: unlocked migration ids %v (tables changed: %v); run\n  go test ./internal/store -run TestMigrationLock -update\nand commit migrations.lock with the migration", ids, tablesChanged)
	}
	var b strings.Builder
	b.WriteString(migrationLockHeader)
	for _, l := range lockMigs {
		fmt.Fprintf(&b, "migration %s %s\n", l.id, l.sum)
	}
	for _, m := range fresh {
		fmt.Fprintf(&b, "migration %s %s\n", m.id, m.sum)
	}
	for _, name := range sortedKeys(names) {
		if sum := tables[name]; sum != "" {
			fmt.Fprintf(&b, "table %s %s\n", name, sum)
		}
	}
	if err := os.WriteFile(migrationLockFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("migrations.lock updated: %d new migration id(s)", len(fresh))
}

// migrationFingerprints returns every entry of `var migrations` in source
// order with the sha256 of its token stream (comments and layout excluded).
func migrationFingerprints(path string) ([]lockedMigration, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}
	var list *ast.CompositeLit
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == "migrations" && i < len(vs.Values) {
					list, _ = vs.Values[i].(*ast.CompositeLit)
				}
			}
		}
	}
	if list == nil {
		return nil, fmt.Errorf("%s: no `var migrations = []migration{...}` literal", path)
	}
	var out []lockedMigration
	for _, elt := range list.Elts {
		lit, ok := elt.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("%s: migration entry at %s is not a composite literal", path, fset.Position(elt.Pos()))
		}
		id := ""
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "id" {
				if v, ok := kv.Value.(*ast.BasicLit); ok && v.Kind == token.STRING {
					id, _ = strconv.Unquote(v.Value)
				}
			}
		}
		if id == "" {
			return nil, fmt.Errorf("%s: migration entry at %s has no literal id", path, fset.Position(lit.Pos()))
		}
		start, end := fset.Position(lit.Pos()).Offset, fset.Position(lit.End()).Offset
		sum, strs := tokenFingerprint(src[start:end])
		out = append(out, lockedMigration{id: id, sum: sum, strs: strs})
	}
	return out, nil
}

// tokenFingerprint hashes the Go tokens of src (comments skipped, automatic
// semicolons normalized) and returns its unquoted string literals.
func tokenFingerprint(src []byte) (string, []string) {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	h := sha256.New()
	var strs []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON {
			lit = ";"
		}
		if tok == token.STRING {
			if v, err := strconv.Unquote(lit); err == nil {
				strs = append(strs, v)
			}
		}
		fmt.Fprintf(h, "%d\x00%s\x00", tok, lit)
	}
	return hex.EncodeToString(h.Sum(nil)), strs
}

var schemaTableHead = regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS ([A-Za-z0-9_]+) \(`)

// schemaTableFingerprints maps every schema.sql table to the sha256 of its
// CREATE TABLE statement with `--` comments removed and whitespace collapsed.
func schemaTableFingerprints(schema string) (map[string]string, error) {
	out := map[string]string{}
	for _, m := range schemaTableHead.FindAllStringSubmatch(schema, -1) {
		ddl, err := schemaTableDDL(m[1])
		if err != nil {
			return nil, err
		}
		var lines []string
		for _, line := range strings.Split(ddl, "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			lines = append(lines, line)
		}
		norm := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
		sum := sha256.Sum256([]byte(norm))
		out[m[1]] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

func readMigrationLock(path string) ([]lockedMigration, map[string]string, error) {
	tables := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, tables, err
	}
	var migs []lockedMigration
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, nil, fmt.Errorf("%s:%d: malformed line %q", path, n, line)
		}
		switch f[0] {
		case "migration":
			migs = append(migs, lockedMigration{id: f[1], sum: f[2]})
		case "table":
			tables[f[1]] = f[2]
		default:
			return nil, nil, fmt.Errorf("%s:%d: unknown entry kind %q", path, n, f[0])
		}
	}
	return migs, tables, sc.Err()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
