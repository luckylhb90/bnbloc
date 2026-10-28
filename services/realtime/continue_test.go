package realtimehttp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func (f *dispatchFixture) cont(caller string, in wire.ContinueRequest) (int, wire.ContinueReply) {
	f.t.Helper()
	buf, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/internal/v1/rooms/room-1/continue", strings.NewReader(string(buf)))
	req.Header.Set("X-Bnbloc-User", caller)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out wire.ContinueReply
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func continueFrom(job string) wire.ContinueRequest {
	return wire.ContinueRequest{Job: job, Component: "conn", Text: "The change is allowed: run it again.", Key: "h1"}
}

func (f *dispatchFixture) feedLen() int {
	f.t.Helper()
	msgs, err := f.chat.RoomFeed(dctx(), "room-1", 0, 20)
	if err != nil {
		f.t.Fatal(err)
	}
	return len(msgs)
}

// The message goes to the agent of the job the card belongs to, as the member,
// marked as automatic, in that job's channel. Another agent of the same member
// in the room never receives it.
func TestContinuePostsToTheHeldJobsAgentAsTheMember(t *testing.T) {
	f := newDispatchFixture(t)
	// maya has two agents in the room; the held job ran on the second one.
	f.parent("job_first", func(j *store.AgentJob) { j.OwnerUserID, j.Provider, j.Status = "maya", "claude", store.AgentJobReplied })
	held := f.parent("job_held", func(j *store.AgentJob) {
		j.OwnerUserID, j.Provider, j.Core, j.Status = "maya", "codex", "acme", store.AgentJobReplied
	})
	code, out := f.cont("maya", continueFrom(held.JobID))
	if code != http.StatusOK || !out.Posted || out.Sequence == 0 {
		t.Fatalf("continue = %d %+v", code, out)
	}
	msgs, _ := f.chat.RoomFeed(dctx(), "room-1", 0, 20)
	if len(msgs) != 1 {
		t.Fatalf("feed has %d messages, want the one continue", len(msgs))
	}
	var payload struct {
		UserID        string              `json:"userId"`
		Text          string              `json:"text"`
		Recipient     map[string]string   `json:"recipient"`
		ContinuedFrom wire.ContinueMarker `json:"continuedFrom"`
	}
	if err := json.Unmarshal(msgs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != "maya" || payload.Recipient["ownerId"] != "maya" || payload.Recipient["provider"] != "codex" || payload.Recipient["core"] != "acme" {
		t.Fatalf("payload = %+v; want maya's message to her codex (the held job's agent), not her claude", payload)
	}
	if payload.ContinuedFrom != (wire.ContinueMarker{JobID: held.JobID, Component: "conn"}) {
		t.Fatalf("continuedFrom = %+v", payload.ContinuedFrom)
	}
	if msgs[0].ChannelID != "ch_general" {
		t.Fatalf("posted in %q, want the job's channel", msgs[0].ChannelID)
	}
}

// The same decision twice posts once.
func TestContinueIsOncePerKey(t *testing.T) {
	f := newDispatchFixture(t)
	held := f.parent("job_held", func(j *store.AgentJob) { j.OwnerUserID, j.Status = "maya", store.AgentJobReplied })
	first, _ := f.cont("maya", continueFrom(held.JobID))
	second, out := f.cont("maya", continueFrom(held.JobID))
	if first != http.StatusOK || second != http.StatusOK || !out.Posted {
		t.Fatalf("answers = %d %d %+v", first, second, out)
	}
	if n := f.feedLen(); n != 1 {
		t.Fatalf("feed has %d messages after two clicks, want 1", n)
	}
}

// What it will not post: a job that is not the member's, of another room,
// still running or gone, and text that is empty or too long. Each says why and
// posts nothing.
func TestContinuePostsNothingForAJobThatIsNotTheMembersToEnd(t *testing.T) {
	f := newDispatchFixture(t)
	running := f.parent("job_running", func(j *store.AgentJob) { j.OwnerUserID = "maya" })
	others := f.parent("job_jacks", func(j *store.AgentJob) { j.RequesterUserID, j.Status = "jack", store.AgentJobReplied })
	elsewhere := f.parent("job_elsewhere", func(j *store.AgentJob) { j.RoomID, j.Status = "room-2", store.AgentJobReplied })
	tooLong := continueFrom(others.JobID)
	tooLong.Text = strings.Repeat("x", 501)
	for name, c := range map[string]struct {
		caller string
		in     wire.ContinueRequest
	}{
		"still running":    {"maya", continueFrom(running.JobID)},
		"another's job":    {"maya", continueFrom(others.JobID)},
		"another room":     {"maya", continueFrom(elsewhere.JobID)},
		"gone":             {"maya", continueFrom("job_gone")},
		"no text":          {"maya", wire.ContinueRequest{Job: others.JobID, Component: "conn", Key: "h1"}},
		"text too long":    {"maya", tooLong},
		"key not a token":  {"maya", wire.ContinueRequest{Job: others.JobID, Component: "conn", Text: "go", Key: "a b"}},
		"component absent": {"maya", wire.ContinueRequest{Job: others.JobID, Text: "go", Key: "h1"}},
	} {
		code, out := f.cont(c.caller, c.in)
		if code != http.StatusOK || out.Posted || out.Why == "" {
			t.Errorf("%s: %d %+v, want 200 with nothing posted and a reason", name, code, out)
		}
	}
	if n := f.feedLen(); n != 0 {
		t.Fatalf("feed has %d messages, want none", n)
	}
}

// Only a member of the room may use it, and only as themself.
func TestContinueNeedsMembership(t *testing.T) {
	f := newDispatchFixture(t)
	held := f.parent("job_held", func(j *store.AgentJob) { j.OwnerUserID, j.Status = "maya", store.AgentJobReplied })
	if code, _ := f.cont("stranger", continueFrom(held.JobID)); code != http.StatusForbidden && code != http.StatusOK {
		t.Fatalf("a stranger got %d", code)
	}
	if n := f.feedLen(); n != 0 {
		t.Fatalf("a stranger's continue posted %d messages", n)
	}
}
