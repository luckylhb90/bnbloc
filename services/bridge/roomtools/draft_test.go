package roomtools

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// draftGateway serves a room whose roster is rosterAgents, stages drafts,
// and answers dispatches with dispatchStatus (200 = accepted).
func draftGateway(rosterAgents []wire.RosterAgent, dispatchStatus int) *fakeGateway {
	return &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		switch {
		case path == "/v1/rooms/room-1/agents":
			return 200, map[string]any{"agents": rosterAgents}
		case strings.HasPrefix(path, "/v1/content-drafts?"):
			return 201, wire.ContentDraft{ID: "cd_1", Kind: "expert", Name: "reviewer", DisplayName: "Reviewer", Version: "1.0.0", ExpiresAtMs: 99}
		case path == "/v1/rooms/room-1/dispatch":
			if dispatchStatus != 200 {
				return dispatchStatus, map[string]string{"code": wire.DispatchFanOutExceeded, "error": "this job already dispatched 8 tasks (the limit)"}
			}
			return 200, wire.DispatchReply{Sequence: 61, JobID: "job_try", Depth: 1}
		case path == "/v1/rooms/room-1/jobs/job_try":
			return 200, wire.AgentRun{JobID: "job_try", Status: "replied", Request: wire.AgentRunRequest{MessageSeq: 61}, Reply: &wire.AgentRunReply{Text: "Issues: 2 risky clauses."}}
		case strings.HasPrefix(path, "/v1/rooms/room-1/history"):
			return 200, map[string]any{"messages": []any{}}
		case strings.HasPrefix(path, "/v1/rooms/room-1/artifacts"):
			return 200, map[string]any{"artifacts": []any{}}
		}
		return 404, map[string]any{"error": "no route " + path}
	}}
}

// draftRoster: the session's own agent (jack/codex), bob's and the
// requester maya's own agent.
var draftRoster = []wire.RosterAgent{
	{OwnerUserID: "jack", Core: "acme", Provider: "codex"},
	{OwnerUserID: "bob", Core: "acme", Provider: "claude"},
	{OwnerUserID: "maya", Core: "acme", Provider: "opencode"},
}

var reviewerDraft = fakeWorkspace{
	"drafts/reviewer/component.json":  `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0","displayName":"Reviewer"}`,
	"drafts/reviewer/INSTRUCTIONS.md": "Review contracts clause by clause.\n",
	"notes.md":                        "not part of the draft",
}

func tryDraftCall(t *testing.T, srv *Server, token string, args map[string]any) (string, bool) {
	t.Helper()
	return toolText(t, post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "try_draft", "arguments": args}), nil))
}

// A draft is validated, staged under the bound job's grant (only the
// draft's files), and dispatched through the dispatch door to the
// requester's own other agent wearing it; the reply comes back like
// ask_agent's, marked as a try.
func TestTryDraftStagesAndDispatches(t *testing.T) {
	gw := draftGateway(draftRoster, 200)
	srv, token, sess := startServer(t, gw)
	sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30, Workspace: reviewerDraft})

	text, isErr := tryDraftCall(t, srv, token, map[string]any{"dir": "drafts/reviewer", "task": "Review this NDA: ..."})
	if isErr || !strings.Contains(text, `"status":"replied"`) || !strings.Contains(text, `"agent":"maya/opencode"`) ||
		!strings.Contains(text, `"draft":{`) || !strings.Contains(text, "nothing was saved") {
		t.Fatalf("try = %s", text)
	}
	staged := gw.callsTo("POST", "/v1/content-drafts")
	if len(staged) != 1 {
		t.Fatalf("staged = %+v", staged)
	}
	u, _ := url.Parse(staged[0].Path)
	if u.Query().Get("job") != "job_lead" || u.Query().Get("room") != "room-1" {
		t.Fatalf("stage grant = %s", staged[0].Path)
	}
	var raw struct {
		ContentType string
		Data        string
	}
	_ = json.Unmarshal(staged[0].Body, &raw)
	data, _ := base64.StdEncoding.DecodeString(raw.Data)
	b, err := tarbundle.Parse(data, "", tarbundle.ContentLimits)
	if err != nil || raw.ContentType != "application/x-tar" || strings.Join(b.Paths(), ",") != "INSTRUCTIONS.md,component.json" {
		t.Fatalf("staged bundle = %v %q %v", err, raw.ContentType, b)
	}
	dispatches := gw.callsTo("POST", "/v1/rooms/room-1/dispatch")
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %+v", dispatches)
	}
	var req wire.DispatchRequest
	_ = json.Unmarshal(dispatches[0].Body, &req)
	if req.ParentJobID != "job_lead" || req.Draft != "cd_1" || req.Expert != nil || req.Recipient.OwnerID != "maya" || req.Recipient.Provider != "opencode" || req.Text != "Review this NDA: ..." {
		t.Fatalf("dispatch = %+v", req)
	}

	// A named agent is used as named.
	if text, isErr := tryDraftCall(t, srv, token, map[string]any{"dir": "drafts/reviewer", "task": "t", "handle": "bob/claude"}); isErr || !strings.Contains(text, `"agent":"bob/claude"`) {
		t.Fatalf("named try = %s", text)
	}
}

