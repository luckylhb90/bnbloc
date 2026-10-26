package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// steerProv is a core that takes steers (outcome, err) and records them.
type steerProv struct {
	*parallelProv
	outcome corepkg.SteerOutcome
	err     error

	smu   sync.Mutex
	steer []string
}

func (p *steerProv) Steer(_ context.Context, _ corepkg.Endpoint, _, sid, text string) (corepkg.SteerOutcome, error) {
	p.smu.Lock()
	p.steer = append(p.steer, sid+": "+text)
	p.smu.Unlock()
	return p.outcome, p.err
}

func (p *steerProv) steers() []string {
	p.smu.Lock()
	defer p.smu.Unlock()
	return append([]string(nil), p.steer...)
}

// noSteerProv is the same core without the capability.
type noSteerProv struct{ *parallelProv }

func steerJob(id, channel, requester, text string) bridgeJob {
	j := channelJob(id, channel, requester)
	j.MessageText = text
	return j
}

// waitBound waits until job id of b is bound to its session (a turn runs).
func waitBound(t *testing.T, b *Bridge, id string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		b.runMu.Lock()
		rj := b.running[id]
		b.runMu.Unlock()
		if rj != nil {
			if _, rs := rj.bound(); rs != nil {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("job %s never bound a session", id)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func steeredStatus(gw *sandboxGateway, into string) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	for _, s := range gw.statuses {
		if d, _ := s["diagnostics"].(map[string]any); d != nil && d[steeredInto] == into && s["status"] == "replied" {
			return true
		}
	}
	return false
}

// A message sent while its own session's turn runs joins that turn when the
// core takes it: no second turn starts, and the job settles replied, naming
// the one it joined.
func TestAMessageJoinsTheRunningTurnOfItsOwnSession(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &steerProv{parallelProv: newParallelProv(4), outcome: corepkg.SteerInjected}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	first := runJobs(b, steerJob("job-1", "ch", "alice", "build it"))
	waitStarts(t, prov.parallelProv, 1)
	waitBound(t, b, "job-1")
	second := runJobs(b, steerJob("job-2", "ch", "alice", "use blue instead"))
	second.Wait() // settled at once, while the first still runs
	got := prov.steers()
	if len(got) != 1 || got[0] != "s1: [added while you work — from alice]\nuse blue instead" {
		t.Fatalf("steers = %q", got)
	}
	if !steeredStatus(gw, "job-1") {
		t.Fatalf("job-2 did not settle replied into job-1: %v", gw.statuses)
	}
	close(prov.release)
	first.Wait()
	if prov.sessions != 1 {
		t.Fatalf("%d sessions; the steer must not start another turn", prov.sessions)
	}
}

// Idle, unsupported, an error or a core without the capability: the message is
// queued behind the turn as before and runs once it ends.
func TestAMessageThatCannotBeSteeredQueues(t *testing.T) {
	cases := map[string]func() corepkg.Provider{
		"idle": func() corepkg.Provider {
			return &steerProv{parallelProv: newParallelProv(4), outcome: corepkg.SteerIdle}
		},
		"unsupported": func() corepkg.Provider {
			return &steerProv{parallelProv: newParallelProv(4), outcome: corepkg.SteerUnsupported}
		},
		"error": func() corepkg.Provider {
			return &steerProv{parallelProv: newParallelProv(4), err: errors.New("core restarting")}
		},
		"no capability": func() corepkg.Provider { return &noSteerProv{newParallelProv(4)} },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := mk()
			var pp *parallelProv
			switch p := prov.(type) {
			case *steerProv:
				pp = p.parallelProv
			case *noSteerProv:
				pp = p.parallelProv
			}
			b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
			b.leaseRenewOverride = 20 * time.Millisecond
			first := runJobs(b, steerJob("job-1", "ch", "alice", "build it"))
			waitStarts(t, pp, 1)
			waitBound(t, b, "job-1")
			second := runJobs(b, steerJob("job-2", "ch", "alice", "also this"))
			waitStarts(t, pp, 0) // still waiting at the session gate
			if steeredStatus(gw, "job-1") {
				t.Fatal("a message the core did not take was marked steered")
			}
			close(pp.release)
			first.Wait()
			second.Wait()
			if pp.same != 0 {
				t.Fatalf("%d turns overlapped in one session", pp.same)
			}
		})
	}
}

// Only the message's own session is steered: another member's message to the
// same agent runs in their own session (rule 1 of multi-member rooms), and so
// does another channel's.
func TestAMessageNeverSteersAnotherSessionsTurn(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &steerProv{parallelProv: newParallelProv(4), outcome: corepkg.SteerInjected}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	wg := runJobs(b, steerJob("job-1", "ch", "alice", "build it"))
	waitStarts(t, prov.parallelProv, 1)
	waitBound(t, b, "job-1")
	wg2 := runJobs(b, steerJob("job-2", "ch", "bob", "my own task"), steerJob("job-3", "other", "alice", "elsewhere"))
	waitStarts(t, prov.parallelProv, 2)
	if got := prov.steers(); len(got) != 0 {
		t.Fatalf("steered %q across sessions", got)
	}
	close(prov.release)
	wg.Wait()
	wg2.Wait()
	if prov.sessions != 3 {
		t.Fatalf("%d sessions, want one each", prov.sessions)
	}
}

// A message with an attachment, a file to edit or an expert needs its own turn.
func TestAMessageThatNeedsItsOwnTurnIsNotSteered(t *testing.T) {
	for name, edit := range map[string]func(*bridgeJob){
		"attachment": func(j *bridgeJob) { j.AttachmentIDs = []string{"a1"} },
		"expert":     func(j *bridgeJob) { j.ExpertID = "cc_x" },
		"dispatched": func(j *bridgeJob) { j.DispatchedByJobID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			d := &drive{job: steerJob("j", "ch", "alice", "text"), text: "text"}
			edit(&d.job)
			if d.steerable() {
				t.Fatal("steerable")
			}
		})
	}
	if d := (&drive{job: steerJob("j", "ch", "alice", "text"), text: "text"}); !d.steerable() {
		t.Fatal("a plain person's message is steerable")
	}
}
