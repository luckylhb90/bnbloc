package manifestcore

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

// A manifest maps an agent profile through {instructions} and the raw-JSON
// "{var|json}" form, and advertises agentProfile only when its createSession
// body references a profile var.
func TestManifestMapsAgentProfile(t *testing.T) {
	var caps []capture
	srv := fakeRESTDaemon(t, &caps)
	defer srv.Close()
	manifest := strings.Replace(restManifest,
		`"prompt": "{prompt}" }`,
		`"prompt": "{prompt}", "system_append": "{instructions}", "profile": "{agentProfile|json}", "mcp": "{mcpServers|json}", "skills": "{skills|json}" }`, 1)
	p, err := Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()
	if !core.Supports(ctx, p, ep, core.CapabilityAgentProfile) {
		t.Fatal("profile-mapping manifest does not advertise agentProfile")
	}
	profile := &core.AgentProfile{
		Instructions: "Act as the reviewer.",
		Skills:       []core.ProfileSkill{{Name: "review", Files: []core.ProfileFile{{Path: "SKILL.md", Content: []byte("x")}}}},
		MCPServers:   []core.MCPServer{{Name: "room", Type: core.MCPHTTP, URL: "http://127.0.0.1:9/mcp"}},
	}
	if _, err := p.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: "proj-1", TargetID: "agent-a", Prompt: "go", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	body := caps[len(caps)-1].body
	if body["system_append"] != "Act as the reviewer." {
		t.Fatalf("instructions = %#v", body["system_append"])
	}
	prof, ok := body["profile"].(map[string]any)
	if !ok || prof["instructions"] != "Act as the reviewer." {
		t.Fatalf("profile = %#v", body["profile"])
	}
	mcp, ok := body["mcp"].([]any)
	if !ok || len(mcp) != 1 || mcp[0].(map[string]any)["url"] != "http://127.0.0.1:9/mcp" {
		t.Fatalf("mcp = %#v", body["mcp"])
	}
	if skills, ok := body["skills"].([]any); !ok || len(skills) != 1 {
		t.Fatalf("skills = %#v", body["skills"])
	}

	// Without a profile the vars are empty: a string var renders "", a json
	// var null.
	if _, err := p.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: "proj-1", TargetID: "agent-a", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	body = caps[len(caps)-1].body
	if body["system_append"] != "" || body["profile"] != nil || body["mcp"] != nil {
		t.Fatalf("empty profile body = %#v", body)
	}

	plain, err := Parse([]byte(restManifest))
	if err != nil {
		t.Fatal(err)
	}
	if core.Supports(ctx, plain, ep, core.CapabilityAgentProfile) {
		t.Fatal("manifest without profile vars advertises agentProfile")
	}
}

// A manifest declares its targets' MCP transports statically; a list
// element's own mcpTransports wins; an unknown transport fails the load.
func TestManifestDeclaresMCPTransports(t *testing.T) {
	var caps []capture
	srv := fakeRESTDaemon(t, &caps)
	defer srv.Close()
	manifest := strings.Replace(restManifest, `"provider": "harness" }`, `"provider": "harness", "mcpTransports": ["stdio", "http"] }`, 1)
	p, err := Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := p.ListTargets(context.Background(), core.Endpoint{BaseURL: srv.URL})
	if err != nil || len(ts) != 2 {
		t.Fatalf("targets = %+v, %v", ts, err)
	}
	for _, tg := range ts {
		if !tg.TransportSupported(core.MCPHTTP) || tg.TransportSupported(core.MCPSSE) {
			t.Fatalf("target %+v", tg)
		}
	}
	bad := strings.Replace(restManifest, `"provider": "harness" }`, `"provider": "harness", "mcpTransports": ["ws"] }`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("unknown transport accepted")
	}
}
