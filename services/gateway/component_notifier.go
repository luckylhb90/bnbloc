package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// notifierInstaller is the gateway's notifier lifecycle: the shared
// endpoint install path verifies, NetGuard-checks and probes the component
// before the record is persisted. Realtime hosts notifiers — its component
// pipeline activates the record on reconcile — so the gateway activates
// nothing.
func notifierInstaller() extpipe.KindInstaller {
	in := notify.Kind.Installer()
	in.Activate = func(context.Context, extpipe.Record, extgov.Guard) (any, error) { return nil, nil }
	return in
}

func isNotifier(rec extpipe.Record) bool { return rec.Kind == string(extreg.KindNotifier) }

// requestNotifierRecord resolves {name} (with ?kind=) to an installed
// notifier, writing the error response otherwise.
func (s *Service) requestNotifierRecord(w http.ResponseWriter, r *http.Request) (extpipe.Record, bool) {
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return extpipe.Record{}, false
	}
	if !isNotifier(rec) {
		httpx.Error(w, "component is not a notifier", http.StatusBadRequest)
		return extpipe.Record{}, false
	}
	return rec, true
}

// handleTestComponent sends one signed test notification through a notifier
// to the calling super-admin and reports the notifier's answer
// (POST /v1/platform/components/{name}/test).
func (s *Service) handleTestComponent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, ok := s.requestNotifierRecord(w, r)
	if !ok {
		return
	}
	ep, err := notify.Kind.Endpoint(rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusConflict)
		return
	}
	secret, found, err := s.componentCredentials().Get(r.Context(), rec.Kind, rec.Name)
	switch {
	case err != nil:
		obs.ServerError(w, "read notifier secret failed", err)
		httpx.Error(w, "read notifier secret failed", http.StatusInternalServerError)
		return
	case !found:
		httpx.Error(w, "notifier has no signing secret; rotate one first", http.StatusConflict)
		return
	}
	tid := claims.Tenant
	now := s.nowTime()
	res := notify.SendTest(r.Context(), ep, secret, notify.TestPayload(tid, claims.Subject, "", now), now)
	outcome := wire.AuditOutcomeDelivered
	if !res.Delivered {
		outcome = wire.AuditOutcomeFailed
	}
	s.auditComponent(r.Context(), claims, rec, "test", outcome, strings.TrimSpace(res.EventID+" "+res.Error))
	httpx.WriteJSON(w, http.StatusOK, res)
}
