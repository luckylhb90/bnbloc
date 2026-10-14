package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// overflowProv runs each session's turn to the end its script gives (the
// n-th created session ends as ends[n-1]); a session that ends without a
// failure answers one reply; acts[n-1] makes it report a step while busy,
// says[n-1] a reply even when it fails.
type overflowProv struct {
	recordingProv
	mu       sync.Mutex
	ends     []corepkg.TurnStatus
	acts     []bool
	says     []bool
	sessions int
	polls    map[string]int
}

func (p *overflowProv) CreateSession(context.Context, corepkg.Endpoint, corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions++
	return corepkg.SessionResult{SessionID: "s" + strconv.Itoa(p.sessions)}, nil
}

func (p *overflowProv) end(sid string) corepkg.TurnStatus {
	n, _ := strconv.Atoi(strings.TrimPrefix(sid, "s"))
	if n < 1 || n > len(p.ends) {
		return corepkg.TurnStatus{Idle: true}
	}
	return p.ends[n-1]
}

func (p *overflowProv) scripted(flags []bool, sid string) bool {
	n, _ := strconv.Atoi(strings.TrimPrefix(sid, "s"))
	return n >= 1 && n <= len(flags) && flags[n-1]
}

func (p *overflowProv) PollTurn(_ context.Context, _ corepkg.Endpoint, _, sid string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.polls == nil {
		p.polls = map[string]int{}
	}
	p.polls[sid]++
	busy := 1
	if p.scripted(p.acts, sid) || p.scripted(p.says, sid) {
		busy = 2 // the turn start's poll, then the reflow's
	}
	if p.polls[sid] <= busy {
		var ts corepkg.TurnStatus
		if p.scripted(p.acts, sid) {
			ts.Activity = []corepkg.TurnActivity{{Kind: "tool", Title: "write report.md"}}
		}
		return ts, nil
	}
	return p.end(sid), nil
}

func (p *overflowProv) Messages(_ context.Context, _ corepkg.Endpoint, _, sid string, after int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if (p.end(sid).Failed && !p.scripted(p.says, sid)) || after >= 1 {
		return nil, after, nil
	}
	return []corepkg.Message{{Version: 1, Seq: 1, Role: "assistant", Text: "done in " + sid, UserVisible: true, Completed: true, MessageID: "m-" + sid, CreatedAtMs: 1}}, 1, nil
}

const providerOverflow = `API Error: 400 litellm.ContextWindowExceededError: {"message":"The input (1245699 tokens) is longer than the model's context length (327680 tokens)."}`

// A turn whose conversation outgrew its model is run once more in a fresh
// session, with one calm notice; the fresh session's reply is the job's.
func TestContextOverflowContinuesInAFreshSession(t *testing.T) {
	for name, first := range map[string]corepkg.TurnStatus{
		"read from the text":   {Idle: true, Failed: true, FailMessage: providerOverflow},
		"reported by the core": {Idle: true, Failed: true, FailCode: corepkg.FailCodeContextOverflow, FailMessage: "too long"},
	} {
		t.Run(name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := &overflowProv{ends: []corepkg.TurnStatus{first, {Idle: true}}}
			dir := t.TempDir()
			b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), StateDir: dir})
			b.runBridgeJob(context.Background(), job("job-1", "alice"))

			// The kept binding is the fresh session's: a restart resumes it.
			if raw, _ := os.ReadFile(filepath.Join(dir, workspacesFile)); !strings.Contains(string(raw), `"sessionId": "s2"`) || strings.Contains(string(raw), `"sessionId": "s1"`) {
				t.Fatalf("kept bindings = %s; want s2 in place of s1", raw)
			}
			if prov.sessions != 2 {
				t.Fatalf("sessions = %d; want the overflowing one and a fresh one", prov.sessions)
			}
			if st := gw.lastStatus(); st["status"] != "replied" {
				t.Fatalf("status = %v; want the fresh session's reply", st)
			}
			_, fresh := gw.stored()
			texts := storedTexts(fresh)
			if len(texts) != 2 || !strings.Contains(texts[0], "grew past codex's context window; continuing in a fresh session") || texts[1] != "done in s2" {
				t.Fatalf("rows = %q; want the reset notice then the reply", texts)
			}
		})
	}
}

