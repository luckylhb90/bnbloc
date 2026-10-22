package protocol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
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
	// A deny-read list alone is honored too, and round-trips VMExecEnv.
	c, err = HostConfineFromEnv(env(map[string]string{EnvConfineDenyRead: `["/h/.config/acme/mail"]`}))
	if err != nil || c == nil || !reflect.DeepEqual(c.DenyRead, []string{"/h/.config/acme/mail"}) {
		t.Fatalf("deny-read alone = %+v, %v", c, err)
	}
	sent := map[string]string{}
	for _, kv := range VMExecEnv(SandboxExecution{Workdir: "/w", HostConfine: &HostConfine{Workspace: "/w", DenyRead: []string{"/h/.config/acme/mail"}}}) {
		k, v, _ := strings.Cut(kv, "=")
		sent[k] = v
	}
	if back, err := HostConfineFromEnv(env(sent)); err != nil || !reflect.DeepEqual(back.DenyRead, []string{"/h/.config/acme/mail"}) {
		t.Fatalf("VMExecEnv does not carry DenyRead: %+v %v", back, err)
	}
	if _, err := HostConfineFromEnv(env(map[string]string{EnvConfineDenyWrite: `/w/.claude`})); err == nil {
		t.Fatal("an unparsable deny list must fail, never read as empty")
	}
	if got := PassthroughFromEnv(env(map[string]string{EnvVMExecPassthrough: " git, ,gh "})); !reflect.DeepEqual(got, []string{"git", "gh"}) {
		t.Fatalf("PassthroughFromEnv = %v", got)
	}
}

// One allowlist for both ends: the default list plus a run's extra names,
// each once; ForwardedEnv admits exactly what it lists.
func TestForwardEnvAllowlistAndForwardedEnv(t *testing.T) {
	if got := ForwardEnvAllowlist(nil); !reflect.DeepEqual(got, VMExecForwardEnv) {
		t.Fatalf("no extra = %v; want the default list", got)
	}
	allow := ForwardEnvAllowlist([]string{"EXAMPLE_API_KEY", "TERM", "EXAMPLE_*"})
	if len(allow) != len(VMExecForwardEnv)+2 || !slices.Contains(allow, "EXAMPLE_API_KEY") || !slices.Contains(allow, "EXAMPLE_*") {
		t.Fatalf("allowlist = %v", allow)
	}
	environ := []string{"TERM=xterm", "EXAMPLE_API_KEY=k", "EXAMPLE_URL=u", "SECRET=s", "LC_ALL=C"}
	if got := ForwardedEnv(environ, VMExecForwardEnv); !reflect.DeepEqual(got, map[string]string{"TERM": "xterm", "LC_ALL": "C"}) {
		t.Fatalf("default forward = %v", got)
	}
	if got := ForwardedEnv(environ, allow); !reflect.DeepEqual(got, map[string]string{"TERM": "xterm", "LC_ALL": "C", "EXAMPLE_API_KEY": "k", "EXAMPLE_URL": "u"}) {
		t.Fatalf("forward with the owner's names = %v", got)
	}
}

// The vmexec environment is spelled only in vmexec_env.go: every other Go
// file (the helper itself, the bridge, tests) uses the Env* constants and
// the parsers, so the two sides cannot drift (they did: confine read the
// allow lists without the workspace and resolved its own path differently).
func TestVMExecEnvNamesOnlyInProtocol(t *testing.T) {
	prefixes := []string{"BNBLOC_" + "CONFINE_", "BNBLOC_" + "VMEXEC_"}
	fset := token.NewFileSet()
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Tests: true}, func(rel string, src []byte) {
		if rel == "core/protocol/vmexec_env.go" {
			return
		}
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
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
	})
}
