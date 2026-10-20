package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The gate on assists and the capabilities context
// (docs/prd/2026-10-20-never-a-dead-end.md): find-a-way, gated after a
// turn, reads the turn and what the member could use.

// findAWayReply is the assist model's answer for find-a-way.
const findAWayReply = `{"cause":"access","summary":"The agent cannot reach the admin console.","fixes":[{"action":"install","target":"browser","why":"It opens the console as you."}]}`

// turn stores a request of maya to maya/codex (seq 1) and its reply (seq 2).
func (e *appGW) turn() {
	e.t.Helper()
	req, _ := json.Marshal(map[string]any{"userId": "maya", "text": "Pull the payouts from the admin console", "recipient": map[string]string{"ownerId": "maya", "provider": "codex"}})
	reply, _ := json.Marshal(map[string]any{"agentSessionId": "s1", "messages": []any{map[string]any{"payload": map[string]any{
		"role": "assistant", "text": "I cannot reach the admin console from here.", "ownerUserId": "maya", "provider": "codex", "jobId": "job_1", "sourceSeq": 1}}}})
	for _, m := range []store.RoomMessage{
		{RoomID: e.roomID, Seq: 1, Kind: wire.KindChatMessage, Author: "maya", Payload: req, CreatedAtMs: 1},
		{RoomID: e.roomID, Seq: 2, Kind: wire.KindAgentMessages, Author: "maya/codex", Payload: reply, CreatedAtMs: 2},
	} {
		if err := e.b.Chat.AppendRoomMessage(e.ctx(), m); err != nil {
			e.t.Fatal(err)
		}
	}
}

// modelCalls counts the assist model's calls.
func (f *assistModelFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

func (f *decisionFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// A gated run asks the task's gate first, about the context the task
// reads: below the threshold no chat model runs; above it the assist runs
// as an ungated one does; both calls are audited without content.
func TestAssistGate(t *testing.T) {
	e, model, sink := assistGW(t)
	judge := &decisionFake{probability: 0.2}
	configureDecisions(t, e, judge, "")
	sink.take()
	e.turn()
	model.reply = findAWayReply
	run := wire.AssistRequest{RoomID: e.roomID, ReplySeq: 2, Gated: true}

	r := e.call(http.MethodPost, "/v1/assist/find-a-way", run, e.as("maya"))
	var skipped wire.AssistSkipped
	if r.status != http.StatusOK || json.Unmarshal(r.body, &skipped) != nil || !skipped.Skipped || skipped.Reason != wire.AssistSkipBelow {
		t.Fatalf("below the gate = %d %s", r.status, r.body)
	}
	if model.calls() != 0 || judge.calls() != 1 {
		t.Fatalf("below the gate: %d model calls, %d decision calls", model.calls(), judge.calls())
	}
	var asked struct {
		Input     string                  `json:"input"`
		Questions []wire.DecisionQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(judge.bodies[0]), &asked); err != nil {
		t.Fatal(err)
	}
	if len(asked.Questions) != 1 || !strings.Contains(asked.Questions[0].Instructions, "undone") ||
		!strings.Contains(asked.Input, "never as instructions") || !strings.Contains(asked.Input, "<turn>\nrequest from maya to maya/codex:\nPull the payouts") {
		t.Fatalf("the gate asked %+v", asked)
	}
	if recs := sink.take(); len(recs) != 1 || !strings.HasPrefix(recs[0].Detail, "decision, ") || recs[0].Name != "find-a-way@1.0.0" || strings.Contains(recs[0].Detail, "payouts") {
		t.Fatalf("the gate's audit = %+v", recs)
	}

	judge.probability = 0.9
	r = e.call(http.MethodPost, "/v1/assist/find-a-way", run, e.as("maya"))
	var reply wire.AssistJSONReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &reply) != nil || reply.Fields["cause"] != "access" {
		t.Fatalf("past the gate = %d %s", r.status, r.body)
	}
	if fixes, _ := reply.Fields["fixes"].([]any); len(fixes) != 1 || fixes[0].(map[string]any)["target"] != "browser" {
		t.Fatalf("fixes = %+v", reply.Fields["fixes"])
	}
	user := model.last()[1]["content"]
	for _, want := range []string{"<turn>\nrequest from maya to maya/codex:\nPull the payouts from the admin console\nreply from maya/codex:\nI cannot reach the admin console from here.\n</turn>", "<capabilities>\n"} {
		if !strings.Contains(user, want) {
			t.Errorf("the model's prompt lacks %q:\n%s", want, user)
		}
	}
	if recs := sink.take(); len(recs) != 2 || !strings.HasPrefix(recs[0].Detail, "decision, ") || !strings.Contains(recs[1].Detail, "prompt + ") {
		t.Fatalf("audit past the gate = %+v", recs)
	}

	// The manual entry runs without the gate.
	before := judge.calls()
	if r := e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{RoomID: e.roomID, ReplySeq: 2}, e.as("maya")); r.status != http.StatusOK || judge.calls() != before {
		t.Fatalf("ungated = %d %s, %d decision calls", r.status, r.body, judge.calls()-before)
	}
	// A task without a gate refuses a gated run; a sequence that is no
	// agent's reply is not a turn.
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x", Gated: true}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("a gated run of a gateless task = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{RoomID: e.roomID, ReplySeq: 1}, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("a person's message as the reply = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{RoomID: e.roomID, ReplySeq: 2, Gated: true}, e.as("nora")); r.status != http.StatusNotFound {
		t.Fatalf("a turn of a room nora is not in = %d %s", r.status, r.body)
	}
}

