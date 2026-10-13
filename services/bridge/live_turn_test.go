package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// liveTurnProv runs a turn that reports its steps while it works and then
// ends as end says, recording the deadline each request carried.
type liveTurnProv struct {
	recordingProv
	mu        sync.Mutex
	polls     int
	deadlines []int64
	end       corepkg.TurnStatus
}

func (p *liveTurnProv) CreateSession(_ context.Context, _ corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deadlines = append(p.deadlines, req.Options.DeadlineMs)
	return corepkg.SessionResult{SessionID: "s"}, nil
}

func (p *liveTurnProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	switch {
	case p.polls < 3:
		return corepkg.TurnStatus{StartedAtMs: 100, Activity: []corepkg.TurnActivity{{Kind: "tool", Title: "Read: template.pptx", AtMs: 101}}}, nil
	case p.polls < 12:
		return corepkg.TurnStatus{StartedAtMs: 100, Activity: []corepkg.TurnActivity{{Kind: "tool", Title: "Read: template.pptx", AtMs: 101}, {Kind: "tool", Title: "Bash: python extract.py " + strings.Repeat("x", 200), AtMs: 102}}}, nil
	}
	return p.end, nil
}

// The bridge owns a turn's length: it sends the deadline (its bound from
// now) to the core, relays the running turn's steps to the room (when they
// change, within bounds), and a turn the core ends at the deadline fails
// as having run out of time, with no sandbox hint.
func TestLiveTurn_deadlineAndProgress(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &liveTurnProv{end: corepkg.TurnStatus{Idle: true, Failed: true, FailCode: corepkg.FailCodeDeadline, FailMessage: "deadline"}}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), MaxTurn: 45 * time.Minute})
	start := time.Now()
	b.runBridgeJob(context.Background(), job("job-1", "alice"))

	if len(prov.deadlines) != 1 {
		t.Fatalf("deadlines = %v", prov.deadlines)
	}
	if d := time.UnixMilli(prov.deadlines[0]).Sub(start); d < 44*time.Minute || d > 46*time.Minute {
		t.Fatalf("deadline is %v from the start, want the 45 minute bound", d)
	}
	gw.mu.Lock()
	progress := append(gw.progress[:0:0], gw.progress...)
	gw.mu.Unlock()
	if len(progress) != 2 || progress[0].StartedAtMs != 100 || len(progress[1].Activity) != 2 || len([]rune(progress[1].Activity[1].Title)) != 120 || progress[1].BridgeID == "" {
		t.Fatalf("progress = %+v; want the two distinct reports, titles cut", progress)
	}
	if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "reply_timeout" {
		t.Fatalf("status = %v", st)
	}
	_, fresh := gw.stored()
	if len(fresh) != 1 || !strings.Contains(payloadText(fresh[0]), "ran out of time after 45 minutes") || strings.Contains(payloadText(fresh[0]), "Environment passed") {
		t.Fatalf("notice = %q", storedTexts(fresh))
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Hour: "2 hours", time.Hour: "1 hour", 90 * time.Minute: "90 minutes", time.Minute: "1 minute", 30 * time.Second: "30s"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
