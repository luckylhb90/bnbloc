package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/sandbox/conformance"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// TestIntegration drives the real binary against a running Sandbox Protocol
// implementation:
//
//	SANDBOX_URL=http://127.0.0.1:7311 SANDBOX_TOKEN=… go test ./cmd/bnbloc-vmexec -run TestIntegration -v
//
// It creates its own machine (a share of a temp workspaces root), a space
// mounting one room workspace at the identical guest path, mints a
// space-scoped exec token, and runs `bnbloc-vmexec` with only that token.
func TestIntegration(t *testing.T) {
	base := os.Getenv("SANDBOX_URL")
	if base == "" {
		t.Skip("SANDBOX_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin := core.Endpoint{BaseURL: base, Token: os.Getenv("SANDBOX_TOKEN")}
	c := sandbox.NewClient(nil)
	c.REST.Timeout = 4 * time.Minute
	d, err := c.Discover(ctx, admin)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, cap := range []string{sandbox.CapabilitySpaces, sandbox.CapabilityShares, sandbox.CapabilityExecTokens} {
		if !sandbox.HasCapability(d, cap) {
			t.Skipf("implementation lacks %q", cap)
		}
	}
	image := os.Getenv("SANDBOX_IMAGE")
	if image == "" {
		image = conformance.DefaultImage
	}

	root := canonical(t.TempDir())
	ws := filepath.Join(root, "room-it")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	name := "vmexec-it-" + hex.EncodeToString(rnd[:])
	m, err := c.CreateMachine(ctx, admin, sandbox.CreateMachineRequest{
		Name: name, Image: image, MemoryMiB: 512,
		Shares: []sandbox.Share{{Name: "workspaces", HostPath: root, GuestPath: root}},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteMachine(context.Background(), admin, m.ID) })
	const space = "room-it"
	if _, err := c.EnsureSpace(ctx, admin, m.ID, space, sandbox.SpaceRequest{
		Mounts:       []sandbox.SpaceMount{{Kind: sandbox.MountShared, HostPath: ws, GuestPath: ws}},
		EnvAllowlist: coreprotocol.VMExecForwardEnv,
	}); err != nil {
		t.Fatalf("ensure space: %v", err)
	}
	tok, err := c.MintExecToken(ctx, admin, m.ID, space, 600)
	if err != nil {
		t.Fatalf("mint exec token: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bnbloc-vmexec")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := append(os.Environ(), coreprotocol.VMExecEnv(coreprotocol.SandboxExecution{
		URL: base, ExecToken: tok.Token, Machine: name, Space: space, VMExec: bin, Workdir: ws,
		Mounts: []coreprotocol.PathMount{{Host: ws, Guest: ws}}, Passthrough: []string{"hostname"},
	})...)
	vmexec := func(stdin string, args ...string) (stdout, stderr string, code int) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = filepath.Join(ws, "sub")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		return o.String(), e.String(), code
	}

	// A shell string runs in the guest at the identical path, exit code exact.
	out, errOut, code := vmexec("", "run", "--", `uname -s; pwd; echo from-guest > made.txt; echo oops >&2; exit 3`)
	if code != 3 || out != "Linux\n"+filepath.Join(ws, "sub")+"\n" || errOut != "oops\n" {
		t.Fatalf("run: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if raw, err := os.ReadFile(filepath.Join(ws, "sub", "made.txt")); err != nil || string(raw) != "from-guest\n" {
		t.Fatalf("host does not see the guest write: %q %v", raw, err)
	}
	// Piped stdin, binary-faithful, long-lived argv form.
	payload := "line-1\n\x00\x01binary\nline-3\n"
	if out, _, code := vmexec(payload, "run", "--stdio", "--", "cat"); code != 0 || out != payload {
		t.Fatalf("stdio cat: code=%d stdout=%q", code, out)
	}
	// Passthrough runs on the host.
	host, _ := os.Hostname()
	if out, _, code := vmexec("", "run", "--", "hostname"); code != 0 || !strings.Contains(out, strings.Split(host, ".")[0]) {
		t.Fatalf("passthrough hostname = %q (host %q) code %d", out, host, code)
	}
	// The Claude prefix router: a Bash-tool command runs in the guest and the
	// final cwd lands in Claude's host cwd file.
	cwdFile := filepath.Join(t.TempDir(), "claude-ab12-cwd")
	claude := `{ shopt -u extglob || setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; } >/dev/null 2>&1 || true && eval 'mkdir -p deeper && cd deeper && uname -s' < /dev/null && pwd -P >| ` + cwdFile
	if out, errOut, code := vmexec("", "--claude-prefix", claude); code != 0 || out != "Linux\n" {
		t.Fatalf("claude-prefix: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if raw, err := os.ReadFile(cwdFile); err != nil || strings.TrimSpace(string(raw)) != filepath.Join(ws, "sub", "deeper") {
		t.Fatalf("claude cwd file = %q %v", raw, err)
	}
	// The exec token cannot reach anything but its space.
	if _, err := c.Run(ctx, core.Endpoint{BaseURL: base, Token: tok.Token}, m.ID, sandbox.ExecRequest{Command: []string{"true"}}); sandbox.ErrorCodeOf(err) != sandbox.ErrorForbidden {
		t.Fatalf("machine-level run with the exec token: %v, want forbidden", err)
	}
	// A cwd outside the mounts is refused before anything runs.
	cmd := exec.CommandContext(ctx, bin, "run", "--", "true")
	cmd.Dir = root
	cmd.Env = env
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != exitRefused {
		t.Fatalf("cwd outside the mounts: %v", err)
	}
}
