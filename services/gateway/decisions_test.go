package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// decisionFake is a decision provider speaking the OpenAI Decisions shape:
// it refuses a call without a bearer (as the real proxy does), answers one
// predicate per question, and records what it was sent.
type decisionFake struct {
	mu     sync.Mutex
	auths  []string
	bodies []string
	// probability is every predicate's answer (0: 0.8).
	probability float64
}

func (f *decisionFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.auths, f.bodies = append(f.auths, r.Header.Get("Authorization")), append(f.bodies, string(raw))
	p := f.probability
	f.mu.Unlock()
	if p == 0 {
		p = 0.8
	}
	if r.URL.Path != "/v1/decisions" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") == "" {
		http.Error(w, `{"error":{"message":"No api key passed in."}}`, http.StatusUnauthorized)
		return
	}
	var body struct {
		Questions []wire.DecisionQuestion `json:"questions"`
	}
	_ = json.Unmarshal(raw, &body)
	answers := []any{}
	for _, q := range body.Questions {
		answers = append(answers, map[string]any{"type": "predicate", "name": q.Name, "probability": p})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "upstream-model", "answers": answers, "usage": map[string]any{"input_tokens": 12, "output_tokens": 3}})
}

// decisionGW is the gallery gateway with the decision provider configured
// by a super admin (the configuration row drained).
func decisionGW(t *testing.T, fake *decisionFake, auth string) *appGW {
	t.Helper()
	e, _, sink := assistGW(t)
	defer func() { sink.take() }()
	configureDecisions(t, e, fake, auth)
	return e
}

// configureDecisions sets fake as the platform's decision provider.
func configureDecisions(t *testing.T, e *appGW, fake *decisionFake, auth string) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	r := e.call(http.MethodPut, "/v1/platform/decisions", wire.MediaProviderUpdate{
		Kind: "openai-decisions", BaseURL: srv.URL + "/v1", Models: []string{"judge-a", "judge-b"}, Auth: auth, APIKey: "sk-decide"}, e.superAdmin())
	if r.status != http.StatusOK {
		t.Fatalf("configure = %d %s", r.status, r.body)
	}
}

func ask(name, question string) wire.DecisionRequest {
	return wire.DecisionRequest{Input: json.RawMessage(`"payouts failing for 3 days, nobody answers"`),
		Questions: []wire.DecisionQuestion{{Type: wire.DecisionPredicate, Name: name, Instructions: question}}}
}

// A member asks: the provider gets the sealed key and the asked model, the
// member gets the answers, the audit row holds counts and tokens but never
// the input, the question or the answer; the key is never returned.
func TestDecide(t *testing.T) {
	fake := &decisionFake{}
	e := decisionGW(t, fake, "")
	sink := e.gw.ExtAudit.(*recordedAudit)

	r := e.call(http.MethodGet, "/v1/decisions", nil, e.as("maya"))
	var caps wire.DecisionCapability
	if err := json.Unmarshal([]byte(r.body), &caps); err != nil || !caps.Available || len(caps.Models) != 2 {
		t.Fatalf("capability = %s %v", r.body, err)
	}
	req := ask("urgent", "Does this need a reply today?")
	req.Model = "judge-b"
	r = e.call(http.MethodPost, "/v1/decisions", req, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("decide = %d %s", r.status, r.body)
	}
	var reply wire.DecisionReply
	if err := json.Unmarshal([]byte(r.body), &reply); err != nil || len(reply.Answers) != 1 || *reply.Answers[0].Probability != 0.8 || reply.Answers[0].Name != "urgent" {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	if fake.auths[0] != "Bearer sk-decide" || !strings.Contains(fake.bodies[0], `"model":"judge-b"`) {
		t.Fatalf("sent auth %q body %s", fake.auths[0], fake.bodies[0])
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "decide" || !strings.Contains(rows[0].Detail, "1 questions") || !strings.Contains(rows[0].Detail, "12 input + 3 output tokens") {
		t.Fatalf("audit = %+v", rows)
	}
	for _, leak := range []string{"payouts", "reply today", "0.8"} {
		if strings.Contains(rows[0].Detail, leak) {
			t.Fatalf("the audit row leaks %q: %s", leak, rows[0].Detail)
		}
	}
	if r := e.call(http.MethodGet, "/v1/platform/decisions", nil, e.superAdmin()); strings.Contains(string(r.body), "sk-decide") || !strings.Contains(string(r.body), `"keySet":true`) {
		t.Fatalf("view = %s", r.body)
	}
}

// A bad request is refused before any provider call, naming the field; an
// organization with AI off gets 409; a member cannot set the provider.
func TestDecideRefusals(t *testing.T) {
	fake := &decisionFake{}
	e := decisionGW(t, fake, "")
	bad := ask("x", "q")
	bad.Questions[0].Type = "noul"
	if r := e.call(http.MethodPost, "/v1/decisions", bad, e.as("maya")); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "questions[0].type") {
		t.Fatalf("bad type = %d %s", r.status, r.body)
	}
	if len(fake.bodies) != 0 {
		t.Fatalf("the provider was called for a bad request")
	}
	if r := e.call(http.MethodPut, "/v1/platform/decisions", wire.MediaProviderUpdate{Kind: "openai-decisions", BaseURL: "https://x.example/v1", Model: "m"}, e.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member set the provider: %d", r.status)
	}
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodPost, "/v1/decisions", ask("x", "q"), e.as("maya")); r.status != http.StatusConflict {
		t.Fatalf("AI off = %d %s", r.status, r.body)
	}
}