// What the validator refuses, a non-wearable kind, the session's own agent
// or a room without another agent stops before anything is staged; a
// dispatch guard's refusal comes back as a tool error and counts against a
// team's request.
func TestTryDraftRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ws     fakeWorkspace
		roster []wire.RosterAgent
		args   map[string]any
		want   string
	}{
		{"invalid draft", fakeWorkspace{"drafts/x/component.json": `{"kind":"bnbloc.expert.package/v1","name":"x","version":"1.0.0","displayName":"X"}`},
			draftRoster, map[string]any{"dir": "drafts/x", "task": "t"}, "not valid yet"},
		{"prompt pack", fakeWorkspace{"drafts/p/component.json": `{"kind":"bnbloc.prompt.package/v1","name":"p","version":"1.0.0","displayName":"P","prompts":[{"id":"a","title":"A","body":"hi"}]}`},
			draftRoster, map[string]any{"dir": "drafts/p", "task": "t"}, "only an expert or a skill pack"},
		{"no task", reviewerDraft, draftRoster, map[string]any{"dir": "drafts/reviewer", "task": " "}, "task is required"},
		{"yourself", reviewerDraft, draftRoster, map[string]any{"dir": "drafts/reviewer", "task": "t", "handle": "jack/codex"}, "is you"},
		{"no other agent", reviewerDraft, draftRoster[:1], map[string]any{"dir": "drafts/reviewer", "task": "t"}, "no other agent in this room"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := draftGateway(tc.roster, 200)
			srv, token, sess := startServer(t, gw)
			sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30, Workspace: tc.ws})
			text, isErr := tryDraftCall(t, srv, token, tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Fatalf("try = %v %s; want an error with %q", isErr, text, tc.want)
			}
			if n := len(gw.callsTo("POST", "/v1/content-drafts")) + len(gw.callsTo("POST", "/v1/rooms/room-1/dispatch")); n != 0 {
				t.Fatalf("%d calls went out for a refused try", n)
			}
		})
	}

	// Not answering a request: nothing to stage under.
	gw := draftGateway(draftRoster, 200)
	srv, token, _ := startServer(t, gw)
	if text, isErr := tryDraftCall(t, srv, token, map[string]any{"dir": "drafts/reviewer", "task": "t"}); !isErr || !strings.Contains(text, "only while you are answering") {
		t.Fatalf("unbound try = %s", text)
	}

	// The dispatch guards apply: a refused dispatch is a tool error naming
	// the refusal, and it counts against a team lead's request.
	gw = draftGateway(draftRoster, 409)
	srv, token, sess := startServer(t, gw)
	sess.Bind(Job{JobID: "job_lead", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30, Workspace: reviewerDraft, Team: releaseTeam()})
	text, isErr := tryDraftCall(t, srv, token, map[string]any{"dir": "drafts/reviewer", "task": "t"})
	if !isErr || !strings.Contains(text, "8 tasks") {
		t.Fatalf("refused dispatch = %v %s", isErr, text)
	}
	if n := sess.notes.failuresOf("job_lead"); n != 1 {
		t.Fatalf("team failures = %d; want the refusal counted", n)
	}
}
