package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Expert teams (docs/prd/2026-10-12-experts-and-teams.md §8): a turn
// wearing a team expert binds its roster to the session, so ask_agent
// dispatches to a role by name (its agent, wearing its expert) and the
// team's guards hold in the tool layer: a capability check (no unknown or
// unstaffed role), a timeout notice when a member does not finish in time,
// and no further dispatch after wire.MaxTeamFailures failed member tasks.
// post_progress posts a progress line as a room notice of the job.

// Team is the roster of a turn wearing a team expert.
type Team struct {
	// Name is the team expert's display name; Lead the lead's role title.
	Name  string
	Lead  string
	Roles []TeamRole
}

// TeamRole is one role as it resolved for the turn.
type TeamRole struct {
	Role string
	// Agent takes the role (nil: unstaffed, Reason says why).
	Agent  *wire.PlanAgent
	Reason string
	// Expert is the role's expert as the requester may use it (nil:
	// Missing says why).
	Expert  *wire.ContentTeamExpert
	Missing string
}

func (t *Team) role(name string) (TeamRole, bool) {
	for _, r := range t.Roles {
		if r.Role == name {
			return r, true
		}
	}
	return TeamRole{}, false
}

func (t *Team) roleNames() string {
	names := make([]string, 0, len(t.Roles))
	for _, r := range t.Roles {
		names = append(names, r.Role)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Progress notice bounds per job.
const (
	maxProgressRunes = 500
	maxProgressLines = 20
)

// jobNotes is the per-job state of the team guards and progress notices
// (reset when the session binds another job).
type jobNotes struct {
	mu sync.Mutex
	st noteState
}

type noteState struct {
	jobID    string
	failures int
	lines    int
	lastSeq  int64
}

// forJob returns the state of jobID, starting it afresh for a new job.
// Callers hold n.mu.
func (n *jobNotes) forJob(jobID string) *noteState {
	if n.st.jobID != jobID {
		n.st = noteState{jobID: jobID}
	}
	return &n.st
}

// failed counts a failed member task of jobID and returns the total.
func (n *jobNotes) failed(jobID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.forJob(jobID)
	st.failures++
	return st.failures
}

func (n *jobNotes) failuresOf(jobID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.forJob(jobID).failures
}

// nextLine reserves one notice line of jobID: its sequence in the job's
// progress session (wall-clock milliseconds, strictly increasing, so a
// restarted bridge never reuses one), or false past the per-job limit of
// counted (progress) lines.
func (n *jobNotes) nextLine(jobID string, counted bool) (int64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.forJob(jobID)
	if counted {
		if st.lines >= maxProgressLines {
			return 0, false
		}
		st.lines++
	}
	seq := time.Now().UnixMilli()
	if seq <= st.lastSeq {
		seq = st.lastSeq + 1
	}
	st.lastSeq = seq
	return seq, true
}

// ProgressSessionID is the activity session a job's progress notices are
// recorded under: under the job's notice namespace (wire.JobNoticeSessionID),
// never a real agent session, so a notice neither moves a reply cursor nor
// counts as the agent's reply.
func ProgressSessionID(jobID string) string { return wire.JobNoticeSessionID(jobID) + "-progress" }

// postNotice posts one notice line for the bound job in its channel,
// attributed to the session's agent.
func postNotice(ctx context.Context, env *Env, job Job, kind, text string, counted bool) error {
	seq, ok := env.Session.notes.nextLine(job.JobID, counted)
	if !ok {
		return errProgressLimit
	}
	scope := env.Session.Scope()
	session := ProgressSessionID(job.JobID)
	body := map[string]any{
		"workspaceId":    scope.RoomID,
		"agentSessionId": session,
		"channelId":      job.ChannelID,
		"messages": []map[string]any{{
			"seq": seq,
			// The job and its request: the feed nests the line under it.
			"payload": wire.JobNotice{
				Kind: kind, MessageID: kind + "-" + job.JobID + "-" + strconv.FormatInt(seq, 10),
				JobID: job.JobID, SourceSeq: job.MessageSeq, Text: text, OwnerUserID: scope.Owner, Provider: scope.Provider,
			}.Payload(),
			"createdAtUnixMs": time.Now().UnixMilli(),
		}},
	}
	_, err := env.Gateway.Do(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/agent-activity/messages", body)
	return err
}

type progressLimitError struct{}

func (progressLimitError) Error() string { return "progress line limit reached" }

var errProgressLimit error = progressLimitError{}

// Notice kinds (payload.notice) the feed renders as notices.
const (
	NoticeProgress = "progress"
	NoticeTimeout  = "member-timeout"
)

func postProgressTool() Tool {
	return Tool{
		Name:  "post_progress",
		Title: "Post a progress line",
		Description: "Post one short progress line to the room, for example \"Phase 2/3: tests dispatched to tester\". " +
			"It shows as a notice of your current request, not as your reply. Use it once per phase of a longer task.",
		InputSchema: objectSchema(map[string]any{
			"text": map[string]any{"type": "string", "description": "The progress line (one sentence, at most 500 characters)."},
		}, "text"),
		Call: func(ctx context.Context, env *Env, args json.RawMessage) Result {
			var in struct {
				Text string `json:"text"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return toolError("invalid arguments: %v", err)
			}
			text := strings.Join(strings.Fields(in.Text), " ")
			switch {
			case text == "":
				return toolError("text is required")
			case utf8.RuneCountInString(text) > maxProgressRunes:
				return toolError("text is too long (at most %d characters)", maxProgressRunes)
			}
			job := env.Session.current()
			if job.JobID == "" {
				return toolError("post_progress works only while you are answering a request in the room")
			}
			if err := postNotice(ctx, env, job, NoticeProgress, text, true); err != nil {
				if err == errProgressLimit {
					return toolError("at most %d progress lines per request", maxProgressLines)
				}
				return refusal("posting the progress line", err)
			}
			return Result{Value: map[string]any{"posted": true}}
		},
	}
}

// teamTarget resolves ask_agent's handle for a turn wearing a team: a role
// name (no "/") is the role's agent and expert. ok=false with a Result is a
// refusal the agent reads; ok=false without one means "not a role" (an
// ordinary handle).
func teamTarget(team *Team, handle, expert string) (TeamRole, *Result) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if team == nil || strings.Contains(name, "/") {
		return TeamRole{}, nil
	}
	r, ok := team.role(name)
	switch {
	case !ok:
		res := toolError("%q is not a role of your team (roles: %s). The roster does not cover it: refuse that part of the request instead of improvising a role", name, team.roleNames())
		return TeamRole{}, &res
	case expert != "":
		res := toolError("the %s role brings its own expert; leave expert out", r.Role)
		return TeamRole{}, &res
	case r.Expert == nil:
		res := toolError("the %s role is unavailable: %s", r.Role, firstNonEmptyString(r.Missing, "its expert does not resolve"))
		return TeamRole{}, &res
	case r.Agent == nil:
		res := toolError("the %s role is unavailable: %s", r.Role, firstNonEmptyString(r.Reason, "no agent can take it"))
		return TeamRole{}, &res
	}
	return r, nil
}

// teamStopped refuses a team job's dispatch once wire.MaxTeamFailures
// member tasks failed.
func teamStopped(env *Env, job Job) *Result {
	if job.Team == nil || env.Session.notes.failuresOf(job.JobID) < wire.MaxTeamFailures {
		return nil
	}
	res := toolError("the team stopped: %d member tasks of this request failed. Do not dispatch more; tell the requester what failed and what is left undone", wire.MaxTeamFailures)
	return &res
}

// noteOutcome applies the team guards to a dispatched task's outcome: a
// failed one counts toward the stop, one still running when the wait ended
// posts a timeout notice. It returns a note for the lead ("" for none).
func noteOutcome(ctx context.Context, env *Env, job Job, label string, run wire.AgentRun, waited time.Duration) string {
	if job.Team == nil {
		return ""
	}
	switch {
	case run.Status == "failed" || run.Status == "timed_out" || run.Status == "cancelled":
		n := env.Session.notes.failed(job.JobID)
		if n >= wire.MaxTeamFailures {
			return "this was failed member task " + strconv.Itoa(n) + ": the team stops here (no more dispatches); report to the requester"
		}
		return "failed member tasks so far: " + strconv.Itoa(n) + " (the team stops at " + strconv.Itoa(wire.MaxTeamFailures) + ")"
	case !settled(run.Status) && waited > 0:
		_ = postNotice(ctx, env, job, NoticeTimeout, label+" has not finished after "+humanDuration(waited)+"; the lead goes on and checks it later.", false)
		return "still running: a notice told the room; check it later with room_status"
	}
	return ""
}

// noteRefusal counts a refused dispatch of a team job as a failed member
// task.
func noteRefusal(env *Env, job Job) {
	if job.Team != nil {
		env.Session.notes.failed(job.JobID)
	}
}

func humanDuration(d time.Duration) string {
	if d >= time.Minute {
		m := int(d.Round(time.Minute) / time.Minute)
		if m == 1 {
			return "1 minute"
		}
		return strconv.Itoa(m) + " minutes"
	}
	s := int(d.Round(time.Second) / time.Second)
	return strconv.Itoa(s) + " seconds"
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
