package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A group chat connects to a room (docs/prd/2026-10-15-app-identity-
// schedules-notify-ai.md §5a): POST /v1/notifier/group-binds. A notifier
// whose package declares the groups capability saw a room's connect code
// posted in a group chat its bot is in, and reports {code, address}: the
// code as typed and its own address of that chat. The call is signed like
// a reply (verifyNotifierCall); the room service spends the code and
// records the connection, which owns everything else.
func (s *Service) handleNotifierGroupBind(w http.ResponseWriter, r *http.Request) {
	rec, body, ok := s.verifyNotifierCall(w, r)
	if !ok {
		return
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil || !containsString(cfg.Capabilities, pkgmeta.NotifierCapabilityGroups) {
		httpx.Error(w, "this notifier does not declare the groups capability", http.StatusForbidden)
		return
	}
	var in wire.NotifierGroupBindRequest
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(in)
	var out wire.NotifierGroupBindReply
	err = s.roomBackendDo(r.Context(), backendHTTP, http.MethodPost, "/internal/v1/group-binds?notifier="+url.QueryEscape(rec.Name), "application/json", raw, gatewayServiceSubject, "", &out)
	var rbe *roomBackendError
	switch {
	case errors.As(err, &rbe) && rbe.Status < http.StatusInternalServerError:
		httpx.WriteStatus(w, rbe.Status, rbe.Body, err)
		return
	case err != nil:
		obs.ServerError(w, "bind the group chat failed", err)
		httpx.Error(w, "bind the group chat failed", http.StatusBadGateway)
		return
	}
	s.audit(r.Context(), "", store.ExtAuditRecord{Kind: wire.AuditKindApp, Name: rec.Name, Action: "group-bind", Actor: rec.Name,
		Outcome: wire.AuditOutcomeAllow, Detail: "group " + out.Name + " connected"})
	httpx.WriteJSON(w, http.StatusOK, out)
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
