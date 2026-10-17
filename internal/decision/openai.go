package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// openAIDecisions is the OpenAI Decisions wire family: POST {base}/decisions
// with {model, input, questions} and an {model, answers, usage} reply, the
// answers in question order. Its params are none today.
type openAIDecisions struct{}

func (*openAIDecisions) Kind() string { return AdapterKindOpenAIDecisions }

// maxReplyBody bounds what is read of one provider answer (255 choices ×
// 128 questions of probabilities fit well within it).
const maxReplyBody = 4 << 20

// upstreamRequest is the provider's request body.
type upstreamRequest struct {
	Model     string                  `json:"model"`
	Input     any                     `json:"input"`
	Questions []wire.DecisionQuestion `json:"questions"`
}

// upstreamReply is the provider's answer as it arrives.
type upstreamReply struct {
	Model   string           `json:"model"`
	Answers []upstreamAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type upstreamAnswer struct {
	Type          string                `json:"type"`
	Name          string                `json:"name"`
	Probability   *float64              `json:"probability"`
	Choice        string                `json:"choice"`
	Score         *float64              `json:"score"`
	Confidence    *float64              `json:"confidence"`
	Probabilities []upstreamProbability `json:"probabilities"`
}

// upstreamProbability is one choice value's ("value": a string) or one
// level's ("value": its index, "label") probability.
type upstreamProbability struct {
	Value       json.RawMessage `json:"value"`
	Label       string          `json:"label"`
	Probability float64         `json:"probability"`
}

// Decide posts the request and normalizes the reply: one answer per
// question, in order, of the question's type.
func (a *openAIDecisions) Decide(ctx context.Context, p Provider, model string, req Request) (wire.DecisionReply, error) {
	body := upstreamRequest{Model: model, Questions: req.Questions}
	if len(req.Messages) > 0 {
		body.Input = req.Messages
	} else {
		body.Input = req.Text
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return wire.DecisionReply{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.Model.BaseURL, "/")+"/decisions", bytes.NewReader(raw))
	if err != nil {
		return wire.DecisionReply{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	p.setAuth(hr)
	resp, err := p.client.Do(hr)
	if err != nil {
		return wire.DecisionReply{}, fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return wire.DecisionReply{}, providerError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody+1))
	if err != nil || len(data) > maxReplyBody {
		return wire.DecisionReply{}, ErrProviderUnreachable
	}
	var up upstreamReply
	if err := json.Unmarshal(data, &up); err != nil {
		return wire.DecisionReply{}, ErrProviderUnreachable
	}
	return normalize(up, req.Questions)
}

// normalize turns the provider's answers into the wire reply, checking
// that each answers its question (same position, same type, or a refusal of
// that one question, passed on as one): a reply that does not is
// unreadable, never passed on half-matched.
func normalize(up upstreamReply, qs []wire.DecisionQuestion) (wire.DecisionReply, error) {
	if len(up.Answers) != len(qs) {
		return wire.DecisionReply{}, fmt.Errorf("%w: %d answers for %d questions", ErrProviderUnreachable, len(up.Answers), len(qs))
	}
	out := wire.DecisionReply{Model: up.Model, Answers: make([]wire.DecisionAnswer, 0, len(qs)),
		Usage: wire.DecisionUsage{InputTokens: up.Usage.InputTokens, OutputTokens: up.Usage.OutputTokens}}
	for i, a := range up.Answers {
		q := qs[i]
		if a.Type == wire.DecisionRefusal {
			out.Answers = append(out.Answers, wire.DecisionAnswer{Type: wire.DecisionRefusal, Name: q.Name})
			continue
		}
		if a.Type != q.Type {
			return wire.DecisionReply{}, fmt.Errorf("%w: answer %d is a %s, the question a %s", ErrProviderUnreachable, i, a.Type, q.Type)
		}
		ans := wire.DecisionAnswer{Type: q.Type, Name: q.Name, Confidence: a.Confidence}
		switch q.Type {
		case wire.DecisionPredicate:
			if a.Probability == nil {
				return wire.DecisionReply{}, fmt.Errorf("%w: predicate %d has no probability", ErrProviderUnreachable, i)
			}
			ans.Probability = a.Probability
		case wire.DecisionChoice:
			ans.Choice = a.Choice
			for _, pr := range a.Probabilities {
				var v string
				if json.Unmarshal(pr.Value, &v) != nil {
					continue
				}
				ans.Probabilities = append(ans.Probabilities, wire.DecisionProbability{Value: v, Probability: pr.Probability})
			}
		case wire.DecisionScore:
			if a.Score == nil {
				return wire.DecisionReply{}, fmt.Errorf("%w: score %d has no score", ErrProviderUnreachable, i)
			}
			ans.Score = a.Score
			for _, pr := range a.Probabilities {
				var lvl int
				if json.Unmarshal(pr.Value, &lvl) != nil {
					continue
				}
				ans.Probabilities = append(ans.Probabilities, wire.DecisionProbability{Value: lvl, Label: pr.Label, Probability: pr.Probability})
			}
		}
		out.Answers = append(out.Answers, ans)
	}
	return out, nil
}

// providerError reads a refusal: the provider's error message, bounded.
func providerError(resp *http.Response) *ProviderError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var e struct {
		Error any `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			msg, _ = v["message"].(string)
		}
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return &ProviderError{Status: resp.StatusCode, Message: msg}
}
