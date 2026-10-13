package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// failingTurnProv scripts one turn the core reports failed: busy on the
// first status poll (with a genuine progress message completed), then idle
// and failed, with the agent's error output completed together with the
// failure as the turn's last assistant message — what an agent CLI that
// cannot start leaves behind (docs/bugs/2026-10-13-demo-app-build-issues.md).
type failingTurnProv struct {
	recordingProv
	mu       sync.Mutex
	polls    int
	progress bool // the turn says something before it fails
}

const agentErrorText = "Warning: Model metadata not found. Missing environment variable: `EXAMPLE_API_KEY`"

func (p *failingTurnProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	if p.polls <= 2 {
		return corepkg.TurnStatus{Idle: false}, nil
	}
	return corepkg.TurnStatus{Idle: true, Failed: true, FailMessage: "the agent returned an error: " + agentErrorText}, nil
}

func (p *failingTurnProv) Messages(_ context.Context, _ corepkg.Endpoint, _, _ string, after int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var all []corepkg.Message
	if p.progress {
		all = append(all, corepkg.Message{Version: 1, Seq: 1, Role: "assistant", Text: "Setting up the project.", UserVisible: true, Completed: true, MessageID: "m1", CreatedAtMs: 1})
	}
	if p.polls > 2 {
		// Completed with the failure.
		all = append(all, corepkg.Message{Version: 2, Seq: 2, Role: "assistant", Text: agentErrorText, UserVisible: true, Completed: true, MessageID: "m2", CreatedAtMs: 2})
	}
	var out []corepkg.Message
	latest := after
	for _, m := range all {
		if m.Version > after {
			out = append(out, m)
			latest = m.Version
		}
	}
	return out, latest, nil
}

// An agent's error is never a reply: a turn the core reports failed ends
// the job failed (agent_error) even when it posted progress before, and the
// error output that completed with the failure reaches the room only as the
// notice's fenced detail.
func TestFailedTurnIsNeverAReply(t *testing.T) {
	for _, progress := range []bool{false, true} {
		g := newSandboxGateway(t, "room-1", false)
		prov := &failingTurnProv{progress: progress}
		b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: newTestCore(prov)})
		b.runBridgeJob(context.Background(), bridgeJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch", OwnerUserID: "maya", RequesterUserID: "maya",
			Core: "core-a", Provider: "codex", TargetID: "t", MessageText: "build the demo app"})

		if st := g.lastStatus(); st["status"] != "failed" || st["failureReason"] != "agent_error" {
			t.Fatalf("progress=%v: status = %+v; want failed/agent_error", progress, st)
		}
		_, fresh := g.stored()
		var replies []string
		var notices int
		for _, m := range fresh {
			if payloadField(m, "notice") == "" {
				replies = append(replies, payloadText(m))
				continue
			}
			notices++
			if payloadField(m, "failReason") != "agent_error" || !strings.Contains(payloadField(m, "detail"), "Missing environment variable") {
				t.Fatalf("progress=%v: notice = %+v; want agent_error with the core's message as its detail", progress, m.Payload)
			}
			if strings.Contains(payloadText(m), "Missing environment variable") {
				t.Fatalf("progress=%v: the agent's output is in the notice sentence, not fenced in its detail: %q", progress, payloadText(m))
			}
		}
		if notices != 1 {
			t.Fatalf("progress=%v: %d notices; want exactly one", progress, notices)
		}
		for _, r := range replies {
			if strings.Contains(r, "Missing environment variable") {
				t.Fatalf("progress=%v: the agent's error was posted as a reply: %q", progress, replies)
			}
		}
		if want := map[bool]int{false: 0, true: 1}[progress]; len(replies) != want {
			t.Fatalf("progress=%v: replies = %q; want %d (what was said while the turn ran stays)", progress, replies, want)
		}
	}
}

// siteTurnProv runs one turn whose agent rewrites its static site on every
// status poll while busy, then replies and goes idle.
type siteTurnProv struct {
	sessionFilesProv
	mu        sync.Mutex
	polls     int
	busyPolls int
}

func (p *siteTurnProv) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	return "ws", nil
}

func (p *siteTurnProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	if p.polls <= p.busyPolls {
		p.fc.set("index.html", "<!doctype html><title>Demo</title><p>draft "+string(rune('a'+p.polls))+"</p>")
		p.fc.set("app.js", "console.log("+string(rune('0'+p.polls%10))+")")
		return corepkg.TurnStatus{Idle: false}, nil
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

func (p *siteTurnProv) Messages(_ context.Context, _ corepkg.Endpoint, _, _ string, after int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.polls <= p.busyPolls || after >= 1 {
		return nil, after, nil
	}
	return []corepkg.Message{{Version: 1, Seq: 1, Role: "assistant", Text: "The demo app is ready.", UserVisible: true, Completed: true, MessageID: "m1"}}, 1, nil
}

// A turn that rewrites its site many times publishes ONE site version, at
// settle: never one per rewrite while the turn runs.
func TestOneTurnPublishesOneSiteVersion(t *testing.T) {
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	prov := &siteTurnProv{sessionFilesProv: sessionFilesProv{fc: fc}, busyPolls: 8}
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya",
		Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"}}}})
	b.runBridgeJob(context.Background(), bridgeJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch", OwnerUserID: "maya", RequesterUserID: "maya",
		Core: "core-a", Provider: "codex", TargetID: "t", MessageText: "build the demo app"})

	// The settle publish runs on its own goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for len(cg.sitePublishes()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // nothing else may follow
	pubs := cg.sitePublishes()
	if len(pubs) != 1 {
		t.Fatalf("site versions published in one turn = %d; want exactly 1", len(pubs))
	}
	bundle, err := tarbundle.Parse([]byte(pubs[0]["file"].(string)), "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatalf("site publish is not a site bundle: %v", err)
	}
	index, _ := bundle.File("index.html")
	if pubs[0]["sourceJobId"] != "job-1" || !strings.Contains(string(index), "draft "+string(rune('a'+prov.busyPolls))) {
		t.Fatalf("site publish = job %v, index %q; want job-1 with the turn's final site", pubs[0]["sourceJobId"], index)
	}
}
