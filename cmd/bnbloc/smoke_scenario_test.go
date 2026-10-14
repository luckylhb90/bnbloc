package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeScenarioAgent is the fake gateway's agent for the long-task scenario:
// it reads attachments, remembers the codeword, keeps a workspace file, and
// can lose either across --between.
type fakeScenarioAgent struct {
	files      map[string]string // attachment id -> content
	codeword   string            // remembered from the brief
	plan       string            // notes/plan.md
	forget     bool              // --between drops the conversation
	wipe       bool              // --between drops the workspace
	noAttach   bool              // attachments never reach the agent
	progress   bool              // publish agent_job_progress before replying
	noTools    bool              // the agent never has room tools
	loseTools  bool              // --between drops its room tools
	betweenRan int
}

var (
	codewordRE = regexp.MustCompile(`CW-[0-9a-f]{8}`)
	markerRE   = regexp.MustCompile(`STEP1-[0-9a-f]{8}`)
)

// answerScenario replies to one scenario turn like an agent would.
func (g *fakeSmokeGateway) answerScenario(room, text string, attachmentIDs []string) {
	g.mu.Lock()
	a := &g.fakeAgent
	reply := "I cannot tell."
	switch {
	case strings.HasPrefix(text, "Read the attached brief.md"):
		for _, id := range attachmentIDs {
			if cw := codewordRE.FindString(a.files[id]); cw != "" && !a.noAttach {
				a.codeword, reply = cw, cw
			}
		}
		if a.noAttach {
			reply = "There is no attachment."
		}
	case strings.HasPrefix(text, "Create the file notes/plan.md"):
		a.plan = markerRE.FindString(text)
		reply = "Narration: writing the file.\x00DONE" // narration, then the answer
	case strings.HasPrefix(text, "Call your room_status tool"):
		reply = "ROOMTOOLS-OK"
		if a.noTools {
			reply = "ROOMTOOLS-NONE"
		}
	case strings.HasPrefix(text, "Without re-reading"):
		reply = strings.TrimSpace(a.codeword + " " + a.plan)
		if reply == "" {
			reply = "I don't know."
		}
	}
	progress := a.progress
	g.mu.Unlock()
	if progress {
		raw, _ := json.Marshal(wire.AgentJobProgress{JobID: "job-1", Activity: []wire.JobActivity{{Kind: "tool", Title: "Read: brief.md", AtMs: 1}}})
		g.mu.Lock()
		g.seq++
		ev := smokeEvent{Sequence: g.seq, Kind: wire.EventAgentJobProgress, Payload: raw}
		g.mu.Unlock()
		g.publish(room, ev, false)
	}
	g.publishAgent(room, reply)
}

// publishAgent publishes each NUL-separated text as its own agent message,
// in order (narration first, the answer last).
func (g *fakeSmokeGateway) publishAgent(room, text string) {
	var evs []smokeEvent
	for _, part := range strings.Split(text, "\x00") {
		raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"payload": map[string]any{
			"role": "assistant", "text": part, "ownerUserId": "alice", "provider": "claude",
		}}}})
		g.mu.Lock()
		g.seq++
		evs = append(evs, smokeEvent{Sequence: g.seq, Kind: wire.KindAgentMessages, Payload: raw})
		g.mu.Unlock()
	}
	go func() {
		for _, ev := range evs {
			time.Sleep(10 * time.Millisecond)
			g.publish(room, ev, true)
		}
	}()
}

func scenarioOptions(g *fakeSmokeGateway, between string) smokeOptions {
	opts := smokeTestOptions()
	opts.Agent = "acme/claude"
	opts.Scenario = scenarioLongTask
	opts.Between = between
	opts.PollEvery = 10 * time.Millisecond
	opts.Settle = 80 * time.Millisecond
	opts.RunBetween = func(context.Context, string) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		a := &g.fakeAgent
		a.betweenRan++
		if a.forget {
			a.codeword = ""
		}
		if a.wipe {
			a.plan = ""
		}
		if a.loseTools {
			a.noTools = true
		}
		return nil
	}
	return opts
}

func newScenarioGateway(t *testing.T) *fakeSmokeGateway {
	g := newFakeSmokeGateway(t)
	g.scenario = true
	g.agents = []myAgent{{Core: "acme", Provider: "claude", TargetID: "acme/local:claude", Online: true}}
	return g
}

