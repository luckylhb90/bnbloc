package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// toolUseRuns stores n runs created at 1000+i ms, each with the room tool
// calls calls(i) recorded as a bridge reports them (nil: a run without the
// key, as an older bridge's).
func toolUseRuns(t *testing.T, n int, calls func(i int) *wire.AgentRunTools) (*Service, *store.MemoryBridgeJobs) {
	t.Helper()
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	jobs := store.NewMemoryBridgeJobs()
	for i := 0; i < n; i++ {
		j := store.AgentJob{JobID: fmt.Sprintf("j-%05d", i), RoomID: "room-1", MessageSeq: int64(i + 1), RequesterUserID: "maya",
			OwnerUserID: "maya", Core: "c1", Provider: "codex", CreatedAtMs: int64(1000 + i)}
		if tools := calls(i); tools != nil {
			j.DiagnosticsJSON, _ = json.Marshal(map[string]any{wire.RunDiagTools: tools})
		}
		if _, err := jobs.PutAgentJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: membership.NewStatic(), BridgeJobs: jobs, Runs: jobs,
		Now: func() time.Time { return time.UnixMilli(1_000_000) }}
	return svc, jobs
}

func getToolUse(t *testing.T, svc *Service, path string) (int, wire.RoomToolUseReply) {
	t.Helper()
	code, body := getRaw(t, svc, path, "carol")
	var out wire.RoomToolUseReply
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
	}
	return code, out
}

// The tool use read sums every run's recorded calls by tool and action over
// the runs created in the range, across pages of runs, most called first;
// calls a run did not keep are counted as dropped, and a run without the
// record counts as a run with no calls.
func TestToolUseSumsCallsAndRefusalsPerTool(t *testing.T) {
	n := store.MaxAgentRunsPage*2 + 5 // more runs than one page
	svc, _ := toolUseRuns(t, n, func(i int) *wire.AgentRunTools {
		switch i % 3 {
		case 0:
			return &wire.AgentRunTools{MCP: []string{"search"}, Calls: []wire.AgentRunToolCall{
				{Tool: "components", Action: "save", Outcome: wire.ToolCallRefused},
				{Tool: "components", Action: "save", Outcome: wire.ToolCallOK},
				{Tool: "read_room", Outcome: wire.ToolCallOK},
			}, CallsDropped: 1}
		case 1:
			return &wire.AgentRunTools{Calls: []wire.AgentRunToolCall{
				{Tool: "read_room", Outcome: wire.ToolCallError},
				{Tool: "components", Action: "list", Outcome: wire.ToolCallOK},
			}}
		}
		return nil
	})
	code, out := getToolUse(t, svc, fmt.Sprintf("/v1/tenant/jobs/tool-calls?from=1000&to=%d", 1000+n))
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	thirds := (n + 2) / 3 // i%3 == 0
	ones := (n + 1) / 3   // i%3 == 1
	want := []wire.RoomToolUse{
		{Tool: "components", Action: "save", Calls: 2 * thirds, Refused: thirds}, // 138
		{Tool: "read_room", Calls: thirds + ones, Errors: ones},                  // 137
		{Tool: "components", Action: "list", Calls: ones},
	}
	if out.Runs != n || out.Truncated || out.Dropped != thirds || fmt.Sprint(out.Tools) != fmt.Sprint(want) {
		t.Fatalf("tool use = %+v\nwant tools %+v", out, want)
	}
	// The range bounds the runs read: [from, to).
	if _, out := getToolUse(t, svc, "/v1/tenant/jobs/tool-calls?from=1001&to=1003"); out.Runs != 2 ||
		fmt.Sprint(out.Tools) != fmt.Sprint([]wire.RoomToolUse{{Tool: "components", Action: "list", Calls: 1}, {Tool: "read_room", Calls: 1, Errors: 1}}) {
		t.Fatalf("narrowed = %+v", out)
	}
	// Without a range: the last seven days up to now.
	if _, out := getToolUse(t, svc, "/v1/tenant/jobs/tool-calls"); out.ToMs != 1_000_001 || out.FromMs != out.ToMs-7*24*3600*1000 || out.Runs != n {
		t.Fatalf("default range = %+v", out)
	}
	for _, bad := range []string{"?from=5000&to=4000", "?from=x"} {
		if code, _ := getToolUse(t, svc, "/v1/tenant/jobs/tool-calls"+bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}

// A read stops at wire.MaxToolUseRuns runs and says so, naming the oldest
// run it counted: never a silent cut.
func TestToolUseSaysWhenItStopped(t *testing.T) {
	n := wire.MaxToolUseRuns + 3
	svc, _ := toolUseRuns(t, n, func(int) *wire.AgentRunTools {
		return &wire.AgentRunTools{Calls: []wire.AgentRunToolCall{{Tool: "read_room", Outcome: wire.ToolCallOK}}}
	})
	_, out := getToolUse(t, svc, "/v1/tenant/jobs/tool-calls?from=1")
	if out.Runs != wire.MaxToolUseRuns || !out.Truncated || out.OldestMs != int64(1000+3) || len(out.Tools) != 1 || out.Tools[0].Calls != wire.MaxToolUseRuns {
		t.Fatalf("truncated read = runs %d truncated %v oldest %d tools %+v", out.Runs, out.Truncated, out.OldestMs, out.Tools)
	}
}

// What the bridge reports on a job's outcome is what the read counts: the
// calls ride the status route's diagnostics into the run record.
func TestToolUseReadsTheBridgesReport(t *testing.T) {
	svc, _ := toolUseRuns(t, 1, func(int) *wire.AgentRunTools { return nil })
	post := func(path, body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-Bnbloc-User", "maya")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/v1/bridge/heartbeat", `{"bridgeId":"b-maya","agents":[{"core":"c1","provider":"codex"}]}`)
	post("/v1/bridge/jobs/claim", `{"bridgeId":"b-maya","maxJobs":5}`)
	post("/v1/bridge/jobs/j-00000/status", `{"bridgeId":"b-maya","status":"replied","diagnostics":{"tools":{"calls":[{"tool":"room_setup","action":"add_schedule","outcome":"refused"}]}}}`)
	_, out := getToolUse(t, svc, "/v1/tenant/jobs/tool-calls?from=1")
	if fmt.Sprint(out.Tools) != fmt.Sprint([]wire.RoomToolUse{{Tool: "room_setup", Action: "add_schedule", Calls: 1, Refused: 1}}) {
		t.Fatalf("tool use = %+v", out)
	}
}
