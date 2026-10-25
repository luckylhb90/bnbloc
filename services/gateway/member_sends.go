package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/servicecomp"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Sends from a member's computer, outside a turn
// (docs/prd/2026-10-25-member-sends.md): a member's action on a service's
// page grants it one send (componentAct); the service asks for it
// (ComponentSendPath); the member's own bridge takes it with its claim
// (activity) and runs the egress protocol through SendProxyPath, the send
// attested in component.SendHeader.

// componentAct is the member's one-time grant to service name, for a call
// that is the member's action (component.Member.Act).
func (s *Service) componentAct(claims auth.Claims, name string) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return s.Signer.Issue(auth.Claims{
		Subject: claims.Subject, Tenant: claims.Tenant,
		ExpiresAtUnix: s.nowTime().Add(component.MaxSendGrantAge).Unix(),
		Kind:          auth.KindComponentAct, ID: name, Data: hex.EncodeToString(nonce),
	})
}

// sendID is a grant's one send: the same grant always names the same send,
// so a second send with it is refused.
func sendID(act string) string {
	sum := sha256.Sum256([]byte(act))
	return hex.EncodeToString(sum[:16])
}

// maxSendURL bounds a send's address.
const maxSendURL = 4096

// handleComponentSend queues a send a service asks for with a member's
// grant (POST component.ComponentSendPath, signed by the service).
func (s *Service) handleComponentSend(w http.ResponseWriter, r *http.Request) {
	kind, name := componentCaller(r)
	if kind != extreg.KindService {
		httpx.Error(w, component.ComponentHeader+" must name a service (service/<name>)", http.StatusUnauthorized)
		return
	}
	body, ok := readBody(w, r, 16<<10)
	if !ok {
		return
	}
	if _, ok := s.verifyComponentCall(w, r, kind, name, component.SignedRequest(r.Method, r.URL.EscapedPath(), "", body)); !ok {
		return
	}
	var req component.ServiceSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Error(w, "the body is a ServiceSendRequest", http.StatusBadRequest)
		return
	}
	member, err := s.Signer.VerifyKind(req.Act, auth.KindComponentAct)
	if err != nil || member.ID != name {
		httpx.Error(w, "the member's grant is not valid: it expired, or it is another component's", http.StatusForbidden)
		return
	}
	u, err := url.Parse(req.URL)
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(req.URL) > maxSendURL {
		httpx.Error(w, "a send is a GET or HEAD of an https address", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if _, ok := s.serviceFor(w, ctx, member.Tenant, name, component.ServiceEgress); !ok {
		return
	}
	if s.BridgeSends == nil {
		httpx.Unavailable(w, "sends from a member's computer")
		return
	}
	id := sendID(req.Act)
	err = s.BridgeSends.PutBridgeSend(tenant.NewContext(ctx, member.Tenant),
		store.BridgeSend{SendID: id, OwnerUserID: member.Subject, Component: name, Method: req.Method, URL: u.String()}, s.nowTime().UnixMilli())
	switch {
	case errors.Is(err, store.ErrNoLiveBridge):
		httpx.Error(w, "none of "+member.Subject+"'s bnbloc desktop apps is running", http.StatusConflict)
		return
	case errors.Is(err, store.ErrBridgeSendExists):
		httpx.Error(w, "the member's grant was used", http.StatusForbidden)
		return
	case err != nil:
		obs.ServerError(w, "queue the send failed", err)
		httpx.Error(w, "queue the send failed", http.StatusInternalServerError)
		return
	}
	s.audit(ctx, member.Tenant, store.ExtAuditRecord{Kind: string(extreg.KindService), Name: name, Action: "send", Actor: member.Subject,
		Outcome: wire.AuditOutcomeAllow, Detail: req.Method + " " + u.Host + u.EscapedPath() + " from the member's computer"})
	httpx.WriteJSON(w, http.StatusOK, component.ServiceSendReply{ID: id})
}

// handleSendProxy forwards the member's bridge's egress call for a send it
// took (POST component.SendProxyPath{send}) to the service's
// ServiceSendEgressPath, the send attested in component.SendHeader. The
// request is the queued one: the bridge's method and address are replaced.
func (s *Service) handleSendProxy(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	if s.BridgeSends == nil {
		httpx.Unavailable(w, "sends from a member's computer")
		return
	}
	ctx := r.Context()
	send, err := s.BridgeSends.HeldBridgeSend(ctx, r.PathValue("send"), caller.Subject, s.nowTime().UnixMilli())
	switch {
	case errors.Is(err, store.ErrBridgeSendNotFound):
		httpx.Error(w, "no such send on your bridge", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "read the send failed", err)
		httpx.Error(w, "read the send failed", http.StatusInternalServerError)
		return
	}
	rec, ok := s.serviceFor(w, ctx, send.TenantID, send.Component, component.ServiceEgress)
	if !ok {
		return
	}
	raw, ok := readServiceBody(w, r)
	if !ok {
		return
	}
	var in component.EgressRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "the body is an EgressRequest", http.StatusBadRequest)
		return
	}
	in.Method, in.URL, in.Body = send.Method, send.URL, nil
	raw, _ = json.Marshal(in)
	ans, ok := s.forwardToService(w, r, rec, servicecomp.Call{
		Method: http.MethodPost, Path: component.ServiceSendEgressPath,
		IdentityHeader: component.SendHeader,
		Identity:       component.EncodeHeader(component.Send{Org: send.TenantID, Member: send.OwnerUserID, ID: send.SendID, Origin: s.publicOrigin(r)}),
		ContentType:    "application/json", Accept: "application/json", Body: raw,
	}, auth.Claims{Subject: send.OwnerUserID, Tenant: send.TenantID})
	if !ok {
		return
	}
	writeServiceAnswer(w, ans)
}
