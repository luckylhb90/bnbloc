package decision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The SystemOne format (POST /v1/systemone): the same call as the list
// format, its questions and answers maps keyed by question name. It is
// converted here, both ways, so the request rules (Validate) and the
// provider call stay one.

// FromSystemOne reads a SystemOne request into a validated Request: State
// is the input (a JSON string as is, any other value as its JSON text);
// each question keeps its name and its written order; noul is a predicate
// whose optional criteria ("true", "false") join its instructions; a
// choice's criteria map each label to its description; a score's criteria
// list its levels, lowest first. Instructions, descriptions and levels may
// be JSON objects, judged as their JSON text.
func FromSystemOne(in wire.SystemOneRequest) (Request, error) {
	input, err := stateText(in.State)
	if err != nil {
		return Request{}, err
	}
	fields, err := orderedObject(in.Questions)
	if err != nil {
		return Request{}, invalid("questions: a map of question name to question")
	}
	out := wire.DecisionRequest{Model: in.Model, Input: input}
	for _, f := range fields {
		at := "questions." + f.key
		if f.key == "" || f.key != strings.TrimSpace(f.key) {
			return Request{}, invalid("questions: a name is required, without surrounding spaces (%q)", f.key)
		}
		var q struct {
			Type         string          `json:"type"`
			Instructions json.RawMessage `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		}
		if err := json.Unmarshal(f.value, &q); err != nil {
			return Request{}, invalid("%s: an object with type, instructions and criteria", at)
		}
		dq := wire.DecisionQuestion{Name: f.key, Instructions: asText(q.Instructions)}
		switch q.Type {
		case wire.SystemOneNoul:
			dq.Type = wire.DecisionPredicate
			if len(q.Criteria) > 0 && string(q.Criteria) != "null" {
				var c map[string]json.RawMessage
				if json.Unmarshal(q.Criteria, &c) != nil {
					return Request{}, invalid("%s.criteria: an object of \"true\" and \"false\"", at)
				}
				if t := asText(c["true"]); t != "" {
					dq.Instructions += "\n\nTrue means: " + t
				}
				if f := asText(c["false"]); f != "" {
					dq.Instructions += "\nFalse means: " + f
				}
			}
		case wire.DecisionChoice:
			dq.Type = wire.DecisionChoice
			labels, err := orderedObject(q.Criteria)
			if err != nil {
				return Request{}, invalid("%s.criteria: a map of label to description", at)
			}
			for _, l := range labels {
				dq.Choices = append(dq.Choices, wire.DecisionChoiceOption{Value: l.key, Description: asText(l.value)})
			}
		case wire.DecisionScore:
			dq.Type = wire.DecisionScore
			var levels []json.RawMessage
			if json.Unmarshal(q.Criteria, &levels) != nil {
				return Request{}, invalid("%s.criteria: a list of levels, lowest first", at)
			}
			for _, l := range levels {
				dq.Levels = append(dq.Levels, wire.DecisionLevel{Label: asText(l)})
			}
		default:
			return Request{}, invalid("%s.type must be %q, %q or %q", at, wire.SystemOneNoul, wire.DecisionChoice, wire.DecisionScore)
		}
		out.Questions = append(out.Questions, dq)
	}
	return Validate(out)
}

// ToSystemOne writes a reply in the SystemOne format, keyed by question
// name (every SystemOne question has one).
func ToSystemOne(reply wire.DecisionReply) wire.SystemOneReply {
	out := wire.SystemOneReply{Model: reply.Model, Usage: reply.Usage, Answers: make(map[string]wire.SystemOneAnswer, len(reply.Answers))}
	for _, a := range reply.Answers {
		ans := wire.SystemOneAnswer{Type: a.Type, Choice: a.Choice, Score: a.Score, Confidence: a.Confidence}
		switch a.Type {
		case wire.DecisionPredicate:
			ans.Type, ans.Noul = wire.SystemOneNoul, a.Probability
		case wire.DecisionChoice:
			ans.Probabilities = map[string]float64{}
			for _, p := range a.Probabilities {
				if v, ok := p.Value.(string); ok {
					ans.Probabilities[v] = p.Probability
				}
			}
		case wire.DecisionScore:
			ans.Probabilities, ans.Legend = map[string]float64{}, map[string]string{}
			for _, p := range a.Probabilities {
				if lvl, ok := p.Value.(int); ok {
					k := strconv.Itoa(lvl)
					ans.Probabilities[k], ans.Legend[k] = p.Probability, p.Label
				}
			}
		}
		out.Answers[a.Name] = ans
	}
	return out
}

// stateText is the input of a SystemOne state: a JSON string as is, any
// other value as its compact JSON text.
func stateText(raw json.RawMessage) (json.RawMessage, error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil, invalid("state: the thing to judge is required")
	}
	if t[0] == '"' {
		return raw, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, invalid("state: not JSON")
	}
	return json.Marshal(buf.String())
}

// asText is a criterion or an instruction as text: a JSON string's value,
// any other value its compact JSON text, "" for none.
func asText(raw json.RawMessage) string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}

// field is one member of a JSON object, in written order.
type field struct {
	key   string
	value json.RawMessage
}

// orderedObject reads a JSON object's members in written order (a Go map
// would lose it); a key written twice is refused.
func orderedObject(raw json.RawMessage) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var out []field
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		if seen[key] {
			return nil, fmt.Errorf("%q is written twice", key)
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, field{key: key, value: v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}
