package decision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// realReply is the provider's documented reply to the triage example
// (a choice, a predicate and a score), verbatim in shape.
const realReply = `{
  "model": "decision-model-x",
  "answers": [
    {"type": "choice", "name": "department", "choice": "billing",
     "probabilities": [{"value": "billing", "probability": 0.97}, {"value": "technical", "probability": 0.03}, {"value": "sales", "probability": 0.0}],
     "confidence": 0.95},
    {"type": "predicate", "name": "urgent", "probability": 0.9},
    {"type": "score", "name": "frustration", "score": 1.96,
     "probabilities": [{"value": 0, "label": "calm", "probability": 0.0}, {"value": 1, "label": "annoyed", "probability": 0.04}, {"value": 2, "label": "angry", "probability": 0.96}],
     "confidence": 0.94}
  ],
  "usage": {"input_tokens": 410, "input_tokens_details": {"cached_tokens": 0}, "output_tokens": 70, "total_tokens": 480}
}`

// triage is the documented request.
func triage() wire.DecisionRequest {
	return wire.DecisionRequest{
		Input: json.RawMessage(`"Help! My payouts have been failing for 3 days and nobody answers my emails."`),
		Questions: []wire.DecisionQuestion{
			{Type: wire.DecisionChoice, Name: "department", Instructions: "Which team should handle this ticket?", Choices: []wire.DecisionChoiceOption{
				{Value: "billing", Description: "Payments, invoices, refunds"}, {Value: "technical", Description: "Bugs, outages, integrations"}, {Value: "sales", Description: "Pricing, upgrades, new accounts"}}},
			{Type: wire.DecisionPredicate, Name: "urgent", Instructions: "Does this ticket need a reply today?"},
			{Type: wire.DecisionScore, Name: "frustration", Instructions: "How frustrated is the customer?", Levels: []wire.DecisionLevel{{Label: "calm"}, {Label: "annoyed"}, {Label: "angry"}}},
		},
	}
}

// fakeProvider refuses what the real proxy refuses (a missing bearer, a
// model it does not serve, an unknown question type) and records the body.
type fakeProvider struct {
	mu     sync.Mutex
	bodies []map[string]any
	auths  []string
	reply  string
}

