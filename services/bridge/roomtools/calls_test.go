package roomtools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Every tools/call a bound session makes is recorded against its job: the
// tool, the action when the tool has actions (only a listed one), and the
// outcome (ok; refused by the tool or a 4xx; error on a 5xx, a transport
// failure or an unreadable answer), never the arguments. The job's outcome
// takes them once; an unbound session's calls belong to no run.
func TestToolCallsAreRecordedPerJob(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, _ []byte) (int, any) {
		switch {
		case path == "/v1/rooms/room-1/agents":
			return 200, roster
		case strings.HasPrefix(path, "/v1/components?"):
			return 503, map[string]string{"error": "catalog down"}
		case strings.HasPrefix(path, "/v1/content-components/cc_gone?"):
			return 404, map[string]string{"error": "no such component"}
		case strings.HasPrefix(path, "/v1/content-components/cc_garbled?"):
			return 200, "not a component"
		}
		return 404, map[string]string{"error": "no route " + path}
	}}
	srv, token, sess := startServer(t, gw)
	call := func(name string, args map[string]any) {
		t.Helper()
		post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": name, "arguments": args}), nil)
	}

	call("list_room_agents", map[string]any{}) // unbound: no run to record it on
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	call("list_room_agents", map[string]any{})
	call("components", map[string]any{"action": "list"})
	call("components", map[string]any{"action": "get", "id": "cc_gone"})
	call("components", map[string]any{"action": "get", "id": "cc_garbled"})
	call("components", map[string]any{"action": "save", "dir": "SECRET-DIR/../x"})
	call("components", map[string]any{"action": "SECRET-ACTION"})
	call("room_help", map[string]any{"topic": "components"})
	call("nope", map[string]any{"x": "SECRET-ARG"}) // not a tool: not a call of one

	got := srv.TakeCalls("job-1")
	want := []wire.AgentRunToolCall{
		{Tool: "list_room_agents", Outcome: wire.ToolCallOK},
		{Tool: "components", Action: "list", Outcome: wire.ToolCallError},
		{Tool: "components", Action: "get", Outcome: wire.ToolCallRefused},
		{Tool: "components", Action: "get", Outcome: wire.ToolCallError},
		{Tool: "components", Action: "save", Outcome: wire.ToolCallRefused},
		{Tool: "components", Outcome: wire.ToolCallRefused},
		{Tool: "room_help", Outcome: wire.ToolCallOK},
	}
	if fmt.Sprint(got.Calls) != fmt.Sprint(want) || got.CallsDropped != 0 {
		t.Fatalf("calls = %+v\nwant    %+v", got, want)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "cc_") {
		t.Fatalf("the record holds what the agent sent: %s", raw)
	}
	if again := srv.TakeCalls("job-1"); len(again.Calls) != 0 {
		t.Fatalf("taken twice: %+v", again)
	}
	if other := srv.TakeCalls("job-never"); len(other.Calls) != 0 || other.CallsDropped != 0 {
		t.Fatalf("a job with no calls = %+v", other)
	}
}

// A run keeps its first wire.MaxRunToolCalls calls and counts the rest;
// the jobs waiting to be taken are bounded, the oldest dropped first.
func TestToolCallRecordIsBounded(t *testing.T) {
	var r callRecord
	for i := 0; i < wire.MaxRunToolCalls+7; i++ {
		r.add("job-1", wire.AgentRunToolCall{Tool: fmt.Sprintf("t%d", i), Outcome: wire.ToolCallOK})
	}
	got := r.take("job-1")
	if len(got.Calls) != wire.MaxRunToolCalls || got.CallsDropped != 7 || got.Calls[0].Tool != "t0" || got.Calls[wire.MaxRunToolCalls-1].Tool != fmt.Sprintf("t%d", wire.MaxRunToolCalls-1) {
		t.Fatalf("kept %d (first %v), dropped %d", len(got.Calls), got.Calls[0], got.CallsDropped)
	}
	for i := 0; i < maxCallJobs+3; i++ {
		r.add(fmt.Sprintf("job-%d", i), wire.AgentRunToolCall{Tool: "read_room", Outcome: wire.ToolCallOK})
	}
	if len(r.jobs) != maxCallJobs || len(r.order) != maxCallJobs {
		t.Fatalf("%d jobs (%d ordered) waiting; want %d", len(r.jobs), len(r.order), maxCallJobs)
	}
	for i := 0; i < 3; i++ {
		if got := r.take(fmt.Sprintf("job-%d", i)); len(got.Calls) != 0 {
			t.Fatalf("job-%d outlived the bound", i)
		}
	}
	if got := r.take(fmt.Sprintf("job-%d", maxCallJobs+2)); len(got.Calls) != 1 {
		t.Fatalf("the newest job lost its call: %+v", got)
	}
	r.add("", wire.AgentRunToolCall{Tool: "read_room"})
	if _, ok := r.jobs[""]; ok {
		t.Fatal("a call with no job was recorded")
	}
}

// How a result reads as an outcome: a gateway 4xx is a refusal, a 5xx or a
// transport error the platform's failure.
func TestRefusalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&StatusError{Status: 403, Body: []byte(`{"error":"not yours"}`)}, wire.ToolCallRefused},
		{&StatusError{Status: 409, Body: []byte("busy")}, wire.ToolCallRefused},
		{&StatusError{Status: 500, Body: []byte(`{"error":"boom"}`)}, wire.ToolCallError},
		{&StatusError{Status: 502}, wire.ToolCallError},
		{errors.New("connection refused"), wire.ToolCallError},
	} {
		if got := refusal("x", tc.err).outcome(); got != tc.want {
			t.Errorf("%v = %s, want %s", tc.err, got, tc.want)
		}
	}
	if got := (Result{Value: 1}).outcome(); got != wire.ToolCallOK {
		t.Errorf("a value = %s", got)
	}
	if got := toolError("no").outcome(); got != wire.ToolCallRefused {
		t.Errorf("a tool's own refusal = %s", got)
	}
}