func TestSmokeLongTaskPasses(t *testing.T) {
	g := newScenarioGateway(t)
	g.fakeAgent.progress = true
	out, err := runFakeSmoke(t, g, scenarioOptions(g, "publish core"))
	if err != nil {
		t.Fatalf("scenario failed: %v\n%s", err, out)
	}
	for _, want := range []string{"PASS agent (", "PASS attachment (", "PASS workspace write (", `PASS between (`, "PASS memory (", "PASS workspace file (", "PASS room tools (", "INFO progress events seen: 5", "PASS delete room ("} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if g.fakeAgent.betweenRan != 1 || len(g.deleted) != 1 {
		t.Fatalf("between ran %d times, deleted %v", g.fakeAgent.betweenRan, g.deleted)
	}
}

// Each property fails on its own line, and a failed scenario keeps its room.
func TestSmokeLongTaskNamesWhatBroke(t *testing.T) {
	for _, tc := range []struct {
		name     string
		set      func(*fakeScenarioAgent)
		fail     string
		pass     string
		noReturn bool
	}{
		{"conversation lost", func(a *fakeScenarioAgent) { a.forget = true }, "FAIL memory: the reply does not hold the codeword", "PASS workspace file (", false},
		{"files lost", func(a *fakeScenarioAgent) { a.wipe = true }, "FAIL workspace file: the reply does not hold notes/plan.md's", "PASS memory (", false},
		{"attachment lost", func(a *fakeScenarioAgent) { a.noAttach = true }, "FAIL attachment: the reply does not hold CW-", "", true},
		{"room tools lost", func(a *fakeScenarioAgent) { a.loseTools = true }, "FAIL room tools: the agent no longer reaches its room tools", "PASS memory (", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newScenarioGateway(t)
			tc.set(&g.fakeAgent)
			opts := scenarioOptions(g, "restart")
			opts.AgentTimeout = 300 * time.Millisecond
			out, err := runFakeSmoke(t, g, opts)
			if !errors.Is(err, errSmokeFailed) {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.fail) || (tc.pass != "" && !strings.Contains(out, tc.pass)) {
				t.Fatalf("want %q and %q:\n%s", tc.fail, tc.pass, out)
			}
			if tc.noReturn && strings.Contains(out, "memory") {
				t.Fatalf("the scenario went on after the attachment failed:\n%s", out)
			}
			if len(g.deleted) != 0 || !strings.Contains(out, "SKIP delete room (kept: the scenario failed)") {
				t.Fatalf("a failed scenario's room was not kept: deleted=%v\n%s", g.deleted, out)
			}
		})
	}
}

// After --between the scenario waits until the agent is online again.
func TestSmokeLongTaskWaitsForTheAgent(t *testing.T) {
	g := newScenarioGateway(t)
	opts := scenarioOptions(g, "restart")
	inner := opts.RunBetween
	opts.RunBetween = func(ctx context.Context, cmd string) error {
		g.mu.Lock()
		g.agents[0].Online = false
		g.mu.Unlock()
		go func() {
			time.Sleep(50 * time.Millisecond)
			g.mu.Lock()
			g.agents[0].Online = true
			g.mu.Unlock()
		}()
		return inner(ctx, cmd)
	}
	out, err := runFakeSmoke(t, g, opts)
	if err != nil || !strings.Contains(out, "PASS between (") {
		t.Fatalf("err = %v\n%s", err, out)
	}

	g2 := newScenarioGateway(t)
	opts2 := scenarioOptions(g2, "restart")
	opts2.AgentTimeout = 100 * time.Millisecond
	opts2.RunBetween = func(context.Context, string) error {
		g2.mu.Lock()
		g2.agents[0].Online = false
		g2.mu.Unlock()
		return nil
	}
	out, err = runFakeSmoke(t, g2, opts2)
	if !errors.Is(err, errSmokeFailed) || !strings.Contains(out, "FAIL between: the agent was not online again") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// The brief is uploaded as a Markdown attachment and sent with the first turn.
func TestSmokeLongTaskUploadsTheBrief(t *testing.T) {
	g := newScenarioGateway(t)
	out, err := runFakeSmoke(t, g, scenarioOptions(g, ""))
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(g.fakeAgent.files) != 1 || !codewordRE.MatchString(g.fakeAgent.files["att_1"]) {
		t.Fatalf("uploaded = %v", g.fakeAgent.files)
	}
	if strings.Contains(out, "between") {
		t.Fatalf("no --between, yet:\n%s", out)
	}
}

// An agent without room tools before the change is not failed for lacking
// them after it.
func TestSmokeLongTaskSkipsRoomToolsForAnAgentWithout(t *testing.T) {
	g := newScenarioGateway(t)
	g.fakeAgent.noTools = true
	out, err := runFakeSmoke(t, g, scenarioOptions(g, "restart"))
	if err != nil || !strings.Contains(out, "INFO room tools: the agent had none before the change (not checked)") || strings.Contains(out, "PASS room tools") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}
