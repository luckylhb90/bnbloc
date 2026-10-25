package gateway

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/egress"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The platform console's Network card (docs/architecture/server-egress.md):
// where the platform's own server-side requests may go, internal/egress.

func (s *Service) handleServerEgress(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	p, err := egress.Load(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load the server egress failed", err)
		httpx.Error(w, "load the server egress failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Service) handlePutServerEgress(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var p wire.ServerEgress
	if !httpx.DecodeJSON(w, r, 64<<10, &p) {
		return
	}
	saved, err := egress.Save(r.Context(), s.PlatformSettings, p, claims.Subject, s.nowTime().UnixMilli())
	switch {
	case errors.Is(err, egress.ErrInvalid):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		obs.ServerError(w, "save the server egress failed", err)
		httpx.Error(w, "save the server egress failed", http.StatusInternalServerError)
		return
	}
	detail := saved.Mode
	if len(saved.Hosts) > 0 {
		detail += ", hosts " + strings.Join(saved.Hosts, ", ")
	}
	s.audit(r.Context(), "", store.ExtAuditRecord{Kind: wire.AuditKindNetwork, Name: "server-egress", Action: "egress-set",
		Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow, Detail: detail})
	httpx.WriteJSON(w, http.StatusOK, saved)
}
