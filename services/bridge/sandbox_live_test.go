package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	exec, err := rt.prepare(ctx, driveKey{room: "room-1", core: "core-a", provider: "codex", sandboxed: true}, "alice", nil, sandboxExecTokenTTL, false, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The next turn re-ensures the space and fences it: the first turn's token
	// is revoked with its processes, the second turn's is the one that works.
	exec, err = rt.prepare(ctx, driveKey{room: "room-1", core: "core-a", provider: "codex", sandboxed: true}, "alice", nil, sandboxExecTokenTTL, false, nil)
	if err != nil {
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

// TestSandboxPrepare_livePerSpaceMounts: two rooms of one machine both get a
// component's folder (the sandbox refuses one guest path for two spaces), each
// from a tree of its own over the same files, read-only, named by a variable of
// the space; and a new version replaces a space's tree on its next turn.
func TestSandboxPrepare_livePerSpaceMounts(t *testing.T) {
	base := os.Getenv("SANDBOX_URL")
	if base == "" {
		t.Skip("SANDBOX_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin := corepkg.Endpoint{BaseURL: base, Token: os.Getenv("SANDBOX_TOKEN")}
	c := sandbox.NewClient(nil)
	c.REST.Timeout = 3 * time.Minute
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guestRoot := filepath.Join(root, "guest-home")
	mountsDir := t.TempDir()
	name := "bridge-ps-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m, err := c.CreateMachine(ctx, admin, sandbox.CreateMachineRequest{
		Name: name, Image: conformance.DefaultImage, MemoryMiB: 512,
		Shares: []sandbox.Share{{Name: "workspaces", HostPath: root, GuestPath: root}},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteMachine(context.Background(), admin, m.ID) })

	version := func(v string) string {
		dir := filepath.Join(MountsHostRoot(root, "agents"), "tool", v)
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", "tool"), []byte("#!/bin/sh\necho tool "+v+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	point := func(dir string) {
		raw, _ := json.Marshal(descriptor(desc{"host": dir, "guest": guestRoot + "/tool", "perSpace": true, "env": "BNBLOC_MOUNT_TOOL"}))
		if err := os.MkdirAll(filepath.Join(mountsDir, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mountsDir, "agents", mountsFileName), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	point(version("1.0.0"))

	rt := newSandboxRuntime(SandboxConfig{Mode: SandboxBorrowed, URL: base, Token: admin.Token, VMExec: "/x/bnbloc-vmexec", Machine: name, WorkspacesRoot: root, MountsDir: mountsDir, MountGuestRoots: []string{guestRoot}})
	if ready, reason := rt.status(ctx); !ready {
		t.Fatalf("sandbox not ready: %s", reason)
	}
	run := func(room, script string) string {
		k := driveKey{room: room, core: "core-a", provider: "codex", sandboxed: true}
		exec, err := rt.prepare(ctx, k, "alice", nil, sandboxExecTokenTTL, false, nil)
		if err != nil {
			t.Fatalf("prepare %s: %v", room, err)
		}
		s := exec.Sandbox
		res, err := c.SpaceRun(ctx, corepkg.Endpoint{BaseURL: base, Token: s.ExecToken}, s.Machine, s.Space, sandbox.ExecRequest{Command: []string{"sh", "-c", script}, HostWorkdir: s.Workdir})
		if err != nil {
			t.Fatalf("run in %s: %v", room, err)
		}
		out, _ := base64.StdEncoding.DecodeString(res.StdoutBase64)
		errb, _ := base64.StdEncoding.DecodeString(res.StderrBase64)
		return strings.TrimSpace(string(out)) + "|" + strings.TrimSpace(string(errb))
	}
	use := `"$BNBLOC_MOUNT_TOOL/bin/tool"; echo "$BNBLOC_MOUNT_TOOL"; touch "$BNBLOC_MOUNT_TOOL/x" 2>&1 | sed 's/.*: //'`
	a, b := run("room-a", use), run("room-b", use)
	for room, got := range map[string]string{"room-a": a, "room-b": b} {
		if !strings.HasPrefix(got, "tool 1.0.0\n"+guestRoot+"/tool-") || !strings.Contains(got, "Read-only file system") {
			t.Errorf("%s: %q: the agent's program runs from its own path, read-only", room, got)
		}
	}
	if strings.Split(a, "\n")[1] == strings.Split(b, "\n")[1] {
		t.Errorf("both rooms were given one guest path: %q %q", a, b)
	}
	// A new version, and the same room's next turn: its tree is replaced.
	point(version("2.0.0"))
	if got := run("room-a", `"$BNBLOC_MOUNT_TOOL/bin/tool"`); !strings.HasPrefix(got, "tool 2.0.0") {
		t.Errorf("after the update: %q", got)
	}
	if got := run("room-b", `"$BNBLOC_MOUNT_TOOL/bin/tool"`); !strings.HasPrefix(got, "tool 2.0.0") {
		t.Errorf("the other room's next turn: %q", got)
	}
}