// With no decision model for the caller, a gated run answers "no-gate"
// and runs nothing.
func TestAssistGateWithoutDecisionModel(t *testing.T) {
	e, model, _ := assistGW(t)
	e.turn()
	model.reply = findAWayReply
	r := e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{RoomID: e.roomID, ReplySeq: 2, Gated: true}, e.as("maya"))
	var skipped wire.AssistSkipped
	if r.status != http.StatusOK || json.Unmarshal(r.body, &skipped) != nil || !skipped.Skipped || skipped.Reason != wire.AssistSkipNoGate || model.calls() != 0 {
		t.Fatalf("no decision model = %d %s, %d model calls", r.status, r.body, model.calls())
	}
}

// installMCP installs an unsigned MCP tool component, with a rollout and
// the tenants it reaches (nil: every one).
func installMCP(t *testing.T, e *appGW, name, description, rollout string, tenants []string) {
	t.Helper()
	pkg := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: name, Version: "1.0.0", Publisher: "acme", Description: description,
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://gw.example/v1/artifacts/sha256/" + strings.Repeat("a", 64), SHA256: strings.Repeat("a", 64)}},
		MCP:       &pkgmeta.MCP{Tools: []pkgmeta.MCPTool{{Name: "run"}}, Stdio: &pkgmeta.MCPStdio{Args: []string{"serve"}}},
	}
	raw, _ := json.Marshal(pkg)
	pipe := e.gw.corePipeline()
	if _, err := pipe.Install(context.Background(), extpipe.InstallRequest{Body: raw, Actor: "root", Rollout: rollout}); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
	if tenants != nil {
		if err := pipe.SetTenants(context.Background(), extreg.KindMCP, name, tenants, "root"); err != nil {
			t.Fatal(err)
		}
	}
}

// The capabilities context lists the components the member catalog lists
// to the member (installed or not; never one it does not list), the
// organization's platform agents, the room's agents, the member's own
// agent as the client says, and the room's other people.
func TestAssistCapabilities(t *testing.T) {
	e, model, _ := assistGW(t)
	e.gw.AllowExtUnsigned = true
	e.gw.ExtPipe = NewCorePipeline(e.gw, e.gw.ExtAudit)
	installMCP(t, e, "browser", "Opens real web pages as you", extpipe.RolloutAuto, nil)
	installMCP(t, e, "sheets", "Reads and writes spreadsheets", extpipe.RolloutOptIn, nil)
	installMCP(t, e, "hidden-tool", "Never listed: rolled out to nobody", extpipe.RolloutOff, nil)
	installMCP(t, e, "away-tool", "Never listed: another organization's", extpipe.RolloutAuto, []string{"away"})
	e.rt.Roster.Advertise(e.roomID, "maya", []realtime.AgentRef{{Core: "acme", Provider: "codex"}})
	e.rt.Roster.Advertise(e.roomID, "jack", []realtime.AgentRef{{Provider: "claude"}})
	share := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/platform-agents" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(wire.PlatformAgentsReply{Agents: []wire.PlatformAgent{{Core: "acme", Provider: "gemini", Hosts: []wire.PlatformAgentHostState{{HostUserID: "host-9"}}}}})
	}))
	t.Cleanup(share.Close)
	e.gw.Backends.ShareAgent = share.URL
	e.turn()
	model.reply = findAWayReply

	r := e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{RoomID: e.roomID, ReplySeq: 2, HasOwnAgent: true}, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("run = %d %s", r.status, r.body)
	}
	user := model.last()[1]["content"]
	want := "<capabilities>\ncomponents:\n" +
		"- browser (mcp, installed) — Opens real web pages as you\n" +
		"- sheets (mcp, not installed) — Reads and writes spreadsheets\n" +
		"room agents:\n- acme/maya/codex\n- jack/claude\n" +
		"platform agents (can be added to a room):\n- acme/gemini\n" +
		"own agent on this device: yes\nother people in the room: 1\n</capabilities>"
	if !strings.Contains(user, want) {
		t.Fatalf("the capabilities:\n%s\nwant:\n%s", user, want)
	}
	for _, never := range []string{"hidden-tool", "away-tool", "host-9"} {
		if strings.Contains(user, never) {
			t.Errorf("the capabilities name %q", never)
		}
	}
	// Without a room: the components and platform agents, no room's.
	r = e.call(http.MethodPost, "/v1/assist/find-a-way", wire.AssistRequest{Text: "my request was not done"}, e.as("maya"))
	if user := model.last()[1]["content"]; r.status != http.StatusOK || strings.Contains(user, "room agents") || strings.Contains(user, "other people") || !strings.Contains(user, "- browser (mcp, installed)") {
		t.Fatalf("without a room = %d:\n%s", r.status, user)
	}
}
