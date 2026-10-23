package gateway

import (
	"context"
	"errors"
	"github.com/luckylhb90/bnbloc/component"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/componentcred"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An endpoint component beside the gateway (a notifier, an auth provider)
// holds one credential (internal/componentcred): created when it is
// installed and shown once, rotated by a super admin, deleted with it. It
// signs the component's calls to the gateway (verifyComponentCall) and,
// for a notifier, bnbloc's deliveries to it.

// componentCredentials is the credential store (sealed with the
// deployment's secret store key).
func (s *Service) componentCredentials() *componentcred.Store {
	return &componentcred.Store{Settings: s.PlatformSettings, Box: s.SecretBox, Now: s.nowTime}
}

// errComponentCredential reports an endpoint component installed without
// its credential stored.
var errComponentCredential = errors.New("save component credential failed")

// afterEndpointInstall gives a newly installed endpoint component its
// credential (an upgrade keeps the existing one, or gets its first) and
// returns it when created, to be shown once.
func (s *Service) afterEndpointInstall(ctx context.Context, rec extpipe.Record, actor string) (string, error) {
	if !componentcred.Holds(rec.Kind) {
		return "", nil
	}
	secret, created, err := s.componentCredentials().Ensure(ctx, rec.Kind, rec.Name, actor)
	if err != nil || !created {
		return "", err
	}
	return secret, nil
}

// afterEndpointUninstall deletes an uninstalled endpoint component's
// credential, and a notifier's reply tokens.
func (s *Service) afterEndpointUninstall(ctx context.Context, rec extpipe.Record) {
	if !componentcred.Holds(rec.Kind) {
		return
	}
	if err := s.componentCredentials().Delete(ctx, rec.Kind, rec.Name); err != nil {
		slog.Warn("bnbloc-gateway: delete component credential failed", "kind", rec.Kind, "component", rec.Name, "error", err)
	}
	// A service's values go with it.
	if rec.Kind == string(extreg.KindService) && s.ComponentValues != nil {
		if err := s.ComponentValues.DeleteComponentValues(ctx, rec.Kind+"/"+rec.Name); err != nil {
			slog.Warn("bnbloc-gateway: delete service values failed", "service", rec.Name, "error", err)
		}
	}
	// A notifier's reply tokens die with it: a later install under the same
	// name must not answer notifications delivered before.
	if isNotifier(rec) && s.NotifierReplies != nil {
		if err := s.NotifierReplies.DeleteNotificationReplyTokens(ctx, rec.Name); err != nil {
			slog.Warn("bnbloc-gateway: delete notifier reply tokens failed", "notifier", rec.Name, "error", err)
		}
	}
}

// handleRotateComponentSecret replaces an endpoint component's credential
// and returns the new one once (POST /v1/platform/components/{name}/secret/rotate).
// A notifier's receiver accepts a comma list of secrets, so an operator adds
// the new one beside the old before removing it; an auth provider's calls
// fail from the rotation until it holds the new one (its settings then
// come from its fallback until then).
func (s *Service) handleRotateComponentSecret(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if !componentcred.Holds(rec.Kind) {
		httpx.Error(w, "a "+rec.Kind+" component has no credential", http.StatusBadRequest)
		return
	}
	secret, err := s.componentCredentials().Rotate(r.Context(), rec.Kind, rec.Name, claims.Subject)
	switch {
	case errors.Is(err, componentcred.ErrOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "rotate secret failed", err)
		httpx.Error(w, "rotate secret failed", http.StatusInternalServerError)
		return
	}
	s.auditComponent(r.Context(), claims, rec, "rotate-secret", wire.AuditOutcomeAllow, "")
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

// verifyComponentCall authenticates an endpoint component's signed call:
// the installed component kind/name must hold a credential, and
// X-Bnbloc-Signature must verify over signed (the raw body of a POST, the
// canonical "<METHOD> <path>" of a GET: componentCallSigned) in constant
// time within notify.SignatureTolerance. It answers the refusal itself
// (401 no or bad signature, 403 not installed) and returns ok=false then.
// It is the one check of every component-signed route.
func (s *Service) verifyComponentCall(w http.ResponseWriter, r *http.Request, kind extreg.Kind, name string, signed []byte) (extpipe.Record, bool) {
	ctx := r.Context()
	if name == "" || !componentcred.Holds(string(kind)) {
		httpx.Error(w, component.ComponentHeader+" required (the component's kind and name)", http.StatusUnauthorized)
		return extpipe.Record{}, false
	}
	rec, found, err := s.findComponentRecord(ctx, kind, name)
	switch {
	case err != nil:
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return extpipe.Record{}, false
	case !found:
		httpx.Error(w, "component not installed", http.StatusForbidden)
		return extpipe.Record{}, false
	}
	secret, ok, err := s.componentCredentials().Get(ctx, rec.Kind, rec.Name)
	switch {
	case err != nil:
		obs.ServerError(w, "read component credential failed", err)
		httpx.Error(w, "read component credential failed", http.StatusInternalServerError)
		return extpipe.Record{}, false
	case !ok:
		httpx.Error(w, "component has no credential; rotate one first", http.StatusUnauthorized)
		return extpipe.Record{}, false
	}
	if err := notify.Verify(r.Header.Get(wire.NotificationSignatureHeader), secret, signed, s.nowTime(), notify.SignatureTolerance); err != nil {
		httpx.Error(w, "invalid signature", http.StatusUnauthorized)
		return extpipe.Record{}, false
	}
	return rec, true
}

// componentCallSigned is what a bodyless component call signs in place of
// a body: "<METHOD> <path>" (no query), e.g. "GET /v1/component-settings".
func componentCallSigned(r *http.Request) []byte {
	return []byte(r.Method + " " + r.URL.Path)
}

// componentCaller reads X-Bnbloc-Component as "<kind>/<name>" (the
// endpoint routes any endpoint kind calls).
func componentCaller(r *http.Request) (extreg.Kind, string) {
	kind, name, _ := strings.Cut(strings.TrimSpace(r.Header.Get(component.ComponentHeader)), "/")
	return extreg.Kind(kind), name
}

func (s *Service) auditComponent(ctx context.Context, claims auth.Claims, rec extpipe.Record, action, outcome, detail string) {
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{
		Kind: rec.Kind, Name: rec.Name, Action: action, Actor: claims.Subject, Outcome: outcome, Detail: detail,
	})
}
