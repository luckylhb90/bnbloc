package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

func sampleProfile() *core.AgentProfile {
	return &core.AgentProfile{
		Instructions: "You are the reviewer. MARKER-7.",
		Skills: []core.ProfileSkill{{Name: "review", Files: []core.ProfileFile{
			{Path: "SKILL.md", Content: []byte("---\nname: review\ndescription: Review diffs\n---\nbody")},
			{Path: "assets/logo.bin", Content: []byte{0xff, 0x00, 0xfe}},
		}}},
		MCPServers: []core.MCPServer{
			{Name: "room", Type: core.MCPHTTP, URL: "http://127.0.0.1:4100/mcp", Headers: []core.NameValue{{Name: "Authorization", Value: "Bearer t"}}},
			{Name: "fs", Type: core.MCPStdio, Command: "mcp-fs", Args: []string{"--root", "."}, Env: []core.NameValue{{Name: "A", Value: "1"}}},
		},
		Env: map[string]string{"TOOL_TOKEN": "x"},
	}
}

func TestProfile_roundTripsThroughTheWire(t *testing.T) {
	wire := WireCreateSession(core.SessionRequest{TargetID: "t", Prompt: "p", Profile: sampleProfile()})
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Wire shape: explicit camelCase, stdio has no type, binary as base64.
	for _, want := range []string{`"profile":`, `"instructions":`, `"skills":`, `"mcpServers":`, `"contentBase64":`, `"type":"http"`, `"headers":[{"name":"Authorization","value":"Bearer t"}]`, `"env":{"TOOL_TOKEN":"x"}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"type":"stdio"`) {
		t.Fatalf("stdio carries a type: %s", raw)
	}
	var back CreateSessionRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	req, err := back.Core("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if req.Profile.ContentSum() != sampleProfile().ContentSum() {
		t.Fatal("content changed through the wire")
	}
	if got := req.Profile.MCPServers; len(got) != 2 || got[1].Type != core.MCPStdio || got[0].Headers[0].Value != "Bearer t" {
		t.Fatalf("mcp servers = %+v", got)
	}
	if req.Profile.Env["TOOL_TOKEN"] != "x" {
		t.Fatalf("env = %+v", req.Profile.Env)
	}
	// No profile stays no profile.
	plain, err := CreateSessionRequest{Prompt: "p"}.Core("ws")
	if err != nil || plain.Profile != nil {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
}

func TestProfile_refusesUnsafeShapes(t *testing.T) {
	for name, p := range map[string]AgentProfile{
		"escaping path":      {Skills: []ProfileSkill{{Name: "a", Files: []ProfileFile{{Path: "../x"}}}}},
		"absolute path":      {Skills: []ProfileSkill{{Name: "a", Files: []ProfileFile{{Path: "/etc/x"}}}}},
		"nested skill name":  {Skills: []ProfileSkill{{Name: "a/b"}}},
		"hidden skill name":  {Skills: []ProfileSkill{{Name: ".x"}}},
		"duplicate skill":    {Skills: []ProfileSkill{{Name: "a"}, {Name: "a"}}},
		"duplicate file":     {Skills: []ProfileSkill{{Name: "a", Files: []ProfileFile{{Path: "x"}, {Path: "x"}}}}},
		"bad base64":         {Skills: []ProfileSkill{{Name: "a", Files: []ProfileFile{{Path: "x", ContentBase64: "!!"}}}}},
		"unnamed server":     {McpServers: []McpServer{{Command: "x"}}},
		"stdio no command":   {McpServers: []McpServer{{Name: "x"}}},
		"http no url":        {McpServers: []McpServer{{Name: "x", Type: "http"}}},
		"unknown type":       {McpServers: []McpServer{{Name: "x", Type: "ws", URL: "ws://x"}}},
		"duplicate server":   {McpServers: []McpServer{{Name: "x", Command: "a"}, {Name: "x", Command: "b"}}},
		"invalid env name":   {Env: map[string]string{"A-B": "1"}},
		"leading digit name": {Env: map[string]string{"1A": "1"}},
	} {
		p := p
		if _, err := (CreateSessionRequest{Prompt: "p", Profile: &p}).Core("ws"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClient_Profile_gatedByDiscovery_andCarriedOnTheWire(t *testing.T) {
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w, CapabilityAgentProfile)
		case "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(CreateSessionResponse{SessionID: "s-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := NewClient("proto", srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()
	if !core.Supports(ctx, client, ep, core.CapabilityAgentProfile) {
		t.Fatal("agentProfile advertised but not supported")
	}
	if _, err := client.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: "ws-1", Prompt: "go", Profile: sampleProfile()}); err != nil {
		t.Fatal(err)
	}
	got := *calls
	prof, _ := got[len(got)-1].body["profile"].(map[string]any)
	if prof["instructions"] != "You are the reviewer. MARKER-7." {
		t.Fatalf("session body = %+v", got[len(got)-1].body)
	}

	// A core without the capability refuses a profile instead of dropping it.
	plain, _ := protocolServer(t, func(w http.ResponseWriter, r *http.Request) { writeDiscover(w) })
	pc := NewClient("p", plain.Client())
	pep := core.Endpoint{BaseURL: plain.URL}
	if core.Supports(ctx, pc, pep, core.CapabilityAgentProfile) {
		t.Fatal("agentProfile supported without being advertised")
	}
	_, err := pc.CreateSession(ctx, pep, core.SessionRequest{WorkspaceID: "ws", Prompt: "go", Profile: sampleProfile()})
	var pe *ProtocolError
	if err == nil || !errors.As(err, &pe) || pe.Code != ErrorCapabilityMissing {
		t.Fatalf("err = %v", err)
	}
}
