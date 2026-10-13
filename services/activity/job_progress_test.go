package activity

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The bridge holding a live job relays its turn's activity to the job's
// room as a transient event, held to the wire's bounds; nobody else can,
// and a finished job gets none.
func TestJobProgressReachesTheJobsRoom(t *testing.T) {
	srv, pub, _ := newJobServer(t)
	steps := []map[string]any{{"kind": "bogus", "title": "dropped", "atMs": 1}}
	for i := 0; i < wire.MaxJobActivity+2; i++ {
		steps = append(steps, map[string]any{"kind": "tool", "title": strings.Repeat("x", wire.MaxJobActivityTitle+10), "atMs": i})
	}
	resp := post(t, srv, "/v1/bridge/jobs/job-1/progress", "maya", map[string]any{"bridgeId": "bridge-maya", "startedAtMs": 5, "activity": steps})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(pub.events) != 1 {
		t.Fatalf("status = %d, events = %d", resp.StatusCode, len(pub.events))
	}
	ev := pub.events[0]
	p, ok := ev.Payload.(wire.AgentJobProgress)
	if ev.Kind != wire.EventAgentJobProgress || ev.RoomID != "room-1" || ev.ChannelID != "chan-1" || !ok || p.JobID != "job-1" || p.StartedAtMs != 5 {
		t.Fatalf("event = %+v", ev)
	}
	if len(p.Activity) != wire.MaxJobActivity || p.Activity[0].AtMs != 2 || len([]rune(p.Activity[0].Title)) != wire.MaxJobActivityTitle {
		t.Fatalf("activity = %+v", p.Activity)
	}

	foreign := post(t, srv, "/v1/bridge/jobs/job-1/progress", "intruder", map[string]any{"bridgeId": "bridge-maya", "activity": steps})
	foreign.Body.Close()
	if foreign.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign progress = %d", foreign.StatusCode)
	}

	done := post(t, srv, "/v1/bridge/jobs/job-1/status", "maya", map[string]any{"bridgeId": "bridge-maya", "status": "replied"})
	done.Body.Close()
	late := post(t, srv, "/v1/bridge/jobs/job-1/progress", "maya", map[string]any{"bridgeId": "bridge-maya", "activity": steps})
	late.Body.Close()
	if late.StatusCode != http.StatusConflict || len(pub.events) != 2 {
		t.Fatalf("progress after the job ended = %d, events = %d", late.StatusCode, len(pub.events))
	}
}
