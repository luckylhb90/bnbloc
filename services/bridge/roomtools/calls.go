package roomtools

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The record of tool calls (docs/prd/2026-10-20-fewer-sharper-room-tools.md
// §2.3): every tools/call a session makes while it drives a job is kept
// against that job (the tool, its action when it has actions, the outcome;
// never the arguments or the result) until the job's outcome takes it
// (Server.TakeCalls) onto the run record beside the tools the session had
// (wire.RunDiagTools). What tells which tools earn their place is measured,
// not guessed.

// maxCallJobs bounds the jobs whose calls wait to be taken: a job whose
// outcome never takes them (a drive that ended elsewhere) is dropped,
// oldest first.
const maxCallJobs = 256

// callLog is one job's calls: the first wire.MaxRunToolCalls, and how many
// more were made.
type callLog struct {
	calls   []wire.AgentRunToolCall
	dropped int
}

// callRecord is the server's record of calls, by job.
type callRecord struct {
	mu    sync.Mutex
	jobs  map[string]*callLog
	order []string // job ids, oldest first
}

// add records one call of job.
func (r *callRecord) add(job string, c wire.AgentRunToolCall) {
	if job == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*callLog{}
	}
	l, ok := r.jobs[job]
	if !ok {
		l = &callLog{}
		r.jobs[job] = l
		r.order = append(r.order, job)
		for len(r.order) > maxCallJobs {
			delete(r.jobs, r.order[0])
			r.order = r.order[1:]
		}
	}
	if len(l.calls) >= wire.MaxRunToolCalls {
		l.dropped++
		return
	}
	l.calls = append(l.calls, c)
}

// take removes and answers job's calls (empty when it made none).
func (r *callRecord) take(job string) wire.AgentRunTools {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.jobs[job]
	if !ok {
		return wire.AgentRunTools{}
	}
	delete(r.jobs, job)
	if i := slices.Index(r.order, job); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
	return wire.AgentRunTools{Calls: l.calls, CallsDropped: l.dropped}
}

// TakeCalls answers the room tool calls the sessions made for jobID and
// forgets them: the job's outcome report carries them once.
func (s *Server) TakeCalls(jobID string) wire.AgentRunTools { return s.calls.take(jobID) }

// outcome is how a call ended, as the record keeps it.
func (r Result) outcome() string {
	switch {
	case r.Err == "":
		return wire.ToolCallOK
	case r.failed:
		return wire.ToolCallError
	default:
		return wire.ToolCallRefused
	}
}

// actionOf is the call's action when the tool takes one (an "action"
// parameter with an enum) and args name one of them; "" otherwise. Only a
// listed action is kept: the record never holds text the agent wrote.
func actionOf(t Tool, args json.RawMessage) string {
	props, _ := t.InputSchema["properties"].(map[string]any)
	p, _ := props["action"].(map[string]any)
	enum, _ := p["enum"].([]string)
	if len(enum) == 0 {
		return ""
	}
	var in struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(args, &in) != nil || !slices.Contains(enum, in.Action) {
		return ""
	}
	return in.Action
}
