package roomtools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Gateway is the bridge's own authenticated gateway client. The tools make
// every call through it: the agent never holds a credential that works
// outside this process.
type Gateway interface {
	// Do sends one request (body JSON-encoded when non-nil) to a gateway
	// path and returns the response body. A non-2xx answer is a
	// *StatusError.
	Do(ctx context.Context, method, path string, body any) ([]byte, error)
}

// StatusError is a non-2xx gateway answer.
type StatusError struct {
	Status int
	Body   []byte
}

func (e *StatusError) Error() string { return "gateway answered " + httpStatusText(e.Status) }

// Config tunes the server. Zero values take the defaults.
type Config struct {
	Gateway Gateway
	// GatewayBaseURL prefixes deliverable links ("" = relative paths).
	GatewayBaseURL string
	// Addr is the loopback address to listen on, when free (the bridge
	// keeps its server's address across restarts, so the sessions it
	// restores keep reaching it); "" or a busy one = a random port.
	Addr string
	// PollInterval is how often ask_agent checks a dispatched job.
	PollInterval time.Duration
	// DefaultWait / MaxWait bound how long ask_agent waits for a
	// dispatched job to settle before returning its handle.
	DefaultWait time.Duration
	MaxWait     time.Duration
	// Names resolves people's display names (id → name) the way the
	// bridge's records do (nil = ids only).
	Names func(ctx context.Context, ids []string) map[string]string
	// EgressDir holds the egress proxy's local CA (egress_ca.go): the
	// member's directory. "" keeps it for this run only.
	EgressDir string
	// NoEgress turns the egress proxy off.
	NoEgress bool
	// ProxyAddr is a loopback address the proxy alone also listens on: the
	// host port a sandbox opens to its guests (BNBLOC_SANDBOX_HOST_PROXY).
	ProxyAddr string
	// PrivateHosts are the hosts a sandboxed command's tunnel may reach at
	// a private address (the member's own, BNBLOC_SANDBOX_PRIVATE_HOSTS);
	// every other sandboxed tunnel reaches only public addresses.
	PrivateHosts []string
}

// Default waits: ask_agent runs inside the dispatching agent's own turn,
// so it returns a handle (room_status) rather than wait without end.
const (
	defaultPollInterval = 2 * time.Second
	defaultWait         = 10 * time.Minute
	defaultMaxWait      = 20 * time.Minute
)

// waitDescription is the wait_seconds parameter's description, from the
// default waits.
func waitDescription(more string) string {
	return fmt.Sprintf("How long to wait for the result in seconds (default %d, at most %d).%s", int(defaultWait/time.Second), int(defaultMaxWait/time.Second), more)
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.DefaultWait <= 0 {
		c.DefaultWait = defaultWait
	}
	if c.MaxWait <= 0 {
		c.MaxWait = defaultMaxWait
	}
	if c.DefaultWait > c.MaxWait {
		c.DefaultWait = c.MaxWait
	}
	return c
}

// Scope is the agent session a bearer was minted for.
type Scope struct {
	RoomID   string
	TenantID string
	// Owner / Core / Provider are the agent the session drives.
	Owner    string
	Core     string
	Provider string
}

// Job is the job a session is driving: the parent of its dispatches.
type Job struct {
	JobID           string
	RequesterUserID string
	ChannelID       string
	MessageSeq      int64
	// ByRequester marks a turn its requester opened with their own message
	// (not a schedule's post, not another agent's dispatch): the turn in
	// which a "the person answered" checkpoint may hold (relay session).
	ByRequester bool
	// Workspace reads the session's workspace files (components save); nil
	// when the core offers no session files.
	Workspace Workspace
	// Team is the roster when the turn wears a team expert (team.go).
	Team *Team
}

// maxTrackedDispatches bounds the dispatches a session remembers for
// room_status (the server-side fan-out limit is far lower per job).
const maxTrackedDispatches = 64

// Session is one driven agent session's room tools state.
type Session struct {
	scope Scope
	token string

	mu  sync.Mutex
	job Job
	// ran are the jobs the session drove before the bound one, newest last
	// (a tool's view that outlives its turn posts for its own: relay.go).
	ran        []string
	dispatched []dispatched
	gone       bool
	// notes holds the bound job's team guard and progress state.
	notes jobNotes
	// host are the host tools this session fronts (hosttools.go).
	host map[string]HostTool
	// egress are the hosts services claim for the session, the services
	// that take a streamed body, and the cards posted for the bound job
	// (egress.go).
	egress   egressHosts
	streams  map[string]bool
	paths    map[string][]string
	cards    map[[2]string]bool
	cardsJob string
}

// dispatched is one dispatch this session made.
type dispatched struct {
	JobID    string
	Handle   string
	Sequence int64
}

// Bind records the job the session is driving now. Every turn binds its own
// job, so the bearer only ever dispatches on behalf of the current one (and
// realtime admits it only while that job runs on this owner's bridge).
func (s *Session) Bind(j Job) {
	s.mu.Lock()
	if s.job.JobID != "" && s.job.JobID != j.JobID {
		s.ran = append(s.ran, s.job.JobID)
		if len(s.ran) > maxRanJobs {
			s.ran = s.ran[len(s.ran)-maxRanJobs:]
		}
	}
	s.job = j
	s.mu.Unlock()
}

// Unbind ends the turn the session is driving: the bound job is remembered as
// one the session drove and nothing is bound until the next turn's Bind. A call
// that acts (a room tool, a relayed call, a host tool) is refused between turns:
// a process of the agent that outlives its turn holds the bearer, and the bearer
// alone must not be enough. It does nothing when another job is bound already
// (the next turn began before this one's end was reported).
func (s *Session) Unbind(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobID == "" || s.job.JobID != jobID {
		return
	}
	s.ran = append(s.ran, s.job.JobID)
	if len(s.ran) > maxRanJobs {
		s.ran = s.ran[len(s.ran)-maxRanJobs:]
	}
	s.job = Job{}
}

// droveJobs is every job the session drove that it remembers, the bound
// one last.
func (s *Session) droveJobs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.ran...)
	if s.job.JobID != "" {
		out = append(out, s.job.JobID)
	}
	return out
}

// maxRanJobs bounds the earlier jobs a session remembers.
const maxRanJobs = 16

// drove reports whether the session drove jobID: the bound job or an
// earlier one it remembers.
func (s *Session) drove(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobID == s.job.JobID {
		return true
	}
	for _, id := range s.ran {
		if id == jobID {
			return true
		}
	}
	return false
}

// Scope is the session's agent.
func (s *Session) Scope() Scope { return s.scope }

// workspace is the bound job's workspace reader (nil = none).
func (s *Session) workspace() Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job.Workspace
}

func (s *Session) current() Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job
}

func (s *Session) remember(d dispatched) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatched = append(s.dispatched, d)
	if len(s.dispatched) > maxTrackedDispatches {
		s.dispatched = s.dispatched[len(s.dispatched)-maxTrackedDispatches:]
	}
}

func (s *Session) dispatches() []dispatched {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dispatched(nil), s.dispatched...)
}

func (s *Session) owns(jobID string) (dispatched, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dispatched {
		if d.JobID == jobID {
			return d, true
		}
	}
	return dispatched{}, false
}

func (s *Session) revoke() {
	s.mu.Lock()
	s.gone = true
	s.job = Job{}
	s.ran = nil
	s.mu.Unlock()
	s.closeHostTools()
}

func (s *Session) revoked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gone
}