// A second overflow fails the job context_overflow with a plain sentence;
// the provider's raw text stays in the notice's detail.
func TestContextOverflowTwiceFails(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	over := corepkg.TurnStatus{Idle: true, Failed: true, FailMessage: providerOverflow}
	prov := &overflowProv{ends: []corepkg.TurnStatus{over, over}}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.runBridgeJob(context.Background(), job("job-1", "alice"))

	if prov.sessions != 2 {
		t.Fatalf("sessions = %d; want exactly one retry", prov.sessions)
	}
	if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "context_overflow" {
		t.Fatalf("status = %v", st)
	}
	_, fresh := gw.stored()
	texts := storedTexts(fresh)
	last := fresh[len(fresh)-1]
	if len(texts) != 2 || strings.Contains(texts[1], "litellm") || !strings.Contains(texts[1], "even in a fresh session") ||
		!strings.Contains(payloadField(last, "detail"), "ContextWindowExceededError") {
		t.Fatalf("rows = %q (detail %q)", texts, payloadField(last, "detail"))
	}
}

// An overflowing turn that already did something (a step, a reply) is not
// run again, which could repeat it: its session is reset, and the room is
// told once to send the request again. The next request starts fresh.
func TestContextOverflowAfterWorkIsNotRunAgain(t *testing.T) {
	over := corepkg.TurnStatus{Idle: true, Failed: true, FailCode: corepkg.FailCodeContextOverflow, FailMessage: "too long"}
	for name, prov := range map[string]*overflowProv{
		"a step":  {ends: []corepkg.TurnStatus{over}, acts: []bool{true}},
		"a reply": {ends: []corepkg.TurnStatus{over}, says: []bool{true}},
	} {
		t.Run(name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
			b.runBridgeJob(context.Background(), job("job-1", "alice"))
			if prov.sessions != 1 {
				t.Fatalf("sessions = %d; want the request not run again", prov.sessions)
			}
			if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "context_reset" {
				t.Fatalf("status = %v", st)
			}
			_, fresh := gw.stored()
			texts := storedTexts(fresh)
			resets := 0
			for _, text := range texts {
				if strings.Contains(text, "conversation was reset") && strings.Contains(text, "send the request again") {
					resets++
				}
				if strings.Contains(text, "continuing in a fresh session") {
					t.Fatalf("rows = %q; want no continuation notice", texts)
				}
			}
			if resets != 1 {
				t.Fatalf("rows = %q; want one reset notice", texts)
			}
			b.runBridgeJob(context.Background(), job("job-2", "alice"))
			if prov.sessions != 2 {
				t.Fatalf("sessions = %d; want the next request in a fresh session", prov.sessions)
			}
		})
	}
}

// Any other failure is not retried.
func TestOtherFailureIsNotRetried(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &overflowProv{ends: []corepkg.TurnStatus{{Idle: true, Failed: true, FailMessage: "missing API key"}}}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.runBridgeJob(context.Background(), job("job-1", "alice"))
	if prov.sessions != 1 {
		t.Fatalf("sessions = %d; want no retry", prov.sessions)
	}
	if st := gw.lastStatus(); st["failureReason"] != "agent_error" {
		t.Fatalf("status = %v", st)
	}
}

func TestContextOverflowClassifier(t *testing.T) {
	for detail, want := range map[string]bool{
		providerOverflow: true,
		"prompt is too long: 210000 tokens > 200000 maximum":   true,
		"This model's maximum context length is 128000 tokens": true,
		"context_length_exceeded":                              true,
		"Rate limit reached: too many tokens per minute (TPM)": false,
		"missing API key": false,
		"unexpected status 403 Forbidden: model not allowed":               false,
		"the agent returned an error: Request too many tokens, rate_limit": false,
	} {
		if got := contextOverflow(turnFailedError{detail: detail}); got != want {
			t.Errorf("contextOverflow(%q) = %v, want %v", detail, got, want)
		}
	}
	if !contextOverflow(turnFailedError{detail: "x", code: corepkg.FailCodeContextOverflow}) {
		t.Error("the core's code is an overflow")
	}
	if contextOverflow(errors.New("context window")) {
		t.Error("only a failed turn is an overflow")
	}
}
