package roomtools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// releaseTeam: jack's agent leads; "reviewer" is staffed on bob/claude
// wearing code-reviewer 1.2.0, "tester" has no agent, "writer" no expert.
func releaseTeam() *Team {
	return &Team{Name: "Release team", Lead: "coordinator", Roles: []TeamRole{
		{Role: "reviewer", Agent: &wire.PlanAgent{OwnerID: "bob", Core: "acme", Provider: "claude", Handle: "bob/claude"},
			Expert: &wire.ContentTeamExpert{ComponentID: "cc_rev", Name: "code-reviewer", Version: "1.2.0"}},
		{Role: "tester", Reason: "a platform agent is not online in this room", Expert: &wire.ContentTeamExpert{ComponentID: "cc_t", Version: "1.0.0"}},
		{Role: "writer", Agent: &wire.PlanAgent{OwnerID: "bob", Provider: "claude", Handle: "bob/claude"}, Missing: "no expert doc-writer that maya may use here"},
	}}
}

// teamGateway answers dispatches with jobs whose runs end as statuses[i]
// (in dispatch order), records notices, and serves the roster.
type teamGateway struct {
	*fakeGateway
	mu       sync.Mutex
	statuses []string
	n        int
}

func newTeamGateway(statuses ...string) *teamGateway {
	tg := &teamGateway{statuses: statuses}
	tg.fakeGateway = &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		switch {
		case path == "/v1/rooms/room-1/agents":
			return 200, roster
		case path == "/v1/rooms/room-1/dispatch":
			tg.mu.Lock()
			tg.n++
			id := "job_child_" + string(rune('0'+tg.n))
			tg.mu.Unlock()
			return 200, wire.DispatchReply{Sequence: int64(50 + tg.n), JobID: id, Depth: 1}
		case strings.HasPrefix(path, "/v1/rooms/room-1/jobs/job_child_"):
			i := int(path[len(path)-1] - '1')
			status := "running"
			if i < len(tg.statuses) {
				status = tg.statuses[i]
			}
			return 200, wire.AgentRun{JobID: path[strings.LastIndex(path, "/")+1:], Status: status, Request: wire.AgentRunRequest{MessageSeq: 51}}
		case path == "/v1/rooms/room-1/agent-activity/messages":
			return 200, map[string]any{}
		case strings.HasPrefix(path, "/v1/rooms/room-1/history"):
			return 200, map[string]any{"messages": []any{}}
		case strings.HasPrefix(path, "/v1/rooms/room-1/artifacts"):
			return 200, map[string]any{"artifacts": []any{}}
		}
		return 404, map[string]any{"error": "no route " + path}
	}}
	return tg
}

func ask(t *testing.T, srv *Server, token string, args map[string]any) (string, bool) {
	t.Helper()
	return toolText(t, post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "ask_agent", "arguments": args}), nil))
}

// list_room_agents reports the bound turn's staffing to a lead: who takes
// each role now, or why nobody can (the lead's instructions carry none of
// it, so a role agent coming or going never churns its session).
func TestListRoomAgentsReportsTeamStaffing(t *testing.T) {
	tg := newTeamGateway()
	srv, token, sess := startServer(t, tg)
	sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", Team: releaseTeam()})
	text, isErr := toolText(t, post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "list_room_agents", "arguments": map[string]any{}}), nil))
	want := `"team":[{"role":"reviewer","agent":"bob/claude"},{"role":"tester","unavailable":"a platform agent is not online in this room"},{"role":"writer","unavailable":"no expert doc-writer that maya may use here"}]`
	if isErr || !strings.Contains(text, want) {
		t.Fatalf("list_room_agents = %s; want %s", text, want)
	}
	sess.Bind(Job{JobID: "job_plain", ChannelID: "ch_general"})
	if text, _ := toolText(t, post(t, srv, token, rpc(2, "tools/call", map[string]any{"name": "list_room_agents", "arguments": map[string]any{}}), nil)); strings.Contains(text, `"team"`) {
		t.Fatalf("a turn without a team reports one: %s", text)
	}
}

