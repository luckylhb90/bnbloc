package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// toolModel is an OpenAI-compatible model that answers a scripted turn per
// call: tool calls, then a final text. It records what it was sent.
type toolModel struct {
	mu    sync.Mutex
	turns []any
	seen  [][]map[string]any
	tools []string
}

func (m *toolModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []map[string]any `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, body.Messages)
	m.tools = m.tools[:0]
	for _, t := range body.Tools {
		m.tools = append(m.tools, t.Function.Name)
	}
	var msg any = map[string]any{"content": "done"}
	if len(m.turns) > 0 {
		msg, m.turns = m.turns[0], m.turns[1:]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
}

func calls(cs ...[2]string) map[string]any {
	var out []any
	for i, c := range cs {
		out = append(out, map[string]any{"id": fmt.Sprintf("c%d", i), "type": "function", "function": map[string]any{"name": c[0], "arguments": c[1]}})
	}
	return map[string]any{"content": "", "tool_calls": out}
}

// scheduleGW is the app gateway with the model, a notifier and the outbox.
func scheduleGW(t *testing.T) (*appGW, *toolModel, *store.MemoryNotifications) {
	t.Helper()
	e, outbox, _ := notifyGW(t)
	box, err := sealbox.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.gw.SecretBox = box
	e.gw.TenantSettings = e.b.Tenant.GetTenant
	model := &toolModel{}
	srv := httptest.NewServer(model)
	t.Cleanup(srv.Close)
	if r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{BaseURL: srv.URL + "/v1", Model: "fast", APIKey: "k"}, e.superAdmin()); r.status != http.StatusOK {
		t.Fatalf("configure the model = %d %s", r.status, r.body)
	}
	return e, model, outbox
}

// runAI calls the internal route as realtime would.
func (e *appGW) runAI(task string, dry bool) wire.ScheduleRunReply {
	e.t.Helper()
	sc := wire.RoomSchedule{ScheduleID: "sch_1", TenantID: e.tenant, RoomID: e.roomID, CreatedBy: "jack", Text: task,
		Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, Timezone: "UTC"}
	body, _ := json.Marshal(wire.ScheduleRunRequest{Schedule: sc, SlotMs: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC).UnixMilli(), DryRun: dry})
	req, _ := svcauth.Key(appGWKey).NewRequest(context.Background(), http.MethodPost, e.srv.URL+"/internal/v1/schedule-runs", bytes.NewReader(body), svcauth.ServiceSubject("realtime"), e.tenant)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out wire.ScheduleRunReply
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		e.t.Fatalf("schedule run = %d", resp.StatusCode)
	}
	return out
}

// An ai run reads an app's data as the owner, notifies whoever the model
// decides, and ends with its summary; the task is the model's input, the
// tools are the registry's. The same slot run again notifies nobody twice.
// A dry run reads for real and sends nothing.
func TestAIScheduleRun(t *testing.T) {
	e, model, outbox := scheduleGW(t)
	maya := e.ticketBase("maya", e.appA)
	e.call(http.MethodPut, maya+"~app/c/reports/w42", map[string]any{"doc": map[string]any{"who": "maya", "log": "shipped"}})

	script := func() {
		model.turns = []any{
			calls([2]string{"app_data", `{"action":"list"}`}),
			calls([2]string{"app_data", fmt.Sprintf(`{"action":"find","artifactId":%q,"collection":"reports"}`, e.appA)}),
			calls([2]string{"notify", `{"to":["nora"],"title":"Weekly report due","text":"Please fill it in"}`}),
			map[string]any{"content": "Reminded nora, who has not filled in."},
		}
	}
	script()
	reply := e.runAI("Every Thursday remind whoever has not filled in the weekly report.", false)
	if reply.Status != wire.ScheduleRunOK || reply.Summary != "Reminded nora, who has not filled in." || len(reply.Actions) != 3 {
		t.Fatalf("run = %+v", reply)
	}
	first := model.seen[0]
	if !strings.Contains(fmt.Sprint(first[0]["content"]), "scheduled task") || !strings.Contains(fmt.Sprint(first[1]["content"]), "weekly report") {
		t.Fatalf("the model was sent %v", first)
	}
	for _, want := range []string{"app_data", "notify", "post_message", "ask_agent", "list_agents"} {
		if !strings.Contains(strings.Join(model.tools, ","), want) {
			t.Fatalf("tools offered = %v", model.tools)
		}
	}
	// The find's answer reached the model as fenced data.
	var found string
	for _, m := range model.seen[2] {
		if m["role"] == "tool" {
			found = fmt.Sprint(m["content"])
		}
	}
	if !strings.Contains(found, "shipped") || !strings.Contains(found, "untrusted-content") {
		t.Fatalf("the model read %q", found)
	}
	got := queued(t, outbox)
	if len(got) != 1 || got[0].Type != wire.NotificationAppNotice || strings.Join(recipientsOf(got[0]), ",") != "nora" || got[0].Title != "Weekly report due in "+got[0].RoomName {
		t.Fatalf("notices = %+v", got)
	}
	// The same slot again (a re-claimed run): no second notice.
	script()
	e.runAI("Every Thursday remind whoever has not filled in the weekly report.", false)
	if n := len(queued(t, outbox)); n != 1 {
		t.Fatalf("a re-run of the slot queued %d notices", n)
	}

	// Dry: reads for real, sends nothing, says what it would do.
	before := len(queued(t, outbox))
	model.turns = []any{calls([2]string{"notify", `{"to":"room","title":"Due"}`}), map[string]any{"content": "Would remind the room."}}
	dry := e.runAI("remind the room", true)
	if dry.Status != wire.ScheduleRunOK || len(dry.Actions) != 1 || !dry.Actions[0].DryRun || !strings.Contains(dry.Actions[0].Detail, "would notify 2 people") {
		t.Fatalf("dry run = %+v", dry)
	}
	if n := len(queued(t, outbox)); n != before {
		t.Fatal("a dry run sent a notice")
	}
}

// A run is bounded by steps, not a clock; a notice to an address off the
// deployment is refused to the model; AI off fails the run with a sentence.
func TestAIScheduleRunBounds(t *testing.T) {
	e, model, _ := scheduleGW(t)
	var loop []any
	for i := 0; i < maxScheduleSteps+2; i++ {
		loop = append(loop, calls([2]string{"list_agents", `{}`}))
	}
	model.turns = loop
	if r := e.runAI("loop", false); r.Status != wire.ScheduleRunError || !strings.Contains(r.Error, "steps") {
		t.Fatalf("a looping model = %+v", r)
	}
	model.turns = []any{calls([2]string{"notify", `{"to":"room","title":"x","url":"https://evil.example/"}`}), map[string]any{"content": "ok"}}
	e.runAI("phish", false)
	var refused string
	for _, m := range model.seen[len(model.seen)-1] {
		if m["role"] == "tool" {
			refused = fmt.Sprint(m["content"])
		}
	}
	if !strings.Contains(refused, "error: url must be a link on this deployment") {
		t.Fatalf("the model was answered %q", refused)
	}
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r := e.runAI("x", false); r.Status != wire.ScheduleRunError || !strings.Contains(r.Error, "AI is not available") {
		t.Fatalf("AI off = %+v", r)
	}
}

// Only realtime may hand the gateway a run.
func TestAIScheduleRunRouteIsRealtimeOnly(t *testing.T) {
	e, _, _ := scheduleGW(t)
	body := []byte(`{}`)
	for _, subject := range []string{"jack", svcauth.ServiceSubject("room")} {
		req, _ := svcauth.Key(appGWKey).NewRequest(context.Background(), http.MethodPost, e.srv.URL+"/internal/v1/schedule-runs", bytes.NewReader(body), subject, e.tenant)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s handing a run = %d", subject, resp.StatusCode)
		}
	}
	resp, err := http.Post(e.srv.URL+"/internal/v1/schedule-runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned run = %d", resp.StatusCode)
	}
}
