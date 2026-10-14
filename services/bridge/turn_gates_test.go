package bridge

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// parallelProv runs every turn until release is closed, and records how
// many ran at once, overall and per session.
type parallelProv struct {
	recordingProv
	limit   int
	release chan struct{}
	started chan string

	mu       sync.Mutex
	sessions int
	running  map[string]bool
	fresh    map[string]bool // started, not yet seen busy
	now      int
	peak     int
	same     int // a turn started in a session already running one
}

func newParallelProv(limit int) *parallelProv {
	return &parallelProv{limit: limit, release: make(chan struct{}), started: make(chan string, 16), running: map[string]bool{}, fresh: map[string]bool{}}
}

func (p *parallelProv) MaxConcurrentTurns(context.Context, corepkg.Endpoint) int { return p.limit }

func (p *parallelProv) start(sid string) {
	p.mu.Lock()
	if p.running[sid] {
		p.same++
	}
	p.running[sid], p.fresh[sid] = true, true
	p.now++
	p.peak = max(p.peak, p.now)
	p.mu.Unlock()
	p.started <- sid
}

func (p *parallelProv) CreateSession(context.Context, corepkg.Endpoint, corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	p.sessions++
	sid := "s" + strconv.Itoa(p.sessions)
	p.mu.Unlock()
	p.start(sid)
	return corepkg.SessionResult{SessionID: sid}, nil
}

func (p *parallelProv) SendInput(_ context.Context, _ corepkg.Endpoint, _, sid, _ string, _ corepkg.SessionOptions) error {
	p.start(sid)
	return nil
}

func (p *parallelProv) PollTurn(_ context.Context, _ corepkg.Endpoint, _, sid string) (corepkg.TurnStatus, error) {
	select {
	case <-p.release:
	default:
		return corepkg.TurnStatus{}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fresh[sid] {
		p.fresh[sid] = false
		return corepkg.TurnStatus{}, nil
	}
	if p.running[sid] {
		p.running[sid] = false
		p.now--
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

// runJobs runs jobs on b at once and returns their wait.
func runJobs(b *Bridge, jobs ...bridgeJob) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.runBridgeJob(context.Background(), j)
		}()
	}
	return &wg
}

func channelJob(id, channel, requester string) bridgeJob {
	j := job(id, requester)
	j.ChannelID = channel
	return j
}

// waitStarts waits for n turn starts; then no other may start for a while.
func waitStarts(t *testing.T, p *parallelProv, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-p.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("%d of %d turns started", i, n)
		}
	}
	select {
	case sid := <-p.started:
		t.Fatalf("turn %s started past the limit", sid)
	case <-time.After(300 * time.Millisecond):
	}
}

// One agent runs the turns of several channels at once, up to its core's
// limit (2 when the core states none); the others wait for a turn to end.
func TestOneAgentRunsTurnsInParallelUpToItsLimit(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{{0, defaultAgentTurns}, {3, 3}, {1, 1}} {
		t.Run(strconv.Itoa(tc.limit), func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := newParallelProv(tc.limit)
			b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
			var jobs []bridgeJob
			for i := 0; i < 4; i++ {
				jobs = append(jobs, channelJob("job-"+strconv.Itoa(i), "ch-"+strconv.Itoa(i), "alice"))
			}
			wg := runJobs(b, jobs...)
			waitStarts(t, prov, tc.want)
			close(prov.release)
			wg.Wait()
			if prov.peak != tc.want || prov.sessions != 4 {
				t.Fatalf("peak %d over %d sessions; want %d at once", prov.peak, prov.sessions, tc.want)
			}
		})
	}
}

// The turns of one conversation queue, never interleaving on one core
// session, and the waiting job keeps renewing its lease.
func TestTurnsOfOneSessionQueueAndTheWaitingJobKeepsItsLease(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := newParallelProv(4)
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.leaseRenewOverride = 20 * time.Millisecond
	first := runJobs(b, channelJob("job-1", "ch", "alice"))
	waitStarts(t, prov, 1)
	second := runJobs(b, channelJob("job-2", "ch", "alice"))
	waitStarts(t, prov, 0)
	gw.mu.Lock()
	renewed := gw.leases["job-2"]
	gw.mu.Unlock()
	if renewed == 0 {
		t.Fatal("the waiting job's lease was not renewed")
	}
	close(prov.release)
	first.Wait()
	second.Wait()
	if prov.same != 0 || prov.sessions != 1 {
		t.Fatalf("%d turns overlapped in one session (%d sessions)", prov.same, prov.sessions)
	}
}

// A borrower's turn runs beside the owner's own: their seats are separate
// sessions of the agent.
func TestABorrowersTurnRunsBesideTheOwners(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := newParallelProv(0)
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	wg := runJobs(b, channelJob("job-1", "ch", "alice"), channelJob("job-2", "ch", "bob"))
	waitStarts(t, prov, 2)
	close(prov.release)
	wg.Wait()
	if prov.sessions != 2 || prov.peak != 2 {
		t.Fatalf("sessions %d, peak %d; want the two seats at once", prov.sessions, prov.peak)
	}
}