// A role dispatches to the role's agent wearing the role's expert, marked
// with the role; an unknown role is refused as uncovered, an unstaffed or
// expert-less role names why, and a role refuses an explicit expert. Raw
// handles still work for a lead.
func TestAskAgentThroughATeamRole(t *testing.T) {
	tg := newTeamGateway("replied")
	srv, token, sess := startServer(t, tg)
	sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30, Team: releaseTeam()})

	text, isErr := ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "review the parser change"})
	if isErr || !strings.Contains(text, `"role":"reviewer"`) || !strings.Contains(text, `"status":"replied"`) {
		t.Fatalf("role dispatch = %s", text)
	}
	calls := tg.callsTo(http.MethodPost, "/v1/rooms/room-1/dispatch")
	var sent wire.DispatchRequest
	if len(calls) != 1 || json.Unmarshal(calls[0].Body, &sent) != nil {
		t.Fatalf("dispatches = %+v", calls)
	}
	if sent.ParentJobID != "job_lead" || sent.Role != "reviewer" || sent.Recipient != (wire.DispatchRecipient{OwnerID: "bob", Core: "acme", Provider: "claude"}) ||
		sent.Expert == nil || *sent.Expert != (wire.ChatExpert{ID: "cc_rev", Version: "1.2.0"}) {
		t.Fatalf("dispatch body = %+v expert %+v", sent, sent.Expert)
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"handle": "designer", "task": "x"}, `"designer" is not a role of your team (roles: reviewer, tester, writer)`},
		{map[string]any{"handle": "tester", "task": "x"}, "the tester role is unavailable: a platform agent is not online"},
		{map[string]any{"handle": "writer", "task": "x"}, "the writer role is unavailable: no expert doc-writer"},
		{map[string]any{"handle": "reviewer", "task": "x", "expert": "cc_other"}, "brings its own expert"},
	} {
		if text, isErr := ask(t, srv, token, tc.args); !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("ask_agent(%v) = %s; want %q", tc.args, text, tc.want)
		}
	}
	if n := len(tg.callsTo(http.MethodPost, "/v1/rooms/room-1/dispatch")); n != 1 {
		t.Fatalf("dispatches after refusals = %d; want 1", n)
	}
	// A lead may still name an agent directly.
	if text, isErr := ask(t, srv, token, map[string]any{"handle": "bob/claude", "task": "x", "wait_seconds": 0}); isErr || strings.Contains(text, `"role"`) {
		t.Fatalf("handle dispatch = %s", text)
	}
}

