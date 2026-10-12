package protocol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestHostConfineFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if c, err := HostConfineFromEnv(env(nil)); c != nil || err != nil {
		t.Fatalf("no confine env = %+v, %v; want nil", c, err)
	}
	// A list without the workspace variable (confine --workspace names it)
	// is honored: a deny list is never dropped.
	c, err := HostConfineFromEnv(env(map[string]string{EnvConfineDenyWrite: `["/w/.claude/"]`}))
	if err != nil || c == nil || c.Workspace != "" || !reflect.DeepEqual(c.DenyWrite, []string{"/w/.claude/"}) {
		t.Fatalf("deny list alone = %+v, %v", c, err)
	}
	c, err = HostConfineFromEnv(env(map[string]string{EnvConfineWorkspace: " /w ", EnvConfineAllowRead: `[]`, EnvConfineAllowWrite: `["/o"]`}))
	if err != nil || c.Workspace != "/w" || c.AllowRead != nil || !reflect.DeepEqual(c.AllowWrite, []string{"/o"}) {
		t.Fatalf("full = %+v, %v", c, err)
	}
	if _, err := HostConfineFromEnv(env(map[string]string{EnvConfineDenyWrite: `/w/.claude`})); err == nil {
		t.Fatal("an unparsable deny list must fail, never read as empty")
	}
	if got := PassthroughFromEnv(env(map[string]string{EnvVMExecPassthrough: " git, ,gh "})); !reflect.DeepEqual(got, []string{"git", "gh"}) {
		t.Fatalf("PassthroughFromEnv = %v", got)
	}
}

// The vmexec environment is spelled only in vmexec_env.go: every other Go
// file (the helper itself, the bridge, tests) uses the Env* constants and
// the parsers, so the two sides cannot drift (they did: confine read the
// allow lists without the workspace and resolved its own path differently).
func TestVMExecEnvNamesOnlyInProtocol(t *testing.T) {
	prefixes := []string{"BNBLOC_" + "CONFINE_", "BNBLOC_" + "VMEXEC_"}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "target", ".git", "web", "apps":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if filepath.ToSlash(rel) == "core/protocol/vmexec_env.go" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			for _, p := range prefixes {
				if strings.Contains(v, p) {
					t.Errorf("%s:%d spells %s…: use the protocol.Env* constants", rel, fset.Position(lit.Pos()).Line, p)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
