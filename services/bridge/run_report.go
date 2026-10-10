package bridge

import (
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// runReplies traces the visible replies a turn posted, for the run record
// (R10): the first reply (id, preview, time) and how many were posted. It
// rides the job-status report diagnostics beside the turn's usage.
type runReplies struct {
	first *wire.AgentRunReply
	count int
}

// noteReply records one reply that reached the room.
func (u *turnUsage) noteReply(m corepkg.Message) {
	if u == nil {
		return
	}
	u.replies.count++
	if u.replies.first != nil {
		return
	}
	at := m.CreatedAtMs
	if at <= 0 {
		at = time.Now().UnixMilli()
	}
	u.replies.first = &wire.AgentRunReply{MessageID: m.MessageID, Text: wire.RunPreview(m.Text), AtMs: at}
}

// withRunReport adds the turn's reply summary and reported usage to a
// job-status diagnostics map (wire.RunDiagReply / wire.RunDiagUsage). Only
// what the turn actually produced is added; the map is returned for chaining.
func (u turnUsage) withRunReport(diag map[string]any, requestedModel string) map[string]any {
	if u.replies.first != nil {
		rep := *u.replies.first
		rep.Count = u.replies.count
		diag[wire.RunDiagReply] = rep
	}
	usage := wire.AgentRunUsage{CostUsd: u.cost}
	if m := firstNonEmpty(u.model, requestedModel); m != "" {
		usage.Model = m
	}
	if u.tokensReported {
		in, out := u.inputTokens, u.outputTokens
		usage.InputTokens, usage.OutputTokens = &in, &out
	}
	if usage.Model != "" || usage.InputTokens != nil || usage.CostUsd != nil {
		diag[wire.RunDiagUsage] = usage
	}
	return diag
}
