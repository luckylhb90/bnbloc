package bridge

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/sandbox/conformance"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// TestSandboxPrepare_liveSidecar runs the bridge's per-turn preparation
// against a real Sandbox Protocol implementation (SANDBOX_URL /
// SANDBOX_TOKEN): a machine sharing a workspaces root at the identical guest
// path (what the desktop prepares), then prepare() twice — the space mount
// nested in the share, re-ensure, and a space token that runs commands in the
// workspace as the space user and nothing else.
func TestSandboxPrepare_liveSidecar(t *testing.T) {
	base := os.Getenv("SANDBOX_URL")
	if base == "" {
		t.Skip("SANDBOX_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	admin := corepkg.Endpoint{BaseURL: base, Token: os.Getenv("SANDBOX_TOKEN")}
	c := sandbox.NewClient(nil)
	c.REST.Timeout = 3 * time.Minute
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "bridge-it-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m, err := c.CreateMachine(ctx, admin, sandbox.CreateMachineRequest{
		Name: name, Image: conformance.DefaultImage, MemoryMiB: 512,
		Shares: []sandbox.Share{{Name: "workspaces", HostPath: root, GuestPath: root}},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteMachine(context.Background(), admin, m.ID) })

	rt := newSandboxRuntime(SandboxConfig{Mode: SandboxBorrowed, URL: base, Token: admin.Token, VMExec: "/x/bnbloc-vmexec", Machine: name, WorkspacesRoot: root})
	if ready, reason := rt.status(ctx); !ready {
		t.Fatalf("sandbox not ready: %s", reason)
	}
	exec, err := rt.prepare(ctx, "room-1", "core-a", "codex", "alice", nil, sandboxExecTokenTTL)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := rt.prepare(ctx, "room-1", "core-a", "codex", "alice", nil, sandboxExecTokenTTL); err != nil {
		t.Fatalf("re-prepare (per-turn re-ensure): %v", err)
	}
	s := exec.Sandbox
	if err := os.WriteFile(filepath.Join(s.Workdir, "from-host.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	tok := corepkg.Endpoint{BaseURL: base, Token: s.ExecToken}
	res, err := c.SpaceRun(ctx, tok, s.Machine, s.Space, sandbox.ExecRequest{
		Command: []string{"sh", "-c", "pwd; id -u; cat from-host.txt; echo guest > from-guest.txt"}, HostWorkdir: s.Workdir,
	})
	if err != nil {
		t.Fatalf("space run with the exec token: %v", err)
	}
	out, _ := base64.StdEncoding.DecodeString(res.StdoutBase64)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if res.ExitCode != 0 || len(lines) != 3 || lines[0] != s.Workdir || lines[1] == "0" || lines[2] != "host" {
		t.Fatalf("guest view = %q (exit %d)", out, res.ExitCode)
	}
	if raw, err := os.ReadFile(filepath.Join(s.Workdir, "from-guest.txt")); err != nil || string(raw) != "guest\n" {
		t.Fatalf("host view of the guest write = %q %v", raw, err)
	}
	if _, err := c.Run(ctx, tok, s.Machine, sandbox.ExecRequest{Command: []string{"true"}}); sandbox.ErrorCodeOf(err) != sandbox.ErrorForbidden {
		t.Fatalf("machine-level run with the exec token: %v, want forbidden", err)
	}
}
