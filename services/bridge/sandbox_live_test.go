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
	exec, err := rt.prepare(ctx, driveKey{room: "room-1", core: "core-a", provider: "codex", sandboxed: true}, "", "alice", nil, sandboxExecTokenTTL, false, nil, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The next turn re-ensures the space and fences it: the first turn's token
	// is revoked with its processes, the second turn's is the one that works.
	exec, err = rt.prepare(ctx, driveKey{room: "room-1", core: "core-a", provider: "codex", sandboxed: true}, "", "alice", nil, sandboxExecTokenTTL, false, nil, nil)
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
		dir := filepath.Join(MountsHostRoot(DefaultMountsRoot(root), "agents"), "tool", v)
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
		exec, err := rt.prepare(ctx, k, "", "alice", nil, sandboxExecTokenTTL, false, nil, nil)
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

// TestSandboxPrepare_liveFixedPathMountsServeEveryRoom: a component's mount at
// one fixed guest path (local skills' folders) reaches every room of a sandbox
// that advertises privateMounts, each room reading it read-only, and a room with
// no such mount sees nothing there.
func TestSandboxPrepare_liveFixedPathMountsServeEveryRoom(t *testing.T) {
	base := os.Getenv("SANDBOX_URL")
	if base == "" {
		t.Skip("SANDBOX_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin := corepkg.Endpoint{BaseURL: base, Token: os.Getenv("SANDBOX_TOKEN")}
	c := sandbox.NewClient(nil)
	c.REST.Timeout = 3 * time.Minute
	if d, err := c.Discover(ctx, admin); err != nil || !sandbox.HasCapability(d, sandbox.CapabilityPrivateMounts) {
		t.Skip("the sandbox does not advertise privateMounts")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guestRoot := filepath.Join(root, "guest-home")
	mountsDir := t.TempDir()
	name := "bridge-fx-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m, err := c.CreateMachine(ctx, admin, sandbox.CreateMachineRequest{
		Name: name, Image: "bnbloc.local/guest:0.1.0", MemoryMiB: 512,
		Shares: []sandbox.Share{{Name: "workspaces", HostPath: root, GuestPath: root}},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteMachine(context.Background(), admin, m.ID) })
	skill := filepath.Join(MountsHostRoot(DefaultMountsRoot(root), "local-skills"), "archify")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("the skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(descriptor(desc{"host": skill, "guest": guestRoot + "/.agents/skills/archify"}))
	if err := os.MkdirAll(filepath.Join(mountsDir, "local-skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountsDir, "local-skills", mountsFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rt := newSandboxRuntime(SandboxConfig{Mode: SandboxBorrowed, URL: base, Token: admin.Token, VMExec: "/x/bnbloc-vmexec", Machine: name, WorkspacesRoot: root, MountsDir: mountsDir, MountGuestRoots: []string{guestRoot}})
	if ready, reason := rt.status(ctx); !ready {
		t.Fatalf("sandbox not ready: %s", reason)
	}
	run := func(room string, borrowed bool) string {
		k := driveKey{room: room, core: "core-a", provider: "codex", sandboxed: true}
		exec, err := rt.prepare(ctx, k, "", "alice", nil, sandboxExecTokenTTL, borrowed, nil, nil)
		if err != nil {
			t.Fatalf("prepare %s: %v", room, err)
		}
		s := exec.Sandbox
		res, err := c.SpaceRun(ctx, corepkg.Endpoint{BaseURL: base, Token: s.ExecToken}, s.Machine, s.Space, sandbox.ExecRequest{Command: []string{"sh", "-c", "cat " + guestRoot + "/.agents/skills/archify/SKILL.md 2>/dev/null; echo; touch " + guestRoot + "/.agents/skills/archify/x 2>&1 | sed 's/.*: //'"}, HostWorkdir: s.Workdir})
		if err != nil {
			t.Fatalf("run in %s: %v", room, err)
		}
		out, _ := base64.StdEncoding.DecodeString(res.StdoutBase64)
		return strings.TrimSpace(string(out))
	}
	for _, room := range []string{"room-a", "room-b", "room-c"} {
		if got := run(room, false); got != "the skill\nRead-only file system" {
			t.Errorf("%s (the owner's own run): %q, want the skill, read-only", room, got)
		}
	}
	// The mount is for the owner's runs: a run another member borrows has none, and
	// does not see the owner's through the machine either.
	if got := run("room-d", true); strings.Contains(got, "the skill") {
		t.Errorf("a borrowed run sees the owner's own mount: %q", got)
	}
}

// TestSandboxPrepare_liveWithTheDesktopsFolderLayout: the manager's own install, made
// the way the desktop gives its plugin the folder (<workspaces root>/.mounts/<account>/
// <plugin>, the account layer included) and named to the bridge (MountsRoot), reaches a
// room's space and runs. The bridge once looked in <root>/.mounts/<plugin>, found
// nothing, and dropped every mount in the real app while every test (which put the
// folders where the bridge looked) passed.
func TestSandboxPrepare_liveWithTheDesktopsFolderLayout(t *testing.T) {
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
	mountsRoot := filepath.Join(root, ".mounts", "b845953278ddb72912bb64688f4cc298") // <account>
	stateMounts := t.TempDir()                                                       // <member>/bridge/mounts
	name := "bridge-dl-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m, err := c.CreateMachine(ctx, admin, sandbox.CreateMachineRequest{
		Name: name, Image: "bnbloc.local/guest:0.1.0", MemoryMiB: 512,
		Shares: []sandbox.Share{{Name: "workspaces", HostPath: root, GuestPath: root}},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteMachine(context.Background(), admin, m.ID) })

	// What the desktop hands the plugin as its mount root, and what the plugin writes.
	pluginFolder := filepath.Join(mountsRoot, "agent-manager", "agents", "tool", "1.0")
	if err := os.MkdirAll(filepath.Join(pluginFolder, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginFolder, "bin", "tool"), []byte("#!/bin/sh\necho from-the-plugin\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(descriptor(desc{"host": pluginFolder, "guest": guestRoot + "/tool", "perSpace": true, "env": "BNBLOC_MOUNT_TOOL"}))
	if err := os.MkdirAll(filepath.Join(stateMounts, "agent-manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateMounts, "agent-manager", mountsFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := SandboxConfig{Mode: SandboxBorrowed, URL: base, Token: admin.Token, VMExec: "/x/bnbloc-vmexec", Machine: name, WorkspacesRoot: root, MountsDir: stateMounts, MountGuestRoots: []string{guestRoot}}
	run := func(cfg SandboxConfig) string {
		rt := newSandboxRuntime(cfg)
		if ready, reason := rt.status(ctx); !ready {
			t.Fatalf("sandbox not ready: %s", reason)
		}
		exec, err := rt.prepare(ctx, driveKey{room: "room-a", core: "core-a", provider: "codex", sandboxed: true}, "job-1", "alice", nil, sandboxExecTokenTTL, false, nil, nil)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		s := exec.Sandbox
		res, err := c.SpaceRun(ctx, corepkg.Endpoint{BaseURL: base, Token: s.ExecToken}, s.Machine, s.Space, sandbox.ExecRequest{Command: []string{"sh", "-c", `"$BNBLOC_MOUNT_TOOL/bin/tool" 2>&1`}, HostWorkdir: s.Workdir})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		out, _ := base64.StdEncoding.DecodeString(res.StdoutBase64)
		return strings.TrimSpace(string(out))
	}
	// Not told where the account's folders are: the mount is dropped (the real-app bug).
	if got := run(cfg); got == "from-the-plugin" {
		t.Errorf("a bridge that guesses the plain layout found the plugin's folder: %q", got)
	}
	// Told, as the desktop does: it runs.
	cfg.MountsRoot = mountsRoot
	if got := run(cfg); got != "from-the-plugin" {
		t.Errorf("with the folder the desktop names: %q, want the plugin's program to run in the room's sandbox", got)
	}
}
