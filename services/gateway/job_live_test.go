package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A job's live view through the gateway: the tool's frame is admitted only
// under the job's grant from the bridge running it, reaches the room's
// realtime, and a member reads it back through the gateway's room routes;
// the owner's input comes back in the tool's next reply.
func TestLiveFrameUnderJobGrant(t *testing.T) {
	e := newAppGW(t)
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-maya", OwnerUserID: "maya", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, ChannelID: "ch-1", MessageSeq: 4, RequesterUserID: "maya", OwnerUserID: "maya", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-maya", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	bridge := "Authorization: Bearer " + e.session("maya")
	frame := wire.LiveFramePost{Run: "run-1", Seq: 1, MimeType: "image/jpeg", Data: []byte("jpeg-bytes"), Width: 800, Height: 600, Title: "Expenses"}

	// No grant, or another member's bridge: refused.
	if r := e.call(http.MethodPost, "/v1/live", frame, bridge); r.status != http.StatusBadRequest {
		t.Fatalf("frame without a job = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/live?room="+e.roomID+"&job=job-1", frame, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusForbidden {
		t.Fatalf("another bridge's frame = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/live?room="+e.roomID+"&job=job-1", frame, bridge); r.status != http.StatusOK {
		t.Fatalf("frame = %d %s", r.status, r.body)
	}
	member := "Authorization: Bearer " + e.session("jack")
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/jobs/job-1/live/frame", nil, member); r.status != http.StatusOK || string(r.body) != "jpeg-bytes" || r.header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("member reads the frame = %d %q %s", r.status, r.body, r.header.Get("Content-Type"))
	}
	owner := "Authorization: Bearer " + e.session("maya")
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/jobs/job-1/live/control", wire.LiveControlPost{Take: true}, owner); r.status != http.StatusOK {
		t.Fatalf("take control = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/jobs/job-1/live/input", wire.LiveInputPost{Events: []wire.LiveInput{{Type: wire.LiveInputText, Text: "hello"}}}, owner); r.status != http.StatusNoContent {
		t.Fatalf("input = %d %s", r.status, r.body)
	}
	r := e.call(http.MethodPost, "/v1/live?room="+e.roomID+"&job=job-1", wire.LiveFramePost{}, bridge)
	var reply wire.LiveFrameReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &reply) != nil || reply.Controller != "maya" || len(reply.Input) != 1 || reply.Input[0].Text != "hello" {
		t.Fatalf("reply = %d %s", r.status, r.body)
	}

	// The turn ended: the tool's frames are refused.
	if _, err := e.jobs.CancelAgentJob(e.ctx(), store.CancelRequest{JobID: "job-1", RoomID: e.roomID, RequestedByUser: "maya", NowMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodPost, "/v1/live?room="+e.roomID+"&job=job-1", frame, bridge); r.status != http.StatusConflict || !strings.Contains(string(r.body), "not running") {
		t.Fatalf("frame after the turn = %d %s", r.status, r.body)
	}
}
