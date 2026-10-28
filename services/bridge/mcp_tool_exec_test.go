package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A tool's update keeps the session's profile, and so its binding checksum
// and the agent's conversation: the profile names the tool through the
// launcher, by name, not by its versioned binary (QA 2026-10-19: every
// browser release restarted the conversations that used it).
func TestToolUpdateKeepsTheSessionBinding(t *testing.T) {
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "pepper"})
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings, StateDir: t.TempDir()}}
	b.UseToolLauncher("/Applications/bnbloc.app/Contents/MacOS/bnbloc-bridge")
	before, _, _ := b.resolveMCP([]wire.ContentMCPTool{codewordTool(f.sha)}, nil, "maya", "home", true)

	// The desktop materializes the next version and drops the first.
	content := []byte("#!/bin/sh\necho codeword v2\n")
	sum := sha256.Sum256(content)
	sha2 := hex.EncodeToString(sum[:])
	bin2 := filepath.Join(f.tools, "mcp-codeword-"+sha2)
	if err := os.WriteFile(bin2, content, 0o755); err != nil {
		t.Fatal(err)
	}
	idx, _ := json.Marshal(mcpIndex{Tools: []mcpIndexEntry{{Name: "codeword", SHA256: sha2, Path: bin2}}})
	if err := os.WriteFile(filepath.Join(f.tools, mcpIndexFile), idx, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(f.binary)
	after, missing, _ := b.resolveMCP([]wire.ContentMCPTool{codewordTool(sha2)}, nil, "maya", "home", true)
	if len(before.servers) != 1 || len(after.servers) != 1 || len(missing) != 0 {
		t.Fatalf("before %+v after %+v missing %v", before.servers, after.servers, missing)
	}
	sum1 := (&corepkg.AgentProfile{MCPServers: before.servers}).BindingSum()
	sum2 := (&corepkg.AgentProfile{MCPServers: after.servers}).BindingSum()
	if sum1 != sum2 {
		t.Fatalf("a tool update changed the session binding:\n%+v\n%+v", before.servers[0], after.servers[0])
	}
	s := after.servers[0]
	if s.Command != b.toolLauncher || strings.Join(s.Args[:5], " ") != MCPToolCommand+" --dir "+f.tools+" --name codeword" || s.Args[5] != "--" {
		t.Fatalf("server = %+v", s)
	}
	// A turn still needs the gateway's current pin materialized.
	if _, missing, _ := b.resolveMCP([]wire.ContentMCPTool{codewordTool(f.sha)}, nil, "maya", "home", true); len(missing) != 1 {
		t.Fatalf("a pin the desktop no longer holds was attached: missing %v", missing)
	}
}

// The launcher runs the binary the desktop verified last, by name, with the
// tool's args; a tool it does not hold fails plainly.
func TestToolLauncherRunsTheMaterializedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture tool is a shell script")
	}
	if os.Getenv("BNBLOC_TEST_TOOL_LAUNCHER") == "1" {
		os.Exit(RunMCPToolMain(strings.Split(os.Getenv("BNBLOC_TEST_TOOL_ARGS"), "\x1f")))
	}
	f := newMCPFixture(t)
	if err := os.WriteFile(f.binary, []byte("#!/bin/sh\necho \"codeword $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestToolLauncherRunsTheMaterializedBinary$")
		cmd.Env = append(os.Environ(), "BNBLOC_TEST_TOOL_LAUNCHER=1", "BNBLOC_TEST_TOOL_ARGS="+strings.Join(args, "\x1f"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("--dir", f.tools, "--name", "codeword", "--", "--headless", "x")
	if err != nil || strings.TrimSpace(out) != "codeword --headless x" {
		t.Fatalf("launch = %q, %v", out, err)
	}
	out, err = run("--dir", f.tools, "--name", "nothing", "--")
	if err == nil || !strings.Contains(out, "not downloaded") {
		t.Fatalf("an unknown tool = %q, %v", out, err)
	}
}

// The desktop keeps each member's index in the member's directory and the
// binaries in one shared store (QA 2026-10-28: with the store unnamed, every
// stdio tool was refused as outside the tools directory and no agent had
// one). The store is named on its own; an index entry outside it is still
// refused, by the turn and by the launcher alike.
func TestToolsIndexAndStoreLiveApart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture tool is a shell script")
	}
	if os.Getenv("BNBLOC_TEST_TOOL_LAUNCHER") == "1" {
		os.Exit(RunMCPToolMain(strings.Split(os.Getenv("BNBLOC_TEST_TOOL_ARGS"), "\x1f")))
	}
	index, store := t.TempDir(), t.TempDir()
	content := []byte("#!/bin/sh\necho \"codeword $*\"\n")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	bin := filepath.Join(store, "mcp@codeword-"+sha)
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIndex := func(path string) {
		raw, _ := json.Marshal(mcpIndex{Tools: []mcpIndexEntry{{Name: "codeword", SHA256: sha, Path: path}}})
		if err := os.WriteFile(filepath.Join(index, mcpIndexFile), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeIndex(bin)
	tool := codewordTool(sha)
	tool.Settings, tool.RequiredBy = nil, []string{"cc_reviewer"}

	b := &Bridge{cfg: Config{MCPToolsDir: index, MCPToolsStore: store}}
	b.UseToolLauncher("/Applications/bnbloc.app/Contents/MacOS/bnbloc-bridge")
	got, missing, _ := b.resolveMCP([]wire.ContentMCPTool{tool}, nil, "maya", "home", true)
	if len(got.servers) != 1 || len(missing) != 0 {
		t.Fatalf("servers %+v missing %v", got.servers, missing)
	}
	if args := strings.Join(got.servers[0].Args, " "); !strings.HasPrefix(args, MCPToolCommand+" --dir "+index+" --store "+store+" --name codeword --") {
		t.Fatalf("launcher args = %q", args)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestToolsIndexAndStoreLiveApart$")
		cmd.Env = append(os.Environ(), "BNBLOC_TEST_TOOL_LAUNCHER=1", "BNBLOC_TEST_TOOL_ARGS="+strings.Join(args, "\x1f"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(got.servers[0].Args[1:]...); err != nil || strings.TrimSpace(out) != "codeword serve" {
		t.Fatalf("launch = %q, %v", out, err)
	}

	// Without the store named, the index's directory is the store: the
	// binary elsewhere is refused (the old desktop layout keeps working).
	alone := &Bridge{cfg: Config{MCPToolsDir: index}}
	if got, missing, _ := alone.resolveMCP([]wire.ContentMCPTool{tool}, nil, "maya", "home", true); len(got.servers) != 0 || len(missing) != 1 || !strings.Contains(missing[0], "outside the tools store") {
		t.Fatalf("a binary outside the index dir with no store: servers %+v missing %v", got.servers, missing)
	}

	// An entry pointing outside the store is refused by both.
	other := filepath.Join(t.TempDir(), "mcp@codeword-"+sha)
	if err := os.WriteFile(other, content, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIndex(other)
	if got, missing, _ := b.resolveMCP([]wire.ContentMCPTool{tool}, nil, "maya", "home", true); len(got.servers) != 0 || len(missing) != 1 {
		t.Fatalf("an entry outside the store: servers %+v missing %v", got.servers, missing)
	}
	if out, err := run("--dir", index, "--store", store, "--name", "codeword", "--"); err == nil || !strings.Contains(out, "outside the tools store") {
		t.Fatalf("launcher with an entry outside the store = %q, %v", out, err)
	}
}
