package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/decision"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/modelsettings"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Decision models (docs/prd/2026-10-17-decision-models.md): calibrated
// answers to bounded questions, through an admin-configured provider
// (internal/decision owns the call and the request rules). This file admits
// the caller and relays the reply, as the media routes do: the caller's
// assist rate, the cost budget, an audit row without content.
//
//	GET  /v1/decisions                     whether decisions run for the caller, and the models
//	POST /v1/decisions                     one decision call, the list format (a member, or ?job=&room= for an agent)
//	POST /v1/systemone                     the same call in the SystemOne format (maps keyed by name)
//	GET  /v1/platform/decisions            the platform's provider (never its key)
//	PUT  /v1/platform/decisions            set it (super admin)
//	POST /v1/platform/decisions/test       one real short call through it
//	GET/PUT /v1/tenant/decisions, POST …/test   an organization's own

// maxDecisionBody bounds one request body: the images (inline, base64) and
// the text, with room for the questions.
const maxDecisionBody = wire.MaxDecisionImages*(wire.MaxDecisionImageBytes*4/3+16) + wire.MaxDecisionInputText + 2<<20

// decisionSettings is the decision provider's settings.
func (s *Service) decisionSettings() *decision.Settings {
	return &decision.Settings{Store: s.PlatformSettings, Box: s.SecretBox, Now: func() int64 { return s.nowTime().UnixMilli() }}
}

// decisionProvider resolves the provider for claims' organization: its own
// while set and allowed, else the platform's; none when the organization
// turned AI off. ownAuth is the request's Authorization when it is the
// acting member's own (a member's call), "" otherwise (an agent acting for
// a requester, an app page): an auth-"caller" provider then refuses
// (decision.ErrNoCallerAuth), never forwarding someone else's session.
func (s *Service) decisionProvider(ctx context.Context, claims auth.Claims, ownAuth string) (decision.Provider, bool, error) {
	if s.PlatformSettings == nil {
		return decision.Provider{}, false, nil
	}
	t, err := s.tenantSettings(ctx, claims.Tenant)
	if err != nil && !errors.Is(err, store.ErrTenantNotFound) {
		return decision.Provider{}, false, err
	}
	if t.AssistDisabled {
		return decision.Provider{}, false, nil
	}
	p, ok, err := s.decisionSettings().Provider(ctx, claims.Tenant)
	if err != nil || !ok {
		return p, ok, err
	}
	return p.WithCallerAuth(ownAuth), true, nil
}

// errDecisionsUnavailable: no provider, or the organization turned AI off.
var errDecisionsUnavailable = errors.New("decisions are not available here: the deployment has no decision model, or your organization turned AI off")

