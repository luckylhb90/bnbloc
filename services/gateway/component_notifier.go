package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
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

// componentInstallResponse is the install answer: the installed component
// plus, for a notifier installed for the first time, its signing secret —
// shown this once and never returned by any list.
type componentInstallResponse struct {
	installedComponent
	Secret string `json:"secret,omitempty"`
}

func (s *Service) notifierSecrets() *notify.SecretStore {
	return &notify.SecretStore{Settings: s.PlatformSettings}
}

func isNotifier(rec extpipe.Record) bool { return rec.Kind == string(extreg.KindNotifier) }

// afterNotifierInstall generates the signing secret of a newly installed
// notifier (an upgrade keeps the existing one) and returns it when created.
func (s *Service) afterNotifierInstall(ctx context.Context, rec extpipe.Record, actor string) (string, error) {
	if !isNotifier(rec) {
		return "", nil
	}
	secret, created, err := s.notifierSecrets().Ensure(ctx, rec.Name, actor)
	if err != nil || !created {
		return "", err
	}
	return secret, nil
}

// afterNotifierUninstall deletes an uninstalled notifier's signing secret and
// its reply tokens.
func (s *Service) afterNotifierUninstall(ctx context.Context, rec extpipe.Record) {
	if !isNotifier(rec) {
		return
	}
	if err := s.notifierSecrets().Delete(ctx, rec.Name); err != nil {
		slog.Warn("bnbloc-gateway: delete notifier secret failed", "notifier", rec.Name, "error", err)
	}
	// Its reply tokens die with it: a later install under the same name
	// must not answer notifications delivered before.
	if s.NotifierReplies != nil {
		if err := s.NotifierReplies.DeleteNotificationReplyTokens(ctx, rec.Name); err != nil {
			slog.Warn("bnbloc-gateway: delete notifier reply tokens failed", "notifier", rec.Name, "error", err)
		}
	}
}

// requestNotifierRecord resolves {name} (with ?kind=) to an installed
// notifier, writing the error response otherwise.
func (s *Service) requestNotifierRecord(w http.ResponseWriter, r *http.Request) (extpipe.Record, bool) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpx.Error(w, "component name required", http.StatusBadRequest)
		return extpipe.Record{}, false
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return extpipe.Record{}, false
	}
	if !isNotifier(rec) {
		httpx.Error(w, "component is not a notifier", http.StatusBadRequest)
		return extpipe.Record{}, false
	}
	return rec, true
}

// handleRotateComponentSecret replaces a notifier's signing secret and
// returns the new one once (POST /v1/platform/components/{name}/secret/rotate).
// The receiver accepts a comma list of secrets, so an operator adds the new
// one beside the old before removing it.
func (s *Service) handleRotateComponentSecret(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, ok := s.requestNotifierRecord(w, r)
	if !ok {
		return
	}
	secret, err := s.notifierSecrets().Rotate(r.Context(), rec.Name, claims.Subject)
	if err != nil {
		obs.ServerError(w, "rotate secret failed", err)
		httpx.Error(w, "rotate secret failed", http.StatusInternalServerError)
		return
	}
	s.auditNotifier(r.Context(), rec, "rotate-secret", "allow", claims.Subject, "")
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"secret": secret})
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
	secret, found, err := s.notifierSecrets().Get(r.Context(), rec.Name)
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
	outcome := "delivered"
	if !res.Delivered {
		outcome = "failed"
	}
	s.auditNotifier(r.Context(), rec, "test", outcome, claims.Subject, strings.TrimSpace(res.EventID+" "+res.Error))
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Service) auditNotifier(ctx context.Context, rec extpipe.Record, action, outcome, actor, detail string) {
	if s.ExtAudit == nil {
		return
	}
	_ = s.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: rec.Kind, Name: rec.Name, Action: action, Actor: actor, Outcome: outcome, Detail: detail, AtMs: s.nowTime().UnixMilli(),
	})
}

// errNotifierSecret reports a notifier installed without its secret stored.
var errNotifierSecret = errors.New("save notifier secret failed")
