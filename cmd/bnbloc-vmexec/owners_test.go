package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

// confine reads its environment through protocol.HostConfineFromEnv: a
// deny list is honored when the workspace comes by flag, an unparsable list
// fails, and its own path resolves symlinks like the hooks' (one rule).
func TestConfineReadsTheProtocolEnv(t *testing.T) {
	ws := t.TempDir()
	spec, _, _, err := parseConfine([]string{"--workspace", ws, "--", "claude"}, confineEnv(map[string]string{
		coreprotocol.EnvConfineDenyWrite: `["` + ws + `/.claude/"]`,
	}))
	if err != nil || spec.Workspace != ws || strings.Join(spec.DenyWrite, ",") != ws+"/.claude/" {
		t.Fatalf("spec = %+v, %v (the env deny list must not be dropped)", spec, err)
	}
	if _, _, _, err := parseConfine([]string{"--workspace", ws, "--", "claude"}, confineEnv(map[string]string{
		coreprotocol.EnvConfineDenyWrite: ws + "/.claude/",
	})); err == nil {
		t.Fatal("an unparsable deny list was accepted")
	}

	real := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "bnbloc-vmexec"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bnbloc-vmexec")
	if err := os.Symlink(filepath.Join(real, "bnbloc-vmexec"), link); err != nil {
		t.Fatal(err)
	}
	env := confineEnv(map[string]string{coreprotocol.EnvConfineWorkspace: ws, coreprotocol.EnvVMExecPath: link})
	spec, _, _, err = parseConfine([]string{"--", "claude"}, env)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(filepath.Join(real, "bnbloc-vmexec"))
	if spec.VMExecDir != filepath.Dir(resolved) || vmexecPath(env) != resolved {
		t.Fatalf("VMExecDir = %s, vmexecPath = %s; want both from %s", spec.VMExecDir, vmexecPath(env), resolved)
	}
}

// Each harness name is spelled once: in the harnesses table.
func TestHarnessNamesListedOnce(t *testing.T) {
	counts := map[string]int{}
	for _, h := range harnesses {
		counts[h.name] = 0
	}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, _ := strconv.Unquote(lit.Value); v != "" {
					if _, known := counts[v]; known {
						counts[v]++
					}
				}
			}
			return true
		})
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("harness %q is spelled %d times: use the harnesses table", name, n)
		}
	}
}

func TestHookNeedsAHarnessWithADialect(t *testing.T) {
	for _, args := range [][]string{nil, {"codex"}, {"nope"}, {"claude", "x"}} {
		var stderr bytes.Buffer
		if code := hook(args, strings.NewReader("{}"), io.Discard, &stderr); code != exitHookBlock {
			t.Errorf("hook(%q) = %d, want %d", args, code, exitHookBlock)
		}
	}
	if code := hook([]string{"opencode"}, strings.NewReader(`{"tool":"read","args":{}}`), io.Discard, io.Discard); code != 0 {
		t.Fatalf("opencode hook = %d", code)
	}
}

// A host command string runs in the user's $SHELL everywhere (passthrough
// and the Claude prefix router agreed on nothing before: /bin/sh vs $SHELL).
func TestHostShell(t *testing.T) {
	if got := hostShell([]string{"PATH=/bin", "SHELL=/bin/zsh"}); got != "/bin/zsh" {
		t.Fatalf("hostShell = %s", got)
	}
	if got := hostShell([]string{"SHELL="}); got != "/bin/sh" {
		t.Fatalf("hostShell without SHELL = %s", got)
	}
	_, srv := newFakeSandbox(t)
	r := testRunner(t, srv, "/host/ws", nil, io.Discard, io.Discard)
	r.environ = append(r.environ, "SHELL=/bin/zsh")
	var got []string
	r.hostExec = func(argv, env []string) error { got = argv; return nil }
	if code := r.run(context.Background(), []string{"gh pr list"}, false); code != 0 || strings.Join(got, "|") != "/bin/zsh|-c|gh pr list" {
		t.Fatalf("passthrough = %d %q", code, got)
	}
}
