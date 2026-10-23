package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/servicecomp"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Service components (docs/architecture/service-components.md): an endpoint
// component that acts for members and their agents' turns through the
// primitives it declares. The gateway is the one place that attests who a
// call is for (a turn through jobGrant, a member through a page ticket),
// forwards it signed (servicecomp.Forward), records the audit row the
// component answers with, and keeps the component's sealed values. What a
// call may do is the component's decision.

// servicePageTicketTTL is how long an opened page works: long enough for a
// sign-in at another system, short enough that a leaked address is of
// little use.
const servicePageTicketTTL = time.Hour

// maxServicePageList bounds one service's page list.
const maxServicePageList = 32

// services lists the installed services distributed to the organization
// (extpipe.TenantDistribution, like a share target: a service is the
// organization's infrastructure and has no per-member install).
func (s *Service) services(ctx context.Context, tenantID string) ([]extpipe.Record, error) {
	recs, err := s.corePipeline().List(ctx, extreg.KindService)
	if err != nil {
		return nil, err
	}
	out := recs[:0:0]
	for _, rec := range recs {
		if extpipe.TenantDistribution(rec, tenantID).Distributed {
			out = append(out, rec)
		}
	}
	return out, nil
}

// serviceFor is the named service distributed to tenantID that declares
// capability; it answers the refusal itself.
func (s *Service) serviceFor(w http.ResponseWriter, ctx context.Context, tenantID, name, capability string) (extpipe.Record, bool) {
	recs, err := s.services(ctx, tenantID)
	if err != nil {
		obs.ServerError(w, "load services failed", err)
		httpx.Error(w, "load services failed", http.StatusInternalServerError)
		return extpipe.Record{}, false
	}
	for _, rec := range recs {
		if rec.Name != name {
			continue
		}
		if !servicecomp.Declares(rec, capability) {
			httpx.Error(w, "the service does not declare "+capability, http.StatusForbidden)
			return extpipe.Record{}, false
		}
		return rec, true
	}
	httpx.Error(w, "service not found", http.StatusNotFound)
	return extpipe.Record{}, false
}