// A caller-auth provider forwards the member's own session; the Test
// button proves the provider with one real predicate.
func TestDecideCallerAuthAndTest(t *testing.T) {
	fake := &decisionFake{}
	e := decisionGW(t, fake, "caller")
	if r := e.call(http.MethodPost, "/v1/decisions", ask("x", "q"), e.as("maya")); r.status != http.StatusOK {
		t.Fatalf("decide = %d %s", r.status, r.body)
	}
	if fake.auths[0] == "" || fake.auths[0] == "Bearer sk-decide" {
		t.Fatalf("forwarded %q; want the member's own session", fake.auths[0])
	}
	r := e.call(http.MethodPost, "/v1/platform/decisions/test", nil, e.superAdmin())
	var out wire.MediaProviderTest
	if err := json.Unmarshal([]byte(r.body), &out); err != nil || !out.OK || !strings.Contains(out.Reply, "p(yes) = 0.80") {
		t.Fatalf("test = %s %v", r.body, err)
	}
}

// POST /v1/systemone is the same call in the SystemOne format: the
// provider gets the list format, the caller the answers keyed by name,
// with the same audit row; a bad map names the field before any call; an
// agent reaches it through a job grant like the list route.
func TestSystemOne(t *testing.T) {
	fake := &decisionFake{}
	e := decisionGW(t, fake, "")
	sink := e.gw.ExtAudit.(*recordedAudit)
	body := json.RawMessage(`{"model":"judge-b","state":{"ticket":"payouts failing"},"questions":{"urgent":{"type":"noul","instructions":"Reply today?"},"vip":{"type":"noul","instructions":"A key account?"}}}`)
	r := e.call(http.MethodPost, "/v1/systemone", body, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("systemone = %d %s", r.status, r.body)
	}
	var reply wire.SystemOneReply
	if err := json.Unmarshal(r.body, &reply); err != nil || len(reply.Answers) != 2 || reply.Answers["vip"].Type != "noul" || *reply.Answers["urgent"].Noul != 0.8 || reply.Usage.InputTokens != 12 {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	if !strings.Contains(fake.bodies[0], `"input":"{\"ticket\":\"payouts failing\"}"`) || !strings.Contains(fake.bodies[0], `"type":"predicate","name":"urgent"`) || !strings.Contains(fake.bodies[0], `"model":"judge-b"`) {
		t.Fatalf("sent = %s", fake.bodies[0])
	}
	if rows := sink.take(); len(rows) != 1 || rows[0].Name != "decide" || !strings.Contains(rows[0].Detail, "2 questions") {
		t.Fatalf("audit = %+v", rows)
	}
	if r := e.call(http.MethodPost, "/v1/systemone", json.RawMessage(`{"state":"s","questions":{"a":{"type":"choice","instructions":"x","criteria":["p"]}}}`), e.as("maya")); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "questions.a.criteria") {
		t.Fatalf("bad map = %d %s", r.status, r.body)
	}
	if len(fake.bodies) != 1 {
		t.Fatalf("the provider was called for a bad request")
	}
}

// Decisions have a rate of their own, sized for a step loop: a member who
// spent their assist rate can still ask decisions, and a decision burst
// past its own bucket is refused 429.
func TestDecisionRateIsItsOwn(t *testing.T) {
	fake := &decisionFake{}
	e := decisionGW(t, fake, "")
	e.gw.assistLimiter = lazyLimiter{}
	e.gw.assistLimiter.get(assistRate, 1, nil).now = func() time.Time { return time.Unix(0, 0) }
	e.gw.assistLimiter.get(assistRate, 1, nil).allow(e.tenant + "/maya") // the assist bucket is empty
	for i := 0; i < 3; i++ {
		if r := e.call(http.MethodPost, "/v1/decisions", ask("x", "q"), e.as("maya")); r.status != http.StatusOK {
			t.Fatalf("decision %d with the assist rate spent = %d %s", i, r.status, r.body)
		}
	}
	e.gw.decisionLimiter = lazyLimiter{}
	e.gw.decisionLimiter.get(decisionRate, 2, nil).now = func() time.Time { return time.Unix(0, 0) }
	refused := 0
	for i := 0; i < 4; i++ {
		if r := e.call(http.MethodPost, "/v1/decisions", ask("x", "q"), e.as("maya")); r.status == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != 2 {
		t.Fatalf("%d of 4 refused, want 2 past a burst of 2", refused)
	}
}
