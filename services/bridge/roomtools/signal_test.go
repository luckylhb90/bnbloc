package roomtools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// signal sends the bound job's event through the gateway's signals door
// (the job id, never a sender: the platform decides who it is sent as); it
// refuses a platform type and works only while a request is being answered.
func TestSignalToolSendsForTheBoundJob(t *testing.T) {
	var got wire.SignalRequest
	var path string
	gw := &fakeGateway{handle: func(method, p string, body []byte) (int, any) {
		path = p
		_ = json.Unmarshal(body, &got)
		return 202, wire.MemberEventReply{Event: wire.CloudEvent{ID: "evt_1", Type: got.Type}, Triggered: 1}
	}}
	srv, token, sess := startServer(t, gw)
	call := func(args map[string]any) (string, bool) {
		t.Helper()
		return toolText(t, post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "signal", "arguments": args}), nil))
	}

	if text, isErr := call(map[string]any{"action": "send", "type": "draft.ready"}); !isErr || !strings.Contains(text, "only while you are answering") {
		t.Fatalf("unbound = %v %s", isErr, text)
	}
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	if text, isErr := call(map[string]any{"action": "send", "type": "room.job.replied"}); !isErr || !strings.Contains(text, "platform's") {
		t.Fatalf("platform type = %v %s", isErr, text)
	}
	if path != "" {
		t.Fatalf("a refused signal reached the gateway: %s", path)
	}
	text, isErr := call(map[string]any{"action": "send", "type": "draft.ready", "subject": "draft.md", "data": map[string]any{"pages": 3}})
	if isErr || !strings.Contains(text, `"tasksRun":1`) {
		t.Fatalf("send = %v %s", isErr, text)
	}
	if path != "/v1/rooms/room-1/signals" || got.JobID != "job-1" || got.ScheduleID != "" || got.Type != "draft.ready" || got.Subject != "draft.md" || string(got.Data) != `{"pages":3}` {
		t.Fatalf("sent %s %+v", path, got)
	}
}
