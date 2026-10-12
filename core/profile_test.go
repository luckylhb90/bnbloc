package core

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestValidBundlePath(t *testing.T) {
	for _, p := range []string{"index.html", "a/b/c.js", "assets/app-1.2.css", "_x/.well"} {
		if !ValidBundlePath(p) {
			t.Fatalf("%q rejected", p)
		}
	}
	for _, p := range []string{"", "/index.html", "a//b", "a/./b", "../x", "a/..", "a\\b", "a b", "a/%2e%2e", "ü.html", strings.Repeat("a", 201)} {
		if ValidBundlePath(p) {
			t.Fatalf("%q accepted", p)
		}
	}
}

func TestWorkspaceFileAllowed(t *testing.T) {
	for _, p := range []string{
		".room/INDEX.md", ".room/attachments/a.png", "CLAUDE.md", "AGENTS.md",
		".claude/skills/review/SKILL.md", ".agents/skills/x/scripts/run.sh",
		".opencode/skills/x/SKILL.md", ".opencode/opencode.json", ".mcp.json",
	} {
		if !WorkspaceFileAllowed(p) {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range []string{
		"", "README.md", "src/main.go", "/CLAUDE.md", "../AGENTS.md", ".room/../x",
		"./AGENTS.md", "sub/AGENTS.md", ".claude/settings.json", ".claude/skills",
		".agents/other/x", "a\\b", ".room",
	} {
		if WorkspaceFileAllowed(p) {
			t.Errorf("%q allowed", p)
		}
	}
}

func TestWorkspacePathKinds(t *testing.T) {
	for _, p := range []string{"CLAUDE.md", "AGENTS.md", ".claude/skills", ".claude/skills/a/SKILL.md", ".agents/skills/x", ".opencode", ".opencode/x", ".mcp.json", ".room", ".room/INDEX.md", "./.room/x", ".ROOM/x", "Claude.md"} {
		if !BnblocOwnedPath(p) {
			t.Errorf("%q not bnbloc-owned", p)
		}
	}
	for _, p := range []string{"docs/CLAUDE.md", "AGENTS.md.bak", ".claude/settings.json", ".agents/other", "report.md", ".roomy/x"} {
		if BnblocOwnedPath(p) {
			t.Errorf("%q is bnbloc-owned", p)
		}
	}
	// The project config a harness executes on the host: settings (hooks,
	// env), agents, commands, hook scripts; also when spelled differently on
	// a case-insensitive filesystem.
	for _, p := range []string{".claude/settings.json", ".claude/settings.local.json", ".claude/agents/x.md", ".claude/commands/x.md", ".claude/hooks/h.sh", ".Claude/Settings.json", "CLAUDE.local.md", ".codex/config.toml", "opencode.json", "opencode.jsonc", `.claude\settings.json`} {
		if !HostExecutableProjectConfig(p) {
			t.Errorf("%q not host-executable project config", p)
		}
	}
	for _, p := range []string{"src/.claude/settings.json", "opencode.json.md", "docs/opencode.json", "codex/config.toml"} {
		if HostExecutableProjectConfig(p) {
			t.Errorf("%q is host-executable project config", p)
		}
	}
}

func TestBindingSumIgnoresCredentials(t *testing.T) {
	base := &AgentProfile{Instructions: "Be terse.", Skills: []ProfileSkill{{Name: "review", Files: []ProfileFile{
		{Path: "SKILL.md", Content: []byte("---\nname: review\n---\n")},
		{Path: "scripts/a.sh", Content: []byte("echo")},
	}}}}
	base.MCPServers = []MCPServer{{Name: "room", Type: MCPHTTP, URL: "http://127.0.0.1:1", Headers: []NameValue{{Name: "Authorization", Value: "Bearer one"}}}}
	withSecrets := *base
	withSecrets.MCPServers = []MCPServer{{Name: "room", Type: MCPHTTP, URL: "http://127.0.0.1:1", Headers: []NameValue{{Name: "Authorization", Value: "Bearer s3cret"}}}}
	withSecrets.Env = map[string]string{"TOKEN": "s3cret"}
	if base.BindingSum() != withSecrets.BindingSum() {
		t.Fatal("credentials changed the binding sum")
	}
	if strings.Contains(withSecrets.BindingSum(), "s3cret") {
		t.Fatal("a credential in the binding sum")
	}
	added := *base
	added.MCPServers = append(append([]MCPServer(nil), base.MCPServers...), MCPServer{Name: "lint", Command: "/t/lint"})
	if added.BindingSum() == base.BindingSum() {
		t.Fatal("adding an MCP server kept the binding sum")
	}
	reordered := &AgentProfile{Instructions: "Be terse.", Skills: []ProfileSkill{{Name: "review", Files: []ProfileFile{
		{Path: "scripts/a.sh", Content: []byte("echo")},
		{Path: "SKILL.md", Content: []byte("---\nname: review\n---\n")},
	}}}, MCPServers: base.MCPServers}
	if base.BindingSum() != reordered.BindingSum() {
		t.Fatal("file order changed the content sum")
	}
	changed := &AgentProfile{Instructions: "Be verbose.", Skills: base.Skills, MCPServers: base.MCPServers}
	if base.BindingSum() == changed.BindingSum() {
		t.Fatal("instructions change kept the content sum")
	}
	var nilProfile *AgentProfile
	if nilProfile.BindingSum() != "" || !nilProfile.Empty() {
		t.Fatal("nil profile")
	}
}

func TestProfileLogValueRedactsSecrets(t *testing.T) {
	p := &AgentProfile{
		Instructions: "secret-instructions-text",
		MCPServers:   []MCPServer{{Name: "room", Type: MCPHTTP, URL: "http://127.0.0.1:9", Headers: []NameValue{{Name: "Authorization", Value: "Bearer hunter2"}}}},
		Env:          map[string]string{"API_KEY": "hunter3"},
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "profile", p)
	out := buf.String()
	for _, secret := range []string{"hunter2", "hunter3", "secret-instructions-text"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "API_KEY") || !strings.Contains(out, "room(http)") {
		t.Fatalf("log lacks names: %s", out)
	}
}
