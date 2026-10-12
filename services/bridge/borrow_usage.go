package bridge

import (
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// turnUsage accumulates the usage a driven turn's replies reported (each
// completed assistant message's metrics, read through wire.TurnUsageFromMetrics
// exactly as the team dashboard reads them). A turn may post several replies;
// their reported tokens and costs are summed. Nothing is fabricated: a turn
// whose replies reported no usage has tokensReported=false and cost=nil, and
// the borrow record then omits those fields.
type turnUsage struct {
	model          string
	tokensReported bool
	inputTokens    int64
	outputTokens   int64
	cost           *float64
	// replies traces the posted replies for the run record (run_report.go).
	replies runReplies
}

// add folds one consumed reply's metrics into the turn's usage.
func (u *turnUsage) add(metrics map[string]any) {
	if u == nil || len(metrics) == 0 {
		return
	}
	r := wire.TurnUsageFromMetrics(metrics)
	if r.Model != "" {
		u.model = r.Model
	}
	if !r.Reported {
		return
	}
	u.tokensReported = true
	u.inputTokens += r.InputTokens
	u.outputTokens += r.OutputTokens
	if r.CostUsd != nil {
		c := *r.CostUsd
		if u.cost != nil {
			c += *u.cost
		}
		u.cost = &c
	}
}

// usage is the turn's usage projection, the one both reports read (the
// borrow record and the run report): the model the core reported, else the
// model the job requested; tokens and cost only when reported.
func (u turnUsage) usage(requestedModel string) wire.AgentRunUsage {
	out := wire.AgentRunUsage{Model: firstNonEmpty(u.model, strings.TrimSpace(requestedModel)), CostUsd: u.cost}
	if u.tokensReported {
		in, outTok := u.inputTokens, u.outputTokens
		out.InputTokens, out.OutputTokens = &in, &outTok
	}
	return out
}

// recordFields are the usage fields of the borrow accountability record
// (.../authorization/record), from the usage projection.
func (u turnUsage) recordFields(requestedModel string) map[string]any {
	use := u.usage(requestedModel)
	out := map[string]any{}
	if use.Model != "" {
		out["model"] = use.Model
	}
	if use.InputTokens != nil {
		out["inputTokens"] = *use.InputTokens
		out["outputTokens"] = *use.OutputTokens
	}
	if use.CostUsd != nil {
		out["cost"] = *use.CostUsd
	}
	return out
}
