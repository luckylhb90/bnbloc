package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

func confineEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseConfine_readsEnvAndFlags(t *testing.T) {
	ws := t.TempDir()
	spec, argv, printOnly, err := parseConfine([]string{"--allow-read", "/opt/tools", "--print-profile", "--", "claude", "-p", "hi"}, confineEnv(map[string]string{
		coreprotocol.EnvConfineWorkspace:  ws,
		coreprotocol.EnvConfineAllowWrite: `["/data/out"]`,
		coreprotocol.EnvVMExecPath:        "/apps/vm/bnbloc-vmexec",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !printOnly || strings.Join(argv, " ") != "claude -p hi" || spec.Workspace != ws || strings.Join(spec.Harnesses, ",") != "claude" || spec.VMExecDir != "/apps/vm" {
		t.Fatalf("spec=%+v argv=%v", spec, argv)
	}
	if strings.Join(spec.AllowRead, ",") != "/opt/tools" || strings.Join(spec.AllowWrite, ",") != "/data/out" {
		t.Fatalf("allow lists = %v / %v", spec.AllowRead, spec.AllowWrite)
	}
	for _, bad := range [][]string{{"--", "x"}, {"--workspace", "rel", "--", "x"}, {"--workspace", ws}, {"--bogus", "--", "x"}, {"--workspace", ws, "--allow-read", "rel", "--", "x"}} {
		if _, _, _, err := parseConfine(bad, confineEnv(nil)); err == nil {
			t.Fatalf("parseConfine(%q) accepted", bad)
		}
	}
}

func TestHarnessFor(t *testing.T) {
	for in, want := range map[string]string{"/usr/local/bin/claude": "claude", "codex": "codex", "opencode-linux-arm64": "opencode", "/bin/sh": "generic"} {
		if got := harnessFor(in); got != want {
			t.Errorf("harnessFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func testSpec(t *testing.T, harness string) confineSpec {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{"workspace/other-project", "bnbloc-workspaces/room-a", "bnbloc-workspaces/room-b", ".claude/plugins", ".codex", "Library", "plugin-src"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(home, ".claude/plugins/installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"p@m":[{"installPath":"`+filepath.Join(home, "plugin-src")+`"}]}}`), 0o644)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	spec := confineSpec{
		Home: home, Workspace: filepath.Join(home, "bnbloc-workspaces/room-a"), Harnesses: strings.Split(harness, ","),
		VMExecDir: "/apps/vm", AllowRead: []string{"/opt/tools"}, AllowWrite: []string{"/data/out"}, TempDirs: []string{"/tmp"},
	}
	spec.resolve()
	return spec
}

func contains(list []string, p string) bool {
	for _, v := range list {
		if v == canonical(p) {
			return true
		}
	}
	return false
}

func TestConfineSpec_pathSets(t *testing.T) {
	s := testSpec(t, "claude")
	deny, allow, write := s.denyReads(), s.allowReads(), s.allowWrites()
	h := func(rel string) string { return filepath.Join(s.Home, rel) }
	for _, p := range []string{h(".ssh"), h("Library/Keychains"), h(".codex"), h("bnbloc-workspaces"), h("workspace"), h("plugin-src")} {
		if !contains(deny, p) {
			t.Errorf("deny reads lacks %s", p)
		}
	}
	for _, p := range []string{h("Library"), h(".claude"), s.Workspace} {
		if contains(deny, p) {
			t.Errorf("deny reads must not include %s", p)
		}
	}
	for _, p := range []string{s.Workspace, h(".claude"), h("plugin-src"), "/opt/tools", "/data/out", "/apps/vm"} {
		if !contains(allow, p) {
			t.Errorf("allow reads lacks %s", p)
		}
	}
	for _, p := range []string{s.Workspace, h(".claude/projects"), h(".claude/transcripts"), h(".claude/plugins/data"), "/data/out"} {
		if !contains(write, p) {
			t.Errorf("allow writes lacks %s", p)
		}
	}
	// Config that executes on the host later is never writable: settings,
	// hooks, plugins, skills — and no tool-wide cache either.
	for _, p := range []string{"/opt/tools", h("bnbloc-workspaces/room-b"), h(".claude"), h(".claude/hooks"), h(".claude/plugins"), h(".claude/skills"), h(".cache"), h("Library/Caches")} {
		if contains(write, p) {
			t.Errorf("%s must not be writable", p)
		}
	}
	files := strings.Join(s.allowWriteFiles(), "\n")
	if !strings.Contains(files, regexp.QuoteMeta(canonical(h(".claude/history.jsonl")))) || strings.Contains(files, "settings") {
		t.Errorf("state files = %s", files)
	}

	codex := testSpec(t, "codex")
	if contains(codex.denyReads(), filepath.Join(codex.Home, ".codex")) || !contains(codex.denyReads(), filepath.Join(codex.Home, ".claude")) {
		t.Error("a harness reads its own state and never another harness's")
	}
}

func TestConfineSpec_seatbeltProfile(t *testing.T) {
	s := testSpec(t, "claude")
	p := s.seatbeltProfile()
	for _, want := range []string{
		"(allow default)", "(deny file-read-data", "(allow file-read-data", "(deny file-write*)", "(allow file-write*",
		`(subpath "` + canonical(s.Workspace) + `")`,
		`(regex "^` + strings.ReplaceAll(regexp.QuoteMeta(canonical(filepath.Join(s.Home, ".claude.json"))), `\`, `\\`) + `")`,
		`(literal "/dev/null")`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %s:\n%s", want, p)
		}
	}
	// Precedence: the allow rules must come after the deny of the same op.
	if strings.Index(p, "(deny file-read-data") > strings.Index(p, "(allow file-read-data") {
		t.Error("allow file-read-data must follow the deny")
	}
	// The core-declared deny-write comes last so it wins over the allows.
	s.DenyWrite = []string{filepath.Join(s.Workspace, ".routing")}
	p2 := s.seatbeltProfile()
	if i, j := strings.LastIndex(p2, "(deny file-write*"), strings.Index(p2, "(allow file-write*"); i < j || !strings.Contains(p2[i:], canonical(filepath.Join(s.Workspace, ".routing"))) {
		t.Errorf("deny-write must follow the allow:\n%s", p2)
	}
	if strings.Contains(p, "file-read*") {
		t.Error("the profile must deny file-read-data, never file-read* (metadata must stay readable)")
	}
	if strings.Contains(p, "mach-lookup") {
		t.Error("keychain access is only denied on request")
	}
	s.DenyKeychain = true
	if !strings.Contains(s.seatbeltProfile(), `com.apple.SecurityServer`) {
		t.Error("--deny-keychain not rendered")
	}
}

func TestConfineSpec_bwrapArgs(t *testing.T) {
	s := testSpec(t, "claude")
	args := strings.Join(s.bwrapArgs("/usr/bin/bwrap", []string{"claude", "-p", "x"}), " ")
	for _, want := range []string{
		"/usr/bin/bwrap --die-with-parent --ro-bind / /",
		"--tmpfs " + canonical(filepath.Join(s.Home, "bnbloc-workspaces")),
		"--bind " + canonical(s.Workspace) + " " + canonical(s.Workspace),
		"-- claude -p x",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("bwrap args lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--unshare-net") {
		t.Error("the CLI needs the network")
	}
}

func TestConfine_extraHarnessStateIsWritable_onlyWhenListed(t *testing.T) {
	s := testSpec(t, "opencode")
	h := func(rel string) string { return filepath.Join(s.Home, rel) }
	if contains(s.allowWrites(), h(".claude/transcripts")) || !contains(s.denyReads(), h(".claude")) {
		t.Fatal("an unlisted harness's state must stay denied")
	}
	if !contains(s.denyWrites(), h(".cache/opencode/packages")) {
		t.Fatal("opencode's plugin package cache must stay read-only")
	}
	both := testSpec(t, "opencode,claude")
	if !contains(both.allowWrites(), filepath.Join(both.Home, ".claude/transcripts")) || contains(both.denyReads(), filepath.Join(both.Home, ".claude")) {
		t.Fatal("--harness claude beside opencode must open claude's state")
	}
}

func TestParseConfine_denyWrite(t *testing.T) {
	ws := t.TempDir()
	spec, _, _, err := parseConfine([]string{"--deny-write", filepath.Join(ws, ".routing"), "--harness", "opencode", "--harness", "claude", "--", "opencode"},
		confineEnv(map[string]string{coreprotocol.EnvConfineWorkspace: ws, coreprotocol.EnvConfineDenyWrite: `["/x/y"]`}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(spec.DenyWrite, ",") != "/x/y,"+filepath.Join(ws, ".routing") || strings.Join(spec.Harnesses, ",") != "opencode,claude" {
		t.Fatalf("spec = %+v", spec)
	}
}

// A temp-then-rename file beside a read-only config dir is allowed by prefix
// (the dir itself stays non-writable).
func TestParseConfine_allowWritePrefix(t *testing.T) {
	ws := t.TempDir()
	cfg := t.TempDir()
	prefix := filepath.Join(cfg, ".state-")
	spec, _, _, err := parseConfine([]string{"--allow-write-prefix", prefix, "--", "generic-cli"},
		confineEnv(map[string]string{coreprotocol.EnvConfineWorkspace: ws}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(spec.AllowWritePrefix, ",") != prefix {
		t.Fatalf("prefixes = %v", spec.AllowWritePrefix)
	}
	want := "^" + regexp.QuoteMeta(canonical(prefix))
	found := false
	for _, re := range spec.allowWriteFiles() {
		if re == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("allowWriteFiles %v lacks %s", spec.allowWriteFiles(), want)
	}
	for _, d := range spec.allowWrites() {
		if d == canonical(cfg) {
			t.Fatal("the config dir itself must not become writable")
		}
	}
	if _, _, _, err := parseConfine([]string{"--allow-write-prefix", "rel/.state-", "--", "x"},
		confineEnv(map[string]string{coreprotocol.EnvConfineWorkspace: ws})); err == nil {
		t.Fatal("relative prefix accepted")
	}
}