// forwardToService sends one call to the service and returns its answer;
// a refusal (no endpoint, no credential, unreachable) is answered here.
// The audit row the service answered with is recorded for actor.
func (s *Service) forwardToService(w http.ResponseWriter, r *http.Request, rec extpipe.Record, call servicecomp.Call, actor auth.Claims) (servicecomp.Answer, bool) {
	ep, err := servicecomp.Kind.Endpoint(rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err != nil {
		obs.ServerError(w, "service endpoint unusable", err)
		httpx.Error(w, "the service is not usable: "+err.Error(), http.StatusBadGateway)
		return servicecomp.Answer{}, false
	}
	secret, ok, err := s.componentCredentials().Get(r.Context(), rec.Kind, rec.Name)
	switch {
	case err != nil:
		obs.ServerError(w, "read service credential failed", err)
		httpx.Error(w, "read service credential failed", http.StatusInternalServerError)
		return servicecomp.Answer{}, false
	case !ok:
		obs.ServerError(w, "service has no credential", errors.Join(err, errServiceNoCredential))
		httpx.Error(w, "the service has no credential; rotate one first", http.StatusBadGateway)
		return servicecomp.Answer{}, false
	}
	start := s.nowTime()
	ans, err := servicecomp.Forward(r.Context(), ep, secret, call, s.nowTime())
	if err != nil {
		obs.ServerError(w, "service call failed: "+rec.Name+" "+strings.SplitN(call.Path, "?", 2)[0], err)
		httpx.Error(w, "the "+rec.Name+" service did not answer: "+err.Error(), http.StatusBadGateway)
		return servicecomp.Answer{}, false
	}
	slog.Debug("bnbloc-gateway: service call", "service", rec.Name, "path", strings.SplitN(call.Path, "?", 2)[0],
		"status", ans.Status, "ms", s.nowTime().Sub(start).Milliseconds())
	if a := ans.Audit; a != nil && actor.Tenant != "" {
		outcome := a.Outcome
		if outcome == "" {
			outcome = wire.AuditOutcomeOK
		}
		detail := strings.TrimSpace(a.Target + " " + a.Detail)
		s.audit(r.Context(), actor.Tenant, store.ExtAuditRecord{
			Kind: rec.Kind, Name: rec.Name, Action: a.Action, Actor: actor.Subject, Outcome: outcome, Detail: detail,
		})
	}
	return ans, true
}

// writeServiceAnswer passes a service's answer on: its status, the headers
// servicecomp lets pass, its body. An HTML answer is sandboxed (an opaque
// origin: it reads nothing of the platform's) and never cached.
func writeServiceAnswer(w http.ResponseWriter, ans servicecomp.Answer) {
	h := w.Header()
	for k, v := range ans.Header {
		h[k] = v
	}
	if strings.HasPrefix(strings.ToLower(ans.Header.Get("Content-Type")), "text/html") {
		h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox")
		h.Set("Cache-Control", "no-store")
	}
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(ans.Status)
	_, _ = w.Write(ans.Body)
}

// readServiceBody reads a forwarded call's body (bounded).
func readServiceBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, servicecomp.MaxBody+1))
	if err != nil {
		httpx.Error(w, "read the request body failed", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > servicecomp.MaxBody {
		httpx.Error(w, "the request body is too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// forwardedPath is the rest of the escaped request path after prefix and
// one segment (the component's name or a ticket), with the query minus
// drop.
func forwardedPath(r *http.Request, prefix string, drop ...string) string {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	q := r.URL.Query()
	for _, d := range drop {
		q.Del(d)
	}
	if enc := q.Encode(); enc != "" {
		return rest + "?" + enc
	}
	return rest
}

// handleTurnServices lists the services a bridge may call for its turns
// (GET /v1/my/turn-services): those distributed to the member's
// organization that declare turnProxy.
func (s *Service) handleTurnServices(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	recs, err := s.services(r.Context(), claims.Tenant)
	if err != nil {
		obs.ServerError(w, "load services failed", err)
		httpx.Error(w, "load services failed", http.StatusInternalServerError)
		return
	}
	out := wire.TurnServiceList{Services: []wire.TurnService{}}
	for _, rec := range recs {
		if caps := servicecomp.Capabilities(rec); servicecomp.Declares(rec, component.ServiceTurnProxy) {
			out.Services = append(out.Services, wire.TurnService{Name: rec.Name, Capabilities: caps})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// turnDrive is whose agent runs a job, as the platform attests it.
func turnDrive(job store.AgentJob) string {
	if _, ok := shareagent.PlatformAgentOfTarget(job.TargetID); ok {
		return component.DrivePlatform
	}
	if job.OwnerUserID == job.RequesterUserID {
		return component.DriveOwn
	}
	return component.DriveBorrowed
}

// handleTurnProxy forwards a bridge's call for a job it drives to a service
// (GET|POST /v1/turn-proxy/{component}/{path...}?room=&job=): admitted by
// jobGrant (the job is live on the caller's bridge and its requester may
// still post in the room), the turn attested in component.TurnHeader.
func (s *Service) handleTurnProxy(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	q := r.URL.Query()
	roomID, jobID := strings.TrimSpace(q.Get("room")), strings.TrimSpace(q.Get("job"))
	if roomID == "" || jobID == "" {
		httpx.Error(w, "room and job required", http.StatusBadRequest)
		return
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	actor, err := s.jobGrant(r.Context(), caller, claims, roomID, jobID)
	if err != nil {
		writeGrantErr(w, err)
		return
	}
	rec, ok := s.serviceFor(w, r.Context(), actor.Claims.Tenant, r.PathValue("component"), component.ServiceTurnProxy)
	if !ok {
		return
	}
	body, ok := readServiceBody(w, r)
	if !ok {
		return
	}
	job := *actor.Job
	turn := component.Turn{
		Org: job.TenantID, Room: roomID, Channel: job.ChannelID, Job: job.JobID,
		Requester: job.RequesterUserID, AgentOwner: job.OwnerUserID, Drive: turnDrive(job), Origin: s.publicOrigin(r),
	}
	ans, ok := s.forwardToService(w, r, rec, servicecomp.Call{
		Method: r.Method, Path: component.ServiceTurnPrefix + forwardedPath(r, component.TurnProxyPath, "room", "job"),
		IdentityHeader: component.TurnHeader, Identity: component.EncodeHeader(turn),
		ContentType: r.Header.Get("Content-Type"), Accept: r.Header.Get("Accept"), Body: body,
	}, actor.Claims)
	if !ok {
		return
	}
	writeServiceAnswer(w, ans)
}

// pageTicket is a page ticket's payload (auth.Claims.Data).
type pageTicket struct {
	Page  string `json:"page"`
	Query string `json:"query,omitempty"`
}

// isOrgAdmin reports a member who manages their organization.
func isOrgAdmin(claims auth.Claims) bool { return rbac.Can(claims.Role, rbac.PermManageOrg) }

// handleComponentPages lists the pages the member may open
// (GET /v1/my/component-pages): each service's that declares pages; an
// admin page only to an organization admin.
func (s *Service) handleComponentPages(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	recs, err := s.services(r.Context(), claims.Tenant)
	if err != nil {
		obs.ServerError(w, "load services failed", err)
		httpx.Error(w, "load services failed", http.StatusInternalServerError)
		return
	}
	out := wire.ComponentPageList{Pages: []wire.ComponentPageInfo{}}
	for _, rec := range recs {
		if !servicecomp.Declares(rec, component.ServicePages) {
			continue
		}
		pages, err := s.servicePages(r.Context(), rec, claims, s.publicOrigin(r))
		if err != nil {
			slog.Warn("bnbloc-gateway: service pages unreadable", "service", rec.Name, "error", err)
			out.Unreadable = append(out.Unreadable, rec.Name)
			continue
		}
		for _, p := range pages {
			if p.Audience == component.AudienceAdmin && !isOrgAdmin(claims) {
				continue
			}
			out.Pages = append(out.Pages, wire.ComponentPageInfo{Component: rec.Name, Name: p.Name, Label: p.Label, Audience: p.Audience})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// servicePages reads a service's page list (GET /pages, as the member).
func (s *Service) servicePages(ctx context.Context, rec extpipe.Record, claims auth.Claims, origin string) ([]component.Page, error) {
	ep, err := servicecomp.Kind.Endpoint(rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err != nil {
		return nil, err
	}
	secret, ok, err := s.componentCredentials().Get(ctx, rec.Kind, rec.Name)
	if err != nil || !ok {
		return nil, errors.New("the service has no credential")
	}
	member := component.Member{Subject: claims.Subject, Org: claims.Tenant, Role: claims.Role, Origin: origin}
	ans, err := servicecomp.Forward(ctx, ep, secret, servicecomp.Call{
		Method: http.MethodGet, Path: component.ServicePagesPath,
		IdentityHeader: component.MemberHeader, Identity: component.EncodeHeader(member), Accept: "application/json",
	}, s.nowTime())
	if err != nil {
		return nil, err
	}
	if ans.Status != http.StatusOK {
		return nil, errors.New("the page list answered HTTP " + http.StatusText(ans.Status))
	}
	var list component.PageList
	if err := json.Unmarshal(ans.Body, &list); err != nil {
		return nil, errors.New("the page list is not valid JSON")
	}
	out := list.Pages[:0:0]
	for _, p := range list.Pages {
		if len(out) == maxServicePageList {
			break
		}
		if component.ValidPageName(p.Name) && strings.TrimSpace(p.Label) != "" && (p.Audience == component.AudienceMember || p.Audience == component.AudienceAdmin) {
			out = append(out, p)
		}
	}
	return out, nil
}

// handleOpenComponentPage opens one of a service's pages for the member
// (POST /v1/my/component-pages/{component}/{page}): a ticket naming the
// member, their organization and role, the service and the page, in the
// page's address.
func (s *Service) handleOpenComponentPage(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var req wire.ComponentPageOpen
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, 4<<10, &req) {
		return
	}
	page := r.PathValue("page")
	if !component.ValidPageName(page) {
		httpx.Error(w, "invalid page name", http.StatusBadRequest)
		return
	}
	if _, err := url.ParseQuery(req.Query); err != nil || len(req.Query) > 1024 {
		httpx.Error(w, "invalid page query", http.StatusBadRequest)
		return
	}
	rec, ok := s.serviceFor(w, r.Context(), claims.Tenant, r.PathValue("component"), component.ServicePages)
	if !ok {
		return
	}
	data, _ := json.Marshal(pageTicket{Page: page, Query: req.Query})
	ticket := s.Signer.Issue(auth.Claims{
		Subject: claims.Subject, Tenant: claims.Tenant, Role: claims.Role,
		ExpiresAtUnix: s.nowTime().Add(servicePageTicketTTL).Unix(),
		Kind:          auth.KindComponentPage, ID: rec.Name, Data: string(data),
	})
	u := s.publicOrigin(r) + component.ComponentPagePath + ticket + "/"
	if req.Query != "" {
		u += "?" + req.Query
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ComponentPageSession{URL: u})
}

// handleComponentPage serves a page a member opened
// (GET|POST /v1/component-pages/{ticket}/{path...}): the ticket is the
// credential; the call goes to the service's /pages/{page}/{path} with the
// member attested in component.MemberHeader.
func (s *Service) handleComponentPage(w http.ResponseWriter, r *http.Request) {
	ticket := r.PathValue("ticket")
	claims, err := s.Signer.VerifyKind(ticket, auth.KindComponentPage)
	var pt pageTicket
	if err == nil {
		err = json.Unmarshal([]byte(claims.Data), &pt)
	}
	if err != nil || !component.ValidPageName(pt.Page) {
		httpx.Error(w, "the page link expired or is not valid: open it again from bnbloc", http.StatusUnauthorized)
		return
	}
	rec, ok := s.serviceFor(w, r.Context(), claims.Tenant, claims.ID, component.ServicePages)
	if !ok {
		return
	}
	body, ok := readServiceBody(w, r)
	if !ok {
		return
	}
	origin := s.publicOrigin(r)
	member := component.Member{
		Subject: claims.Subject, Org: claims.Tenant, Role: claims.Role, Page: pt.Page,
		PageURL: origin + component.ComponentPagePath + ticket + "/", Origin: origin,
	}
	ans, ok := s.forwardToService(w, r, rec, servicecomp.Call{
		Method: r.Method, Path: component.ServicePagesPath + "/" + pt.Page + "/" + forwardedPath(r, component.ComponentPagePath),
		IdentityHeader: component.MemberHeader, Identity: component.EncodeHeader(member),
		ContentType: r.Header.Get("Content-Type"), Accept: r.Header.Get("Accept"), Body: body,
	}, auth.Claims{Subject: claims.Subject, Tenant: claims.Tenant})
	if !ok {
		return
	}
	writeServiceAnswer(w, ans)
}

// handleComponentCallback forwards another system's redirect to a service
// (GET|POST /v1/component-callbacks/{component}/{path...}): open (the
// service checks its own state), the call signed like every other.
func (s *Service) handleComponentCallback(w http.ResponseWriter, r *http.Request) {
	rec, found, err := s.findComponentRecord(r.Context(), extreg.KindService, r.PathValue("component"))
	switch {
	case err != nil:
		obs.ServerError(w, "load services failed", err)
		httpx.Error(w, "load services failed", http.StatusInternalServerError)
		return
	case !found || !servicecomp.Declares(rec, component.ServiceCallbacks):
		httpx.Error(w, "callback not found", http.StatusNotFound)
		return
	}
	body, ok := readServiceBody(w, r)
	if !ok {
		return
	}
	origin := s.publicOrigin(r)
	cb := component.Callback{Origin: origin, URL: origin + r.URL.EscapedPath()}
	ans, ok := s.forwardToService(w, r, rec, servicecomp.Call{
		Method: r.Method, Path: component.ServiceCallbackPrefix + forwardedPath(r, component.ComponentCallbackPath),
		IdentityHeader: component.CallbackHeader, Identity: component.EncodeHeader(cb),
		ContentType: r.Header.Get("Content-Type"), Accept: r.Header.Get("Accept"), Body: body,
	}, auth.Claims{})
	if !ok {
		return
	}
	writeServiceAnswer(w, ans)
}

// errServiceNoCredential: a service installed without its credential.
var errServiceNoCredential = errors.New("the service has no credential")
