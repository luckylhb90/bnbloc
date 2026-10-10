package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The run record (R10) summary rides the job-status diagnostics: the first
// visible reply (preview-bounded) plus the reply count, and the usage the
// turn's replies reported — nothing that was not reported.
func TestRunReportSummarizesRepliesAndUsage(t *testing.T) {
	var u turnUsage
	u.add(map[string]any{"model": "m-1", "inputTokens": 100.0, "outputTokens": 20.0, "costUsd": 0.25})
	u.noteReply(corepkg.Message{MessageID: "m-a", Text: strings.Repeat("y", 500), CreatedAtMs: 42})
	u.add(map[string]any{"inputTokens": 5.0, "outputTokens": 1.0})
	u.noteReply(corepkg.Message{MessageID: "m-b", Text: "second", CreatedAtMs: 50})

	diag := u.withRunReport(map[string]any{"core": "c1"}, "requested")
	raw, _ := json.Marshal(diag)
	var got struct {
		Core  string             `json:"core"`
		Reply wire.AgentRunReply `json:"reply"`
		Usage wire.AgentRunUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Core != "c1" || got.Reply.MessageID != "m-a" || got.Reply.AtMs != 42 || got.Reply.Count != 2 {
		t.Fatalf("reply = %+v", got.Reply)
	}
	if n := len([]rune(got.Reply.Text)); n != wire.RunPreviewRunes+1 {
		t.Fatalf("preview runes = %d", n)
	}
	if got.Usage.Model != "m-1" || *got.Usage.InputTokens != 105 || *got.Usage.OutputTokens != 21 || *got.Usage.CostUsd != 0.25 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestRunReportOmitsWhatWasNotProduced(t *testing.T) {
	var u turnUsage
	diag := u.withRunReport(map[string]any{"error": "boom"}, "")
	if _, ok := diag[wire.RunDiagReply]; ok {
		t.Fatal("no reply posted, no reply summary")
	}
	if _, ok := diag[wire.RunDiagUsage]; ok {
		t.Fatal("nothing reported and no model requested, no usage")
	}
	// The requested model alone is still worth recording.
	diag = u.withRunReport(map[string]any{}, "m-req")
	if us, ok := diag[wire.RunDiagUsage].(wire.AgentRunUsage); !ok || us.Model != "m-req" || us.InputTokens != nil || us.CostUsd != nil {
		t.Fatalf("usage = %+v", diag[wire.RunDiagUsage])
	}
}
