package wire

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEventKindsRegistered: RoomEventKinds is the one list of room event
// kinds. Every kind constant declared in this package is registered, the
// registry has no duplicates, and no Go source outside this package spells
// a registered kind as a string literal (a publish, a switch case or a
// comparison): it names the constant, so a kind cannot be published, or
// matched, without being registered — the metrics labels, replay and
// persistence all read the registry (2026-10-12 review: the metrics label
// list was kept by hand and missed member_joined).
func TestEventKindsRegistered(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range RoomEventKinds {
		if k.Name == "" || seen[k.Name] {
			t.Fatalf("registry entry %q is empty or duplicated", k.Name)
		}
		seen[k.Name] = true
		if k.Durable != k.Replay {
			t.Errorf("%s: a durable kind is exactly a replayed one (history and replay hold the same events)", k.Name)
		}
		if k.Revocation && (k.Durable || k.Replay) {
			t.Errorf("%s: a revocation is live only", k.Name)
		}
	}

	// Every Event*/Kind* string constant of this package is registered.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "Event") && !strings.HasPrefix(name.Name, "Kind") {
							continue
						}
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, _ := strconv.Unquote(lit.Value)
						if !seen[v] {
							t.Errorf("kind constant %s = %q is not in RoomEventKinds", name.Name, v)
						}
					}
				}
			}
		}
	}

	// No registered kind spelled as a literal outside this package.
	allowed := map[string]string{
		"internal/store/migrations.go": "agent_messages is also a table name",
		"internal/store/room_owned.go": "agent_messages is also a table name",
		"internal/store/retention.go":  "agent_messages is also a table name",
	}
	root := filepath.Join("..", "..")
	var problems []string
	for _, dir := range []string{"services", "internal", "core", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/wire/") || allowed[rel] != "" {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fs := token.NewFileSet()
			file := fs.AddFile(path, fs.Base(), len(src))
			var sc scanner.Scanner
			sc.Init(file, src, nil, 0)
			for {
				pos, tok, lit := sc.Scan()
				if tok == token.EOF {
					break
				}
				if tok != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit); err == nil && seen[v] {
					problems = append(problems, rel+":"+strconv.Itoa(fs.Position(pos).Line)+": "+lit)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("a room event kind spelled as a literal (name the wire constant):\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestEventKindAttributes(t *testing.T) {
	if !PersistedFeedKind(KindChatMessage) || !PersistedFeedKind(KindAgentMessages) || PersistedFeedKind(EventPresenceTyping) || PersistedFeedKind("bogus") {
		t.Fatal("durable kinds are exactly chat_message and agent_messages")
	}
	for _, k := range []string{EventPresenceTyping, EventContentSaved, EventArtifactState, EventArtifactUpdated, EventAgentSessionState, EventMemberRemoved, EventRoomDeleted} {
		if ReplayedKind(k) {
			t.Errorf("%s must not replay", k)
		}
	}
}