// handleDecisionCapability answers GET /v1/decisions.
func (s *Service) handleDecisionCapability(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	p, ok, err := s.decisionProvider(r.Context(), claims, headerAuth(r))
	if err != nil {
		obs.ServerError(w, "read the decision provider failed", err)
		httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
		return
	}
	out := wire.DecisionCapability{}
	if ok && p.Model.Configured() {
		out.Available, out.Models = true, p.Models
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleDecide answers POST /v1/decisions: a member's own call, or with
// ?job=&room= an agent's as the job's requester (mediaCaller, the content
// routes' admission; the room relay sets the grant).
func (s *Service) handleDecide(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	s.runDecision(w, r, actor.Claims, ownAuthOf(r, actor), "decide", decisionList)
}

// handleSystemOne answers POST /v1/systemone: handleDecide in the SystemOne
// format, for callers written for it.
func (s *Service) handleSystemOne(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	s.runDecision(w, r, actor.Claims, ownAuthOf(r, actor), "decide", decisionSystemOne)
}

// decisionFormat is one request and reply format of the same call: how a
// body is read into a validated request (false: refused, answered) and how
// the reply is written.
type decisionFormat struct {
	read  func(w http.ResponseWriter, r *http.Request) (decision.Request, bool)
	reply func(wire.DecisionReply) any
}

var (
	// decisionList is the OpenAI Decisions format (ordered lists).
	decisionList = decisionFormat{
		read: func(w http.ResponseWriter, r *http.Request) (decision.Request, bool) {
			var in wire.DecisionRequest
			if !httpx.DecodeJSON(w, r, int64(maxDecisionBody), &in) {
				return decision.Request{}, false
			}
			req, err := decision.Validate(in)
			return validDecision(w, req, err)
		},
		reply: func(reply wire.DecisionReply) any { return reply },
	}
	// decisionSystemOne is the SystemOne format (maps keyed by name).
	decisionSystemOne = decisionFormat{
		read: func(w http.ResponseWriter, r *http.Request) (decision.Request, bool) {
			var in wire.SystemOneRequest
			if !httpx.DecodeJSON(w, r, int64(maxDecisionBody), &in) {
				return decision.Request{}, false
			}
			req, err := decision.FromSystemOne(in)
			return validDecision(w, req, err)
		},
		reply: func(reply wire.DecisionReply) any { return decision.ToSystemOne(reply) },
	}
)

// validDecision answers a refused request 400, naming the field.
func validDecision(w http.ResponseWriter, req decision.Request, err error) (decision.Request, bool) {
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return decision.Request{}, false
	}
	return req, true
}

// decisionRate bounds one member's decision calls: a decision is a step of
// a loop (a browser run asks one or two per page step, about a second
// each), so its bucket is sized for steps, not for a person's assists
// (assistRate). Every call still spends the cost budget and is audited.
const (
	decisionRate  = 600.0 / 60.0
	decisionBurst = 120.0
)

// runDecision is the ONE owner of a decision call (the member route and an
// app's ~app/ai/decide): the caller's decision rate, the request's
// validation, the provider, the call, the audit row (never the input, the
// questions or the answers) and the reply.
func (s *Service) runDecision(w http.ResponseWriter, r *http.Request, claims auth.Claims, ownAuth, auditName string, format decisionFormat) {
	ctx := r.Context()
	if !s.decisionLimiter.get(decisionRate, decisionBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many decision calls: try again in a few seconds", http.StatusTooManyRequests)
		return
	}
	req, ok := format.read(w, r)
	if !ok {
		return
	}
	p, has, err := s.decisionProvider(ctx, claims, ownAuth)
	if err != nil {
		obs.ServerError(w, "read the decision provider failed", err)
		httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, errDecisionsUnavailable.Error(), http.StatusConflict)
		return
	}
	start := time.Now()
	reply, err := p.Decide(ctx, req)
	s.auditDecision(ctx, claims, auditName, p, req, reply.Usage, time.Since(start).Milliseconds(), err)
	if err != nil {
		writeDecisionErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, format.reply(reply))
}

// auditDecision records one decision call: the model, the question and
// image counts, the tokens and the latency; never the input, the questions
// or the answers.
func (s *Service) auditDecision(ctx context.Context, claims auth.Claims, name string, p decision.Provider, req decision.Request, usage wire.DecisionUsage, latencyMs int64, runErr error) {
	model := p.Choose(req.Model)
	if p.Model.Org {
		model += " (the organization's)"
	}
	detail := fmt.Sprintf("decision, model %s, %d questions, %d images, %d input + %d output tokens, %d ms",
		model, len(req.Questions), req.Images(), usage.InputTokens, usage.OutputTokens, latencyMs)
	outcome := wire.AuditOutcomeAllow
	if runErr != nil {
		outcome, detail = "failed", detail+", failed: "+decisionErrClass(runErr)
	}
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{
		Kind: wire.AuditKindAssist, Name: name, Action: "run",
		Actor: claims.Subject, Outcome: outcome, Detail: detail,
	})
}

// decisionErrClass names a failure for the audit row without the
// provider's message (it may quote the request).
func decisionErrClass(err error) string {
	var pe *decision.ProviderError
	switch {
	case errors.As(err, &pe):
		return fmt.Sprintf("provider answered %d", pe.Status)
	case errors.Is(err, decision.ErrNoCallerAuth):
		return "no caller session to forward"
	case errors.Is(err, decision.ErrProviderUnreachable):
		return "unreadable reply"
	case errors.Is(err, context.Canceled):
		return "caller went away"
	}
	return "provider unreachable"
}

// writeDecisionErr answers a failed call: the provider's refusal with its
// own status and message (a 4xx names the field), a caller-auth provider
// without the caller's session as 409, anything else as 502.
func writeDecisionErr(w http.ResponseWriter, err error) {
	var pe *decision.ProviderError
	switch {
	case errors.Is(err, context.Canceled):
	case errors.Is(err, decision.ErrNoCallerAuth):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &pe):
		status := http.StatusBadGateway
		if pe.Status >= 400 && pe.Status < 500 {
			status = pe.Status
		}
		httpx.WriteStatus(w, status, "the decision provider refused the call: "+pe.Message, err)
	default:
		httpx.WriteStatus(w, http.StatusBadGateway, "the decision provider could not be reached", err)
	}
}

// --- the provider settings (platform and organization) ---

