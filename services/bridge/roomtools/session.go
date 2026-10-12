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
	// PollInterval is how often ask_agent checks a dispatched job.
	PollInterval time.Duration
	// DefaultWait / MaxWait bound how long ask_agent waits for a
	// dispatched job to settle before returning its handle.
	DefaultWait time.Duration
	MaxWait     time.Duration
	// DeliverableSettle is how long ask_agent waits after a job settles
	// before reading its deliverables (the bridge publishes them just after
	// the reply).
	DeliverableSettle time.Duration
}

// Default waits. MaxWait stays well under the bridge's 30-minute hard cap
// on one turn: ask_agent runs inside the dispatching agent's own turn.
const (
	defaultPollInterval      = 2 * time.Second
	defaultWait              = 10 * time.Minute
	defaultMaxWait           = 20 * time.Minute
	defaultDeliverableSettle = 3 * time.Second
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
	if c.DeliverableSettle < 0 {
		c.DeliverableSettle = 0
	} else if c.DeliverableSettle == 0 {
		c.DeliverableSettle = defaultDeliverableSettle
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
	// Workspace reads the session's workspace files (save_component); nil
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

	mu         sync.Mutex
	job        Job
	dispatched []dispatched
	gone       bool
	// notes holds the bound job's team guard and progress state.
	notes jobNotes
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
	s.job = j
	s.mu.Unlock()
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
	s.mu.Unlock()
}

func (s *Session) revoked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gone
}