// After wire.MaxTeamFailures failed member tasks the lead may not dispatch
// again; refused dispatches count. A member still running when the wait
// ends posts a timeout notice in the job's progress session. A job without
// a team is never stopped.
func TestTeamFailureStopAndTimeoutNotice(t *testing.T) {
	tg := newTeamGateway("failed", "timed_out", "running")
	srv, token, sess := startServer(t, tg)
	sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30, Team: releaseTeam()})

	text, _ := ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "a"})
	if !strings.Contains(text, "failed member tasks so far: 1") {
		t.Fatalf("first failure = %s", text)
	}
	if text, _ = ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "b"}); !strings.Contains(text, "failed member tasks so far: 2") {
		t.Fatalf("second failure = %s", text)
	}
	// Third: still running when a 1-second wait ends: a notice, no failure.
	if text, _ = ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "c", "wait_seconds": 1}); !strings.Contains(text, "still running") {
		t.Fatalf("running = %s", text)
	}
	notices := tg.callsTo(http.MethodPost, "/v1/rooms/room-1/agent-activity/messages")
	if len(notices) != 1 {
		t.Fatalf("notices = %d", len(notices))
	}
	var note struct {
		AgentSessionID string `json:"agentSessionId"`
		ChannelID      string `json:"channelId"`
		Messages       []struct {
			Seq     int64          `json:"seq"`
			Payload map[string]any `json:"payload"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(notices[0].Body, &note)
	if note.AgentSessionID != "job-job_lead-progress" || note.ChannelID != "ch_general" || len(note.Messages) != 1 ||
		note.Messages[0].Payload["notice"] != NoticeTimeout || note.Messages[0].Payload["ownerUserId"] != "jack" ||
		note.Messages[0].Payload["jobId"] != "job_lead" || note.Messages[0].Payload["sourceSeq"] != float64(30) ||
		!strings.Contains(note.Messages[0].Payload["text"].(string), "reviewer (bob/claude) has not finished after 1 seconds") {
		t.Fatalf("notice = %+v", note)
	}

	// A refused dispatch is the third failure: the team stops.
	tg.fakeGateway.handle = func(method, path string, body []byte) (int, any) {
		if path == "/v1/rooms/room-1/dispatch" {
			return 403, map[string]string{"code": wire.DispatchRequesterGone, "error": "gone"}
		}
		return 404, nil
	}
	if text, isErr := ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "d"}); !isErr || !strings.Contains(text, "refused") {
		t.Fatalf("refused = %s", text)
	}
	if text, isErr := ask(t, srv, token, map[string]any{"handle": "reviewer", "task": "e"}); !isErr || !strings.Contains(text, "the team stopped: 3 member tasks") {
		t.Fatalf("stopped = %s", text)
	}

	// A new job starts afresh; a job without a team is never stopped.
	sess.Bind(Job{JobID: "job_plain", ChannelID: "ch_general"})
	tg.fakeGateway.handle = func(method, path string, body []byte) (int, any) {
		if path == "/v1/rooms/room-1/agents" {
			return 200, roster
		}
		return 403, map[string]string{"code": wire.DispatchRequesterGone, "error": "gone"}
	}
	for i := 0; i < 4; i++ {
		if text, _ := ask(t, srv, token, map[string]any{"handle": "bob/claude", "task": "x"}); strings.Contains(text, "team stopped") {
			t.Fatalf("plain job stopped: %s", text)
		}
	}
}

// post_progress posts a notice of the bound job (its own session, strictly
// increasing sequences), bounded per job.
func TestPostProgress(t *testing.T) {
	tg := newTeamGateway()
	srv, token, sess := startServer(t, tg)
	call := func(text string) (string, bool) {
		return toolText(t, post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "post_progress", "arguments": map[string]any{"text": text}}), nil))
	}
	if text, isErr := call("Phase 1"); !isErr || !strings.Contains(text, "only while you are answering") {
		t.Fatalf("unbound = %s", text)
	}
	sess.Bind(Job{JobID: "job_lead", ChannelID: "ch_general"})
	if text, isErr := call(""); !isErr || !strings.Contains(text, "required") {
		t.Fatalf("empty = %s", text)
	}
	if text, isErr := call(strings.Repeat("x", 501)); !isErr || !strings.Contains(text, "too long") {
		t.Fatalf("long = %s", text)
	}
	for i := 0; i < maxProgressLines; i++ {
		if text, isErr := call("Phase  1/3:\n review dispatched"); isErr {
			t.Fatalf("line %d = %s", i, text)
		}
	}
	if text, isErr := call("one more"); !isErr || !strings.Contains(text, "at most 20") {
		t.Fatalf("over the limit = %s", text)
	}
	posts := tg.callsTo(http.MethodPost, "/v1/rooms/room-1/agent-activity/messages")
	if len(posts) != maxProgressLines {
		t.Fatalf("posts = %d", len(posts))
	}
	last := int64(0)
	for _, p := range posts {
		var b struct {
			AgentSessionID string `json:"agentSessionId"`
			Messages       []struct {
				Seq     int64          `json:"seq"`
				Payload map[string]any `json:"payload"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(p.Body, &b)
		m := b.Messages[0]
		if b.AgentSessionID != "job-job_lead-progress" || m.Seq <= last || m.Payload["text"] != "Phase 1/3: review dispatched" || m.Payload["notice"] != NoticeProgress {
			t.Fatalf("progress post = %+v", b)
		}
		last = m.Seq
	}
	// A new job gets its own allowance.
	sess.Bind(Job{JobID: "job_next", ChannelID: "ch_general"})
	if text, isErr := call("Phase 1"); isErr {
		t.Fatalf("next job = %s", text)
	}
}