// handleDecisionProvider answers GET /v1/platform/decisions.
func (s *Service) handleDecisionProvider(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	view, err := s.decisionSettings().View(r.Context(), "")
	if err != nil {
		obs.ServerError(w, "read the decision provider failed", err)
		httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// handlePutDecisionProvider answers PUT /v1/platform/decisions.
func (s *Service) handlePutDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.putDecisionProvider(w, r, claims, "")
}

// handleOrgDecisionProvider answers GET /v1/tenant/decisions: the
// organization's own provider and the hosts it may name.
func (s *Service) handleOrgDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	own, err := s.decisionSettings().View(ctx, claims.Tenant)
	if err == nil {
		var platform wire.MediaProviderConfig
		if platform, err = s.decisionSettings().View(ctx, ""); err == nil {
			out := wire.MediaOrgProvider{Provider: own, AllowedHosts: platform.OrgHosts, PlatformModel: platform.Model}
			if out.AllowedHosts == nil {
				out.AllowedHosts = []string{}
			}
			if out.Provider.OrgHosts == nil {
				out.Provider.OrgHosts = []string{}
			}
			httpx.WriteJSON(w, http.StatusOK, out)
			return
		}
	}
	obs.ServerError(w, "read the decision provider failed", err)
	httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
}

// handlePutOrgDecisionProvider answers PUT /v1/tenant/decisions: the
// organization's own, on a host the platform allows (an empty base URL goes
// back to the platform's).
func (s *Service) handlePutOrgDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.putDecisionProvider(w, r, claims, claims.Tenant)
}

// putDecisionProvider stores the platform's (tenant "") or an
// organization's provider and audits the change.
func (s *Service) putDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims, tenant string) {
	var u wire.MediaProviderUpdate
	if !httpx.DecodeJSON(w, r, 64<<10, &u) {
		return
	}
	name := "decision-model"
	if tenant != "" {
		u.OrgHosts, name = nil, "org-decision-model" // an organization never sets the hosts
	}
	view, err := s.decisionSettings().Put(r.Context(), tenant, u, claims.Subject)
	switch {
	case errors.Is(err, modelsettings.ErrSettingsOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &modelsettings.InvalidError{}):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		obs.ServerError(w, "save the decision provider failed", err)
		httpx.Error(w, "save the decision provider failed", http.StatusInternalServerError)
	default:
		s.audit(r.Context(), claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindAssist, Name: name,
			Action: "provider-set", Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow,
			Detail: "kind " + view.Kind + ", model " + view.Model + " at " + hostOfBaseURL(view.BaseURL)})
		if tenant != "" {
			s.handleOrgDecisionProvider(w, r, claims)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

// handleTestDecisionProvider answers POST /v1/platform/decisions/test.
func (s *Service) handleTestDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.testDecisionProvider(w, r, claims, "")
}

// handleTestOrgDecisionProvider answers POST /v1/tenant/decisions/test
// through the organization's effective provider.
func (s *Service) handleTestOrgDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.testDecisionProvider(w, r, claims, claims.Tenant)
}

// testDecisionProvider makes one real call (one predicate) through the
// provider for tenant; a failure is the answer, not an HTTP error. A
// caller-auth provider is tested with the admin's own session.
func (s *Service) testDecisionProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims, tenant string) {
	ctx := r.Context()
	p, has, err := s.decisionSettings().Provider(ctx, tenant)
	if err != nil {
		obs.ServerError(w, "read the decision provider failed", err)
		httpx.Error(w, "read the decision provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, "no decision model is configured", http.StatusConflict)
		return
	}
	req, _ := decision.Validate(wire.DecisionRequest{
		Input:     []byte(`"The sky is blue on a clear day."`),
		Questions: []wire.DecisionQuestion{{Type: wire.DecisionPredicate, Name: "true", Instructions: "Is this statement true?"}},
	})
	out := wire.MediaProviderTest{}
	start := time.Now()
	reply, err := p.WithCallerAuth(headerAuth(r)).Decide(ctx, req)
	out.LatencyMs = time.Since(start).Milliseconds()
	s.auditDecision(ctx, claims, "decide-test", p, req, reply.Usage, out.LatencyMs, err)
	switch {
	case err != nil:
		var pe *decision.ProviderError
		if errors.As(err, &pe) {
			out.Error = pe.Error()
		} else {
			out.Error = "the provider could not be reached: " + err.Error()
		}
	case len(reply.Answers) == 1 && reply.Answers[0].Probability != nil:
		out.OK = true
		out.Reply = fmt.Sprintf("%s: p(yes) = %.2f", firstNonEmpty(reply.Model, p.Choose("")), *reply.Answers[0].Probability)
	default:
		out.Error = "the provider answered without a probability"
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
