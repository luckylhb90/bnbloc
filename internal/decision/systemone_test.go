package decision

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// systemOneTriage is the documented SystemOne request, with a JSON-object
// state and object instructions and criteria (what a browser agent sends).
const systemOneTriage = `{
  "model": "jdg-b",
  "state": {"page": {"title": "Payouts", "text": "Help! My payouts have been failing for 3 days."}},
  "questions": {
    "department": {"type": "choice", "instructions": "Which team should handle this ticket?",
      "criteria": {"billing": "Payments, invoices, refunds", "technical": {"area": "Bugs, outages"}, "sales": "Pricing"}},
    "urgent": {"type": "noul", "instructions": {"goal": "Does this ticket need a reply today?"},
      "criteria": {"true": "a reply today", "false": "it can wait"}},
    "frustration": {"type": "score", "instructions": "How frustrated is the customer?", "criteria": ["calm", "annoyed", "angry"]}
  }
}`

// A SystemOne request is the same call: its questions keep their written
// order and become the list format (noul a predicate whose criteria join
// its instructions, a choice's labels its choices, a score's levels), its
// object state and object texts are judged as their JSON text, and the
// reply comes back keyed by name in the SystemOne shape.
func TestSystemOneRoundTrip(t *testing.T) {
	var in wire.SystemOneRequest
	if err := json.Unmarshal([]byte(systemOneTriage), &in); err != nil {
		t.Fatal(err)
	}
	req, err := FromSystemOne(in)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "jdg-b" || !strings.HasPrefix(req.Text, `{"page":{"title":"Payouts"`) {
		t.Fatalf("model %q input %q", req.Model, req.Text)
	}
	qs := req.Questions
	if len(qs) != 3 || qs[0].Name != "department" || qs[1].Name != "urgent" || qs[2].Name != "frustration" {
		t.Fatalf("order = %+v", qs)
	}
	if qs[0].Type != wire.DecisionChoice || len(qs[0].Choices) != 3 || qs[0].Choices[1].Value != "technical" || qs[0].Choices[1].Description != `{"area":"Bugs, outages"}` {
		t.Fatalf("choice = %+v", qs[0])
	}
	if qs[1].Type != wire.DecisionPredicate || qs[1].Instructions != `{"goal":"Does this ticket need a reply today?"}`+"\n\nTrue means: a reply today\nFalse means: it can wait" {
		t.Fatalf("noul = %q", qs[1].Instructions)
	}
	if qs[2].Type != wire.DecisionScore || len(qs[2].Levels) != 3 || qs[2].Levels[2].Label != "angry" {
		t.Fatalf("score = %+v", qs[2])
	}

	f := &fakeProvider{reply: realReply}
	srv := f.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Models: []string{"jdg-a", "jdg-b"}, APIKey: "k"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Provider(ctx, "")
	reply, err := p.Decide(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	out := ToSystemOne(reply)
	raw, _ := json.Marshal(out)
	dep, urg, fr := out.Answers["department"], out.Answers["urgent"], out.Answers["frustration"]
	if dep.Type != "choice" || dep.Choice != "billing" || dep.Probabilities["billing"] != 0.97 || *dep.Confidence != 0.95 {
		t.Fatalf("department = %s", raw)
	}
	if urg.Type != wire.SystemOneNoul || *urg.Noul != 0.9 {
		t.Fatalf("urgent = %s", raw)
	}
	if fr.Type != "score" || *fr.Score != 1.96 || fr.Probabilities["2"] != 0.96 || fr.Legend["2"] != "angry" {
		t.Fatalf("frustration = %s", raw)
	}
	if out.Usage.InputTokens != 410 || !strings.Contains(string(raw), `"input_tokens":410`) {
		t.Fatalf("usage = %s", raw)
	}
}

// A model's refusal of one question is that question's answer, in either
// format; the other answers stand.
func TestRefusalIsAnAnswer(t *testing.T) {
	f := &fakeProvider{reply: `{"answers":[{"type":"refusal"},{"type":"predicate","probability":0.7},{"type":"score","score":1}]}`}
	srv := f.start(t)
	s := settingsWith(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAIDecisions, BaseURL: srv.URL + "/v1", Model: "jdg-a", APIKey: "k"}, "root"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Provider(ctx, "")
	v, _ := Validate(triage())
	reply, err := p.Decide(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if a := reply.Answers[0]; a.Type != wire.DecisionRefusal || a.Name != "department" || a.Choice != "" {
		t.Fatalf("refusal = %+v", a)
	}
	if *reply.Answers[1].Probability != 0.7 {
		t.Fatalf("the other answers = %+v", reply.Answers)
	}
	if got := ToSystemOne(reply).Answers["department"]; got.Type != wire.DecisionRefusal {
		t.Fatalf("systemone refusal = %+v", got)
	}
}

// The SystemOne refusals name the field; the list rules (Validate) apply
// to what it converts to.
func TestSystemOneRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"no state":        {`{"questions":{"a":{"type":"noul","instructions":"x"}}}`, "state: the thing to judge"},
		"list questions":  {`{"state":"s","questions":[{"type":"noul"}]}`, "questions: a map"},
		"no questions":    {`{"state":"s","questions":{}}`, "questions: 1 to"},
		"predicate type":  {`{"state":"s","questions":{"a":{"type":"predicate","instructions":"x"}}}`, `questions.a.type must be "noul"`},
		"criteria list":   {`{"state":"s","questions":{"a":{"type":"choice","instructions":"x","criteria":["p","q"]}}}`, "questions.a.criteria: a map"},
		"score map":       {`{"state":"s","questions":{"a":{"type":"score","instructions":"x","criteria":{"p":"q"}}}}`, "questions.a.criteria: a list"},
		"one choice":      {`{"state":"s","questions":{"a":{"type":"choice","instructions":"x","criteria":{"p":"q"}}}}`, "choices: 2 to"},
		"name twice":      {`{"state":"s","questions":{"a":{"type":"noul","instructions":"x"},"a":{"type":"noul","instructions":"y"}}}`, "questions: a map"},
		"no instructions": {`{"state":"s","questions":{"a":{"type":"noul"}}}`, "instructions: the question is required"},
	} {
		var in wire.SystemOneRequest
		if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := FromSystemOne(in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}
}
