package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An agent reads the room's memory through the job it runs, for the job's
// requester: the active brief and the ranked notes, never without a job and
// never through a bridge that does not run it; members write notes through the
// room's own routes.
func TestRoomMemoryReadThroughAJob(t *testing.T) {
	e := newAppGW(t)
	maya := "Authorization: Bearer " + e.session("maya")
	notes := "/v1/rooms/" + e.roomID + "/notes"
	for _, n := range []wire.NoteCreateRequest{
		{Type: wire.NoteBrief, Title: "Project", Body: "The weekly report."},
		{Type: wire.NoteMemory, Title: "dated"},
		{Type: wire.NoteKnowledge, Title: "decision"},
	} {
		if r := e.call(http.MethodPost, notes, n, maya); r.status != http.StatusCreated {
			t.Fatalf("create %s = %d %s", n.Title, r.status, r.body)
		}
	}
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	bridge := "Authorization: Bearer " + e.session("jack")
	path := "/v1/rooms/" + e.roomID + "/memory"

	r := e.call(http.MethodGet, path+"?job=job-1", nil, bridge)
	var titles []string
	mem := r.json()
	for _, n := range mem["notes"].([]any) {
		titles = append(titles, n.(map[string]any)["title"].(string))
	}
	brief, _ := mem["brief"].(map[string]any)
	if r.status != http.StatusOK || brief["title"] != "Project" || len(titles) != 2 || titles[0] != "decision" || titles[1] != "dated" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the agent's memory = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, path, nil, bridge); r.status != http.StatusBadRequest {
		t.Fatalf("without a job = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, path+"?job=job-1", nil, maya); r.status != http.StatusForbidden {
		t.Fatalf("a bridge that does not run the job = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, path+"?job=job-missing", nil, bridge); r.status == http.StatusOK {
		t.Fatalf("an unknown job read the memory: %s", r.body)
	}
}
