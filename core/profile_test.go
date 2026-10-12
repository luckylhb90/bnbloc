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

func TestIsAgentConfigPath(t *testing.T) {
	for _, p := range []string{"CLAUDE.md", "AGENTS.md", ".claude/skills", ".claude/skills/a/SKILL.md", ".agents/skills/x", ".opencode", ".opencode/x", ".mcp.json"} {
		if !IsAgentConfigPath(p) {
			t.Errorf("%q not a config path", p)
		}
	}
	for _, p := range []string{"docs/CLAUDE.md", "AGENTS.md.bak", ".claude/settings.json", ".agents/other", "report.md"} {
		if IsAgentConfigPath(p) {
			t.Errorf("%q is a config path", p)
		}
	}
}

func TestContentSumIgnoresCredentials(t *testing.T) {
	base := &AgentProfile{Instructions: "Be terse.", Skills: []ProfileSkill{{Name: "review", Files: []ProfileFile{
		{Path: "SKILL.md", Content: []byte("---\nname: review\n---\n")},
		{Path: "scripts/a.sh", Content: []byte("echo")},
	}}}}
	withSecrets := *base
	withSecrets.MCPServers = []MCPServer{{Name: "room", Type: MCPHTTP, URL: "http://127.0.0.1:1", Headers: []NameValue{{Name: "Authorization", Value: "Bearer s3cret"}}}}
	withSecrets.Env = map[string]string{"TOKEN": "s3cret"}
	if base.ContentSum() != withSecrets.ContentSum() {
		t.Fatal("credentials changed the content sum")
	}
	reordered := &AgentProfile{Instructions: "Be terse.", Skills: []ProfileSkill{{Name: "review", Files: []ProfileFile{
		{Path: "scripts/a.sh", Content: []byte("echo")},
		{Path: "SKILL.md", Content: []byte("---\nname: review\n---\n")},
	}}}}
	if base.ContentSum() != reordered.ContentSum() {
		t.Fatal("file order changed the content sum")
	}
	changed := &AgentProfile{Instructions: "Be verbose.", Skills: base.Skills}
	if base.ContentSum() == changed.ContentSum() {
		t.Fatal("instructions change kept the content sum")
	}
	var nilProfile *AgentProfile
	if nilProfile.ContentSum() != "" || !nilProfile.Empty() {
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
