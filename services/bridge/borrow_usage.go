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

// recordFields are the usage fields of the borrow accountability record
// (.../authorization/record). The model is the one the core reported, else
// the model the job requested; tokens and cost appear only when reported.
func (u turnUsage) recordFields(requestedModel string) map[string]any {
	out := map[string]any{}
	if m := firstNonEmpty(u.model, strings.TrimSpace(requestedModel)); m != "" {
		out["model"] = m
	}
	if u.tokensReported {
		out["inputTokens"] = u.inputTokens
		out["outputTokens"] = u.outputTokens
	}
	if u.cost != nil {
		out["cost"] = *u.cost
	}
	return out
}
