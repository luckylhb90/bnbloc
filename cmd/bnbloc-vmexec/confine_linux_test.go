//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

// TestConfineBwrap_agentConfigPaths runs the real bubblewrap: a confined
// shell may write its workspace but can neither create nor edit the
// agent-config paths, including the ones the workspace did not have yet,
// nor move a protected path (or its parent) out of the way.
// It needs bwrap and user namespaces (a container runs it --privileged).
func TestConfineBwrap_agentConfigPaths(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}
	if out, err := exec.Command("bwrap", "--ro-bind", "/", "/", "--", "true").CombinedOutput(); err != nil {
		t.Skipf("bubblewrap cannot create a sandbox here: %v %s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "bnbloc-vmexec")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	ws := filepath.Join(home, "bnbloc-workspaces", "room-a")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("role"), 0o644); err != nil {
		t.Fatal(err)
	}
	deny := `["` + strings.Join([]string{
		filepath.Join(ws, "CLAUDE.md"), filepath.Join(ws, "AGENTS.md"), filepath.Join(ws, ".mcp.json"),
		filepath.Join(ws, ".claude/skills") + "/", filepath.Join(ws, ".agents/skills") + "/", filepath.Join(ws, ".opencode") + "/",
	}, `","`) + `"]`
	script := `cd "$1"
try() { if sh -c "$1" 2>/dev/null; then echo "WROTE $2"; fi; }
try 'echo x > CLAUDE.md' CLAUDE.md
try 'echo x > AGENTS.md' AGENTS.md
try 'echo x > .mcp.json' .mcp.json
try 'mkdir -p .claude/skills/x && echo x > .claude/skills/x/SKILL.md' .claude/skills
try 'mkdir -p .agents/skills/x' .agents/skills
try 'echo x > .opencode/x' .opencode
try 'rm -f CLAUDE.md' rm-CLAUDE.md
try 'mv .claude .claude-moved && mkdir -p .claude/skills/x' renamed-parent
try 'mv .opencode .opencode-moved' renamed-dir
echo ok > ok.txt`
	cmd := exec.Command(bin, "confine", "--workspace", ws, "--", "sh", "-c", script, "sh", ws)
	cmd.Env = append(os.Environ(), "HOME="+home, coreprotocol.EnvConfineDenyWrite+"="+deny)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("confined run: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "WROTE") {
		t.Fatalf("the confined shell wrote an agent-config path:\n%s", out)
	}
	if raw, err := os.ReadFile(filepath.Join(ws, "ok.txt")); err != nil || string(raw) != "ok\n" {
		t.Fatalf("the workspace is not writable: %q %v\n%s", raw, err, out)
	}
	if raw, _ := os.ReadFile(filepath.Join(ws, "AGENTS.md")); string(raw) != "role" {
		t.Fatalf("AGENTS.md changed: %q", raw)
	}
	if fi, err := os.Stat(filepath.Join(ws, ".claude/skills")); err != nil || !fi.IsDir() {
		t.Fatalf(".claude/skills placeholder missing: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(ws, ".claude/skills")); len(entries) != 0 {
		t.Fatalf(".claude/skills gained entries: %v", entries)
	}
}
