package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/decision"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An assist's gate (docs/prd/2026-10-20-never-a-dead-end.md §2.1): a run
// with "gated" first asks the decision model the task's question about the
// context the task reads, through the one decision call (callDecision), so
// an assist a client runs on its own (after a turn) costs a chat model
// call only when the gate says it is worth one. The platform names no
// question: the gate is the task's.

// assistGateQuestion names the gate's one question in the decision call.
const assistGateQuestion = "gate"

// assistGate asks task's gate about the run's prompt (the user message:
// the resolved context and the text, fenced and introduced as data) and
// answers the run itself when it stops there: wire.AssistSkipped when no
// decision model runs for the caller or the answer is below the threshold
// (a declined question included), an error when the call failed. true:
// the assist runs.
func (s *Service) assistGate(w http.ResponseWriter, r *http.Request, claims auth.Claims, task assistTask, msgs []assist.Message) bool {
	gate := task.Package.Manifest.Gate
	input, err := json.Marshal(roomlog.Truncate(msgs[len(msgs)-1].Content, wire.MaxDecisionInputText))
	if err != nil {
		obs.ServerError(w, "encode the gate's input failed", err)
		httpx.Error(w, "encode the gate's input failed", http.StatusInternalServerError)
		return false
	}
	req, err := decision.Validate(wire.DecisionRequest{Input: input, Questions: []wire.DecisionQuestion{
		{Type: wire.DecisionPredicate, Name: assistGateQuestion, Instructions: gate.Question},
	}})
	if err != nil {
		// The validator held the gate when it was saved; a refusal here is
		// a rule that drifted, not the member's request.
		obs.ServerError(w, "the assist's gate is not a valid decision", err)
		httpx.Error(w, "the assist's gate is not a valid decision: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	reply, has, err := s.callDecision(r.Context(), claims, headerAuth(r), task.Component.Name+"@"+task.Version.Version, req)
	switch {
	case errors.Is(err, errDecisionProviderRead):
		obs.ServerError(w, "read the decision provider failed", err)
		httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
		return false
	case err != nil:
		writeDecisionErr(w, err)
		return false
	case !has:
		httpx.WriteJSON(w, http.StatusOK, wire.AssistSkipped{Skipped: true, Reason: wire.AssistSkipNoGate})
		return false
	}
	if p := gateProbability(reply); p < gate.Threshold {
		httpx.WriteJSON(w, http.StatusOK, wire.AssistSkipped{Skipped: true, Reason: wire.AssistSkipBelow})
		return false
	}
	return true
}

// gateProbability is the probability of yes the reply gives the gate's
// question; 0 when it declined it or answered something else.
func gateProbability(reply wire.DecisionReply) float64 {
	for _, a := range reply.Answers {
		if a.Name == assistGateQuestion && a.Type == wire.DecisionPredicate && a.Probability != nil {
			return *a.Probability
		}
	}
	return 0
}
