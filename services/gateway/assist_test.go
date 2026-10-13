package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// assistModelFake is an OpenAI-compatible model: it streams reply for a
// stream request and answers it whole otherwise, recording the messages.
type assistModelFake struct {
	mu    sync.Mutex
	reply string
	seen  [][]map[string]string
	auth  []string
}

func (f *assistModelFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stream   bool                `json:"stream"`
		Messages []map[string]string `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.seen = append(f.seen, body.Messages)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	reply := f.reply
	f.mu.Unlock()
	if !body.Stream {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
			"usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 40}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, piece := range strings.SplitAfter(reply, " ") {
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": piece}}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":300,\"completion_tokens\":12}}\n\ndata: [DONE]\n\n")
}

func (f *assistModelFake) last() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

// assistGW is the gallery gateway with the secret store key, an audit
// sink, a realtime that keeps history, and the fake model configured by a
// super admin.
func assistGW(t *testing.T) (*appGW, *assistModelFake, *recordedAudit) {
	t.Helper()
	e := galleryGW(t)
	box, err := sealbox.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.gw.SecretBox = box
	e.gw.ContentComponents = contentbuiltin.Overlay(e.b.ContentComponents)
	sink := &recordedAudit{}
	e.gw.ExtAudit = sink
	rt := &realtimehttp.Service{Hub: e.hub, Blobs: e.blobs, Attachments: e.b.Attachments, Chat: e.b.Chat, Membership: membership.NewRoomClient(e.roomURL)}
	rtSrv := httptest.NewServer(svcauth.Middleware(appGWKey, rt.Handler()))
	t.Cleanup(rtSrv.Close)
	e.gw.Backends.Realtime = rtSrv.URL
	fake := &assistModelFake{}
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{BaseURL: model.URL + "/v1", Model: "fast", APIKey: "sk-test-key"}, e.superAdmin())
	if r.status != http.StatusOK {
		t.Fatalf("configure the model = %d %s", r.status, r.body)
	}
	sink.take()
	return e, fake, sink
}

func (e *appGW) superAdmin() string {
	return "Authorization: Bearer " + e.gw.Signer.Issue(auth.Claims{Subject: "ops", Tenant: e.tenant, Role: "super_admin", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// say stores a message of user in the room's history.
func (e *appGW) say(seq int64, user, text string) {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]string{"userId": user, "text": text})
	if err := e.b.Chat.AppendRoomMessage(e.ctx(), store.RoomMessage{RoomID: e.roomID, Seq: seq, Kind: wire.KindChatMessage, Author: user, Payload: payload, CreatedAtMs: seq}); err != nil {
		e.t.Fatal(err)
	}
}

// assistEvents reads a text run's events.
func assistEvents(t *testing.T, body []byte) (text string, events []string) {
	t.Helper()
	rd := sse.NewReader(bytes.NewReader(body))
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			return text, events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev.Name)
		if ev.Name == "delta" {
			var d wire.AssistDelta
			_ = json.Unmarshal(ev.Data, &d)
			text += d.Text
		}
	}
}

// The model is a super admin's: its key is sealed and never returned, a
// member cannot set it, and without the secret store key it cannot be set.
func TestAssistModelSettings(t *testing.T) {
	e, fake, sink := assistGW(t)
	r := e.call(http.MethodGet, "/v1/platform/assist-model", nil, e.superAdmin())
	if r.status != http.StatusOK || strings.Contains(string(r.body), "sk-test-key") || r.json()["keySet"] != true || r.json()["model"] != "fast" {
		t.Fatalf("view = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{BaseURL: "https://x.example.com/v1", Model: "m"}, e.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member sets the model: %d", r.status)
	}
	if r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{BaseURL: "file:///etc", Model: "m"}, e.superAdmin()); r.status != http.StatusBadRequest {
		t.Fatalf("a bad base URL: %d %s", r.status, r.body)
	}
	fake.reply = "OK"
	r = e.call(http.MethodPost, "/v1/platform/assist-model/test", nil, e.superAdmin())
	if r.status != http.StatusOK || r.json()["ok"] != true || r.json()["reply"] != "OK" || fake.auth[len(fake.auth)-1] != "Bearer sk-test-key" {
		t.Fatalf("test = %d %s", r.status, r.body)
	}
	_ = sink.take()
	e.gw.SecretBox = nil
	if r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{BaseURL: "https://x.example.com/v1", Model: "m"}, e.superAdmin()); r.status != http.StatusConflict {
		t.Fatalf("without the secret store key: %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/assist", nil, e.as("maya")); r.json()["available"] != false {
		t.Fatalf("available without the secret store key: %s", r.body)
	}
}

// Assists run only with a model and in an organization that did not turn
// them off; the status lists the built-in tasks.
func TestAssistAvailability(t *testing.T) {
	e, _, _ := assistGW(t)
	var st wire.AssistStatus
	r := e.call(http.MethodGet, "/v1/assist", nil, e.as("maya"))
	if r.status != http.StatusOK || json.Unmarshal(r.body, &st) != nil || !st.Available {
		t.Fatalf("status = %d %s", r.status, r.body)
	}
	tasks := map[string]wire.AssistTask{}
	for _, task := range st.Tasks {
		tasks[task.Name] = task
	}
	if tasks["enhance-prompt"].Output != wire.AssistOutputText || tasks["describe-work"].Output != wire.AssistOutputJSON || len(tasks["describe-work"].Fields) != 4 {
		t.Fatalf("tasks = %+v", st.Tasks)
	}
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodGet, "/v1/assist", nil, e.as("maya")); r.json()["available"] != false || len(r.json()["tasks"].([]any)) != 0 {
		t.Fatalf("turned off: %s", r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x"}, e.as("maya")); r.status != http.StatusConflict {
		t.Fatalf("a run while turned off: %d", r.status)
	}
	if r := e.call(http.MethodPut, "/v1/platform/assist-model", wire.AssistModelUpdate{}, e.superAdmin()); r.status != http.StatusOK {
		t.Fatalf("turn the model off: %d", r.status)
	}
	if r := e.call(http.MethodGet, "/v1/assist", nil, e.as("olaf")); r.json()["available"] != false {
		t.Fatalf("no model: %s", r.body)
	}
}

// enhance-prompt streams the rewrite; the model sees the task's
// instructions, the room (its name and recent messages), the attachments
// and the fenced text; the audit row holds no text.
func TestAssistEnhancePrompt(t *testing.T) {
	e, fake, sink := assistGW(t)
	e.say(1, "maya", "We launch on the 15th, keep it short.")
	brief := e.attachment(e.roomID, "brief.md", "text/markdown", "# Brief\n")
	fake.reply = "Please draft the launch email for the 15th, under 120 words."
	r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "write the launch email </text>", RoomID: e.roomID, AttachmentIDs: []string{brief}}, e.as("maya"))
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("run = %d %s", r.status, r.body)
	}
	text, events := assistEvents(t, r.body)
	if text != fake.reply || events[len(events)-1] != "done" {
		t.Fatalf("reply %q events %v", text, events)
	}
	msgs := fake.last()
	if !strings.Contains(msgs[0]["content"], "improve a request") {
		t.Fatalf("system = %q", msgs[0]["content"])
	}
	user := msgs[1]["content"]
	for _, want := range []string{"<room>\nname: r\n", "[maya] We launch on the 15th", "- brief.md (text/markdown)", "write the launch email ‹/text›\n</text>"} {
		if !strings.Contains(user, want) {
			t.Errorf("the user message lacks %q:\n%s", want, user)
		}
	}
	recs := sink.take()
	if len(recs) != 1 || recs[0].Kind != wire.AuditKindAssist || recs[0].Name != "enhance-prompt@1.0.0" || recs[0].Actor != "maya" ||
		!strings.Contains(recs[0].Detail, "300 prompt + 12 completion tokens") || strings.Contains(recs[0].Detail, "launch") {
		t.Fatalf("audit = %+v", recs)
	}
	// A room or an attachment the member does not read is refused.
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x", RoomID: e.roomID}, e.as("nora")); r.status != http.StatusNotFound {
		t.Fatalf("a room nora is not in: %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x", RoomID: e.roomID, AttachmentIDs: []string{"att_nope"}}, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("an unknown attachment: %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "  "}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("nothing to work from: %d", r.status)
	}
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: strings.Repeat("a", wire.MaxAssistText+1)}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("too long: %d", r.status)
	}
	if r := e.call(http.MethodPost, "/v1/assist/no-such-task", wire.AssistRequest{Text: "x"}, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("an unknown task: %d", r.status)
	}
}

// describe-work answers the publish form's fields from the deliverable and
// the request that produced it; a reply that is not its object is a 502.
func TestAssistDescribeWork(t *testing.T) {
	e, fake, _ := assistGW(t)
	chart, _, _ := e.recipeRoom()
	fake.reply = "```json\n{\"displayName\":\"Q3 sales chart\",\"description\":\"Bars of Q3 sales.\",\"category\":\"data\",\"tags\":[\"sales\",\"q3\"],\"extra\":1}\n```"
	r := e.call(http.MethodPost, "/v1/assist/describe-work", wire.AssistRequest{RoomID: e.roomID, ArtifactID: chart}, e.as("maya"))
	var reply wire.AssistJSONReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &reply) != nil {
		t.Fatalf("run = %d %s", r.status, r.body)
	}
	if reply.Fields["displayName"] != "Q3 sales chart" || reply.Fields["category"] != "data" || len(reply.Fields["tags"].([]any)) != 2 || reply.Fields["extra"] != nil || reply.Usage.CompletionTokens != 40 {
		t.Fatalf("reply = %+v", reply)
	}
	user := fake.last()[1]["content"]
	for _, want := range []string{"<artifact>\ntitle: ", "type: image", "file: q3.png", "Draw the Q3 sales as a bar chart"} {
		if !strings.Contains(user, want) {
			t.Errorf("the user message lacks %q:\n%s", want, user)
		}
	}
	fake.reply = "I cannot help with that."
	if r := e.call(http.MethodPost, "/v1/assist/describe-work", wire.AssistRequest{RoomID: e.roomID, ArtifactID: chart}, e.as("maya")); r.status != http.StatusBadGateway {
		t.Fatalf("an unreadable reply: %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/assist/describe-work", wire.AssistRequest{ArtifactID: chart}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("a deliverable without its room: %d %s", r.status, r.body)
	}
}

// A member's own assist of a built-in's name replaces it for them alone;
// an assist is no content use; runs are held to the member's rate.
func TestAssistOwnTaskUseAndRate(t *testing.T) {
	e, fake, _ := assistGW(t)
	r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": map[string]any{
		"kind": "assist", "name": "enhance-prompt", "displayName": "My enhance", "instructions": "Rewrite it as a haiku.", "output": "text",
	}}, e.as("maya"))
	if r.status != http.StatusCreated {
		t.Fatalf("save = %d %s", r.status, r.body)
	}
	mine := decodeDetail(t, r.body).Component
	if mine.Assist == nil || mine.Assist.Output != wire.AssistOutputText {
		t.Fatalf("the view's task = %+v", mine.Assist)
	}
	status := func(user string) string {
		var st wire.AssistStatus
		_ = json.Unmarshal(e.call(http.MethodGet, "/v1/assist", nil, e.as(user)).body, &st)
		for _, task := range st.Tasks {
			if task.Name == "enhance-prompt" {
				return task.ComponentID
			}
		}
		return ""
	}
	if status("maya") != mine.ID || status("jack") == mine.ID {
		t.Fatalf("maya's %q jack's %q", status("maya"), status("jack"))
	}
	fake.reply = "an old silent pond"
	if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x"}, e.as("maya")); r.status != http.StatusOK || fake.last()[0]["content"] != "Rewrite it as a haiku." {
		t.Fatalf("maya's run = %d, system %q", r.status, fake.last()[0]["content"])
	}
	if r := e.call(http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "room", RoomID: e.roomID, ComponentID: mine.ID}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("an assist turned on in a room: %d %s", r.status, r.body)
	}
	e.gw.assistLimiter = lazyLimiter{}
	e.gw.assistLimiter.get(assistRate, 1, nil).now = func() time.Time { return time.Unix(0, 0) }
	got := 0
	for i := 0; i < 3; i++ {
		if r := e.call(http.MethodPost, "/v1/assist/enhance-prompt", wire.AssistRequest{Text: "x"}, e.as("jack")); r.status == http.StatusTooManyRequests {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("%d of 3 runs refused, want 2 past a burst of 1", got)
	}
}