func (f *fakeProvider) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		reply := f.reply
		f.mu.Unlock()
		switch {
		case r.URL.Path != "/v1/decisions" || r.Method != http.MethodPost:
			http.NotFound(w, r)
		case r.Header.Get("Authorization") == "":
			http.Error(w, `{"error":{"message":"Authentication Error, No api key passed in."}}`, http.StatusUnauthorized)
		case body["model"] != "jdg-a" && body["model"] != "jdg-b":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid model name passed in model=` + strings.TrimSpace(body["model"].(string)) + `","type":"invalid_request_error"}}`))
		default:
			_, _ = w.Write([]byte(reply))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func settingsWith(t *testing.T) *Settings {
	t.Helper()
	box, err := sealbox.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Settings{Store: store.NewMemoryPlatformSettings(), Box: box, Now: func() int64 { return 1 }}
}

// The documented request reaches the provider in its shape, with the sealed
// key and the asked model; the documented reply comes back normalized, in
// question order; an unknown model falls back to the default.
func TestDecideAgainstTheDocumentedShapes(t *testing.T) {
	f := &fakeProvider{reply: realReply}
	srv := f.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Models: []string{"jdg-a", "jdg-b"}, APIKey: "sk-test"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, ok, err := s.Provider(ctx, "acme")
	if err != nil || !ok {
		t.Fatalf("provider = %v %v", ok, err)
	}
	req := triage()
	req.Model = "jdg-b"
	v, err := Validate(req)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := p.Decide(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	sent := f.bodies[0]
	if sent["model"] != "jdg-b" || f.auths[0] != "Bearer sk-test" || !strings.HasPrefix(sent["input"].(string), "Help!") {
		t.Fatalf("sent = %v auth = %q", sent, f.auths[0])
	}
	qs := sent["questions"].([]any)
	if len(qs) != 3 || qs[0].(map[string]any)["choices"] == nil || qs[2].(map[string]any)["levels"] == nil {
		t.Fatalf("questions = %v", qs)
	}
	if len(reply.Answers) != 3 || reply.Usage.InputTokens != 410 || reply.Usage.OutputTokens != 70 {
		t.Fatalf("reply = %+v", reply)
	}
	dep, urg, fr := reply.Answers[0], reply.Answers[1], reply.Answers[2]
	if dep.Choice != "billing" || len(dep.Probabilities) != 3 || dep.Probabilities[0].Value != "billing" || *dep.Confidence != 0.95 {
		t.Fatalf("department = %+v", dep)
	}
	if urg.Name != "urgent" || *urg.Probability != 0.9 {
		t.Fatalf("urgent = %+v", urg)
	}
	if *fr.Score != 1.96 || len(fr.Probabilities) != 3 || fr.Probabilities[2].Value != 2 || fr.Probabilities[2].Label != "angry" {
		t.Fatalf("frustration = %+v", fr)
	}
	req.Model = "not-served"
	v, _ = Validate(req)
	if _, err := p.Decide(ctx, v); err != nil || f.bodies[1]["model"] != "jdg-a" {
		t.Fatalf("unknown model: err %v, sent %v; want the default", err, f.bodies[1]["model"])
	}
}

// A provider's refusal keeps its status and message; a reply that does not
// answer the questions (count or type) is unreadable, never half-matched.
func TestDecideRefusalsAndUnreadableReplies(t *testing.T) {
	f := &fakeProvider{reply: `{"answers":[{"type":"predicate","probability":0.5}],"usage":{}}`}
	srv := f.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Model: "jdg-a"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Provider(ctx, "")
	v, _ := Validate(triage())
	var pe *ProviderError
	if _, err := p.Decide(ctx, v); !errors.As(err, &pe) || pe.Status != http.StatusUnauthorized {
		t.Fatalf("no key = %v; want the provider's 401", err)
	}
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Model: "jdg-a", APIKey: "k"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.Provider(ctx, "")
	if _, err := p.Decide(ctx, v); !errors.Is(err, ErrProviderUnreachable) {
		t.Fatalf("one answer for three questions = %v", err)
	}
	f.reply = `{"answers":[{"type":"predicate","probability":0.5},{"type":"predicate","probability":0.5},{"type":"score","score":1}]}`
	if _, err := p.Decide(ctx, v); !errors.Is(err, ErrProviderUnreachable) || !strings.Contains(err.Error(), "the question a choice") {
		t.Fatalf("type mismatch = %v", err)
	}
}

// A caller-auth provider forwards the pinned bearer and nothing else; with
// none pinned (an agent acting for someone else) it refuses before any call.
func TestCallerAuth(t *testing.T) {
	f := &fakeProvider{reply: realReply}
	srv := f.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Model: "jdg-a", Auth: AuthCaller, APIKey: "never-sent"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Provider(ctx, "")
	v, _ := Validate(triage())
	if _, err := p.Decide(ctx, v); !errors.Is(err, ErrNoCallerAuth) || len(f.bodies) != 0 {
		t.Fatalf("unpinned = %v (calls %d)", err, len(f.bodies))
	}
	if _, err := p.WithCallerAuth("Bearer member-session").Decide(ctx, v); err != nil || f.auths[0] != "Bearer member-session" {
		t.Fatalf("pinned = %v auth %q", err, f.auths)
	}
}

// Every bound is checked before a provider is called, and the refusal
// names the field.
func TestValidate(t *testing.T) {
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nrest"))
	ok := triage()
	ok.Input = json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"which?"},{"type":"input_image","image_url":"` + png + `"}]}]`)
	v, err := Validate(ok)
	if err != nil || v.Images() != 1 || len(v.Messages) != 1 {
		t.Fatalf("messages input = %+v %v", v, err)
	}
	cases := []struct {
		name   string
		mutate func(*wire.DecisionRequest)
		want   string
	}{
		{"no input", func(r *wire.DecisionRequest) { r.Input = nil }, "input: the thing to judge"},
		{"blank input", func(r *wire.DecisionRequest) { r.Input = json.RawMessage(`"  "`) }, "input: the thing to judge"},
		{"input object", func(r *wire.DecisionRequest) { r.Input = json.RawMessage(`{"a":1}`) }, "a string or a list"},
		{"assistant role", func(r *wire.DecisionRequest) {
			r.Input = json.RawMessage(`[{"role":"assistant","content":[{"type":"input_text","text":"x"}]}]`)
		}, `input[0].role`},
		{"image link", func(r *wire.DecisionRequest) {
			r.Input = json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"https://x.example/a.png"}]}]`)
		}, "links are not fetched"},
		{"not an image", func(r *wire.DecisionRequest) {
			r.Input = json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("<svg/>")) + `"}]}]`)
		}, "a PNG, JPEG, WebP or GIF"},
		{"no questions", func(r *wire.DecisionRequest) { r.Questions = nil }, "questions: 1 to 128"},
		{"too many", func(r *wire.DecisionRequest) {
			for len(r.Questions) <= wire.MaxDecisionQuestions {
				r.Questions = append(r.Questions, wire.DecisionQuestion{Type: wire.DecisionPredicate, Instructions: "x"})
			}
		}, "questions: 1 to 128"},
		{"duplicate name", func(r *wire.DecisionRequest) { r.Questions[1].Name = "department" }, `"department" is used twice`},
		{"no instructions", func(r *wire.DecisionRequest) { r.Questions[1].Instructions = " " }, "questions[1].instructions"},
		{"one choice", func(r *wire.DecisionRequest) { r.Questions[0].Choices = r.Questions[0].Choices[:1] }, "questions[0].choices: 2 to 255"},
		{"duplicate choice", func(r *wire.DecisionRequest) { r.Questions[0].Choices[1].Value = "billing" }, `"billing" is listed twice`},
		{"eleven levels", func(r *wire.DecisionRequest) {
			for len(r.Questions[2].Levels) <= wire.MaxDecisionLevels {
				r.Questions[2].Levels = append(r.Questions[2].Levels, wire.DecisionLevel{Label: "x"})
			}
		}, "questions[2].levels: 2 to 10"},
		{"predicate with choices", func(r *wire.DecisionRequest) { r.Questions[1].Choices = r.Questions[0].Choices }, "a predicate takes no"},
		{"unknown type", func(r *wire.DecisionRequest) { r.Questions[1].Type = "noul" }, "questions[1].type must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := triage()
			c.mutate(&r)
			_, err := Validate(r)
			var ie InvalidError
			if !errors.As(err, &ie) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// An organization's own provider replaces the platform's while its host is
// allowed; a kind outside the registry is refused at save.
func TestOrgProviderAndKinds(t *testing.T) {
	plat, org := &fakeProvider{reply: realReply}, &fakeProvider{reply: realReply}
	ps, os := plat.start(t), org.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	orgHost := strings.TrimPrefix(os.URL, "http://")
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: ps.URL + "/v1", Model: "jdg-a", APIKey: "p", OrgHosts: []string{orgHost}}, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "acme", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: os.URL + "/v1", Model: "jdg-b", APIKey: "o"}, "admin"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Provider(ctx, "acme")
	v, _ := Validate(triage())
	if _, err := p.Decide(ctx, v); err != nil || len(org.bodies) != 1 || len(plat.bodies) != 0 || org.auths[0] != "Bearer o" {
		t.Fatalf("org provider: err %v org %d platform %d", err, len(org.bodies), len(plat.bodies))
	}
	p, _, _ = s.Provider(ctx, "other")
	if _, err := p.Decide(ctx, v); err != nil || len(plat.bodies) != 1 {
		t.Fatalf("platform provider: err %v platform %d", err, len(plat.bodies))
	}
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: "systemone", BaseURL: ps.URL, Model: "x"}, "root"); err == nil || !strings.Contains(err.Error(), "registered adapters") {
		t.Fatalf("unknown kind = %v", err)
	}
}
