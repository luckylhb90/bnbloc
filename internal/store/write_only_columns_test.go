package store

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// writeOnlyColumnAllowed are schema.sql columns no store code reads, each
// with the reason it is kept anyway. A column nothing reads is dead weight
// (D3: write-only columns are deleted); add one here only with a reason.
var writeOnlyColumnAllowed = map[string]string{
	"extension_audit.key_id": "forensic record of the signing key an install was verified with; read by operators in the table, not by the console",
	// Retiring: no code reads or writes the room notes tables any more (the
	// feature was withdrawn on 2026-10-17); they stay in schema.sql for one release
	// of version skew, and the next release's migration drops them.
	"room_note_revisions.at_ms":      "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.body":       "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.changed_by": "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.note_id":    "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.pinned":     "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.revision":   "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.status":     "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.title":      "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_note_revisions.type":       "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.author":              "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.body":                "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.created_at_ms":       "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.note_id":             "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.pinned":              "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.revision":            "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.sources_json":        "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.status":              "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.supersedes":          "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.title":               "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.type":                "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.updated_at_ms":       "retiring: the withdrawn room notes feature; dropped by the next release",
	"room_notes.verified_at_ms":      "retiring: the withdrawn room notes feature; dropped by the next release",
	// Retiring: room packages replaced room templates on 2026-10-20
	// (docs/prd/2026-10-20-rooms-as-components.md); no code reads or writes
	// the table, which stays for one release of version skew.
	"room_templates.body_json":     "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.created_at_ms": "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.created_by":    "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.description":   "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.name":          "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.template_id":   "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.tenant_id":     "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.updated_at_ms": "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.use_count":     "retiring: room templates, replaced by room packages; dropped by the next release",
	"room_templates.visibility":    "retiring: room templates, replaced by room packages; dropped by the next release",
	// Bookkeeping timestamps that predate the check; candidates for the
	// next removal migration (or a reader that needs them).
	"org_sync_default_rooms.granted_at_ms": "pre-existing: when organization sync granted the default room",
	"room_read_state.updated_at_ms":        "pre-existing: when the member last moved the read marker",
	"user_profiles.updated_at_ms":          "pre-existing: when the display name last changed",
}

// TestNoWriteOnlyColumns: every column of schema.sql is read by the store
// (docs/prd/2026-10-12-code-health-remediation.md, 4.5). The check is a
// static heuristic over the string literals of the non-test Go sources of
// this package (migration DDL excluded): a column counts as read when its
// name appears, as a whole word, in a literal of a file whose literals also
// name its table, once the write-only spellings are removed (an INSERT's
// column list, an upsert's "col = VALUES(col)"). It does not tell a WHERE
// from a SET, so a column only ever assigned in an UPDATE still passes; it
// catches the columns nothing mentions outside their writes.
func TestNoWriteOnlyColumns(t *testing.T) {
	tables := schemaTableColumns(t)
	files := storeSQLLiterals(t)
	var unread []string
	for table, cols := range tables {
		tableRE := regexp.MustCompile(`\b` + table + `\b`)
		for _, col := range cols {
			key := table + "." + col
			colRE := regexp.MustCompile(`\b` + col + `\b`)
			read := false
			for _, text := range files {
				if tableRE.MatchString(text) && colRE.MatchString(text) {
					read = true
					break
				}
			}
			_, allowed := writeOnlyColumnAllowed[key]
			switch {
			case !read && !allowed:
				unread = append(unread, key)
			case read && allowed:
				t.Errorf("%s is read now: remove it from writeOnlyColumnAllowed", key)
			}
		}
	}
	for key := range writeOnlyColumnAllowed {
		dot := strings.Index(key, ".")
		if !containsString(tables[key[:dot]], key[dot+1:]) {
			t.Errorf("writeOnlyColumnAllowed names %s, which schema.sql does not define", key)
		}
	}
	sort.Strings(unread)
	if len(unread) > 0 {
		t.Fatalf("schema.sql columns no store code reads (drop them in a new migration, or allow one with a reason):\n  %s",
			strings.Join(unread, "\n  "))
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var (
	insertColumnList = regexp.MustCompile(`(?is)(INSERT\s+(?:IGNORE\s+)?INTO\s+\w+)\s*\([^)]*\)`)
	upsertAssignment = regexp.MustCompile(`(?i)\b\w+\s*=\s*VALUES\(\w+\)`)
)

// storeSQLLiterals returns, per non-test Go file of the package (the
// migration DDL excluded), its string literals joined, with INSERT column
// lists and upsert assignments removed.
func storeSQLLiterals(t *testing.T) map[string]string {
	t.Helper()
	out := storeLiterals(t)
	for path, text := range out {
		text = insertColumnList.ReplaceAllString(text, "$1")
		out[path] = upsertAssignment.ReplaceAllString(text, "")
	}
	return out
}

// storeLiterals returns, per non-test Go file of the package (the migration
// DDL excluded), its string literals joined: every SQL the store runs, reads
// and writes alike.
func storeLiterals(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{"migrations.go": true, "schema_ddl.go": true}
	out := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || skip[path] {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		var s scanner.Scanner
		s.Init(fset.AddFile(path, -1, len(src)), src, nil, 0)
		var b strings.Builder
		for {
			_, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", path, lit, err)
			}
			b.WriteString(v)
			b.WriteString("\n")
		}
		out[path] = b.String()
	}
	if len(out) < 20 {
		t.Fatalf("read only %d store sources", len(out))
	}
	return out
}
