package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A service's own AI tasks (docs/prd/2026-10-23-connections-ai.md §2, the
// assist primitive): POST /v1/component-assist/{task}, signed by the
// service like its store calls, runs one JSON assist task named after the
// service (component.ServiceAssistTask) for an organization the service
// is distributed to, on that organization's assist model, with the text
// the service sends and no other context. The task resolves to the
// organization's version, else the platform's (assist.ServiceTask). Each
// run spends the service's rate in that organization and is an assist
// audit row with the service as the actor.

// serviceAssistKey is a service's assist rate key in an organization.
func serviceAssistKey(service, org string) string { return "service/" + service + "\x00" + org }

func (s *Service) handleComponentAssist(w http.ResponseWriter, r *http.Request) {
	kind, name := componentCaller(r)
	if kind != extreg.KindService {
		httpx.Error(w, component.ComponentHeader+" must name a service (service/<name>)", http.StatusUnauthorized)
		return
	}
	body, ok := readBody(w, r, maxAssistRequestBytes)
	if !ok {
		return
	}
	if _, ok := s.verifyComponentCall(w, r, kind, name, component.SignedRequest(r.Method, r.URL.EscapedPath(), "", body)); !ok {
		return
	}
	var req component.AssistRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Org == "" {
		httpx.Error(w, "the body is an AssistRequest naming its org", http.StatusBadRequest)
		return
	}
	if !utf8.ValidString(req.Text) || strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > component.MaxAssistText {
		httpx.Error(w, fmt.Sprintf("text must be 1 to %d characters", component.MaxAssistText), http.StatusBadRequest)
		return
	}
	taskName := r.PathValue("task")
	if _, platformTask := wire.AssistContractOf(taskName); platformTask || !component.ServiceAssistTask(name, taskName) {
		httpx.Error(w, "a service runs only its own tasks, named "+name+"-<task>", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if _, err := s.tenantSettings(ctx, req.Org); errors.Is(err, store.ErrTenantNotFound) {
		httpx.Error(w, "no such organization", http.StatusNotFound)
		return
	}
	if _, ok := s.serviceFor(w, ctx, req.Org, name, component.ServiceAssist); !ok {
		return
	}
	claims := auth.Claims{Subject: "service/" + name, Tenant: req.Org}
	model, ok, err := s.assistModel(ctx, claims)
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, errAssistUnavailable.Error(), http.StatusConflict)
		return
	}
	c, found, err := assist.ServiceTask(ctx, s.ContentComponents, viewerOf(auth.Claims{Subject: req.Member, Tenant: req.Org}), taskName)
	if err != nil {
		contentStoreErr(w, "resolve the assist failed", err)
		return
	}
	if !found {
		httpx.Error(w, "the task "+taskName+" is not installed: publish it as content", http.StatusNotFound)
		return
	}
	task, err := s.loadAssistTask(ctx, c)
	if err != nil {
		contentStoreErr(w, "load the assist failed", err)
		return
	}
	if task.Package.Manifest.Output != wire.AssistOutputJSON {
		httpx.Error(w, "a service's task answers JSON", http.StatusBadRequest)
		return
	}
	if !s.assistLimiter.Get(assistRate, assistBurst, s.Peers.Size).Allow(serviceAssistKey(name, req.Org)) {
		httpx.Error(w, "too many AI runs for this service: try again in a minute", http.StatusTooManyRequests)
		return
	}
	what := assistWhat(task)
	if req.Member != "" {
		what += ", for " + req.Member
	}
	fields, _, err := s.completeJSONTask(ctx, claims, task, model, assist.Messages(task.Package.Instructions, req.Text, assist.Context{}), what)
	if err != nil {
		writeAssistModelErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, component.AssistReply{Fields: fields})
}
