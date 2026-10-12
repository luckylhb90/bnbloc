package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

const componentTrustedKeysKey = "component.trustedKeys"

func (s *Service) handlePlatformComponents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListComponents(w, r)
	case http.MethodPost:
		s.handleInstallComponent(w, r)
	default:
		httpx.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) handleListComponents(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	components, err := s.listInstalledComponents(r.Context())
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	if err := s.addComponentAdoption(r.Context(), components); err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"components": components})
}

func (s *Service) handleInstallComponent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	var req componentInstallRequest
	if !httpx.DecodeJSON(w, r, 512*1024, &req) {
		return
	}
	if req.Rollout != "" && !extpipe.ValidRollout(req.Rollout) {
		httpx.Error(w, "rollout must be auto, optin, or off", http.StatusBadRequest)
		return
	}
	// A content component's signed platform package installs into the
	// content tables (content_platform.go), not as an extension record.
	var shell struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(req.Package), &shell) == nil && contentpkg.IsContentKind(shell.Kind) {
		s.installPlatformContent(w, r, claims, req)
		return
	}
	rec, err := s.corePipeline().Install(r.Context(), extpipe.InstallRequest{
		Body:            []byte(req.Package),
		Signature:       []byte(req.Signature),
		Actor:           claims.Subject,
		Rollout:         req.Rollout,
		ArtifactBaseURL: publicBaseURL(r),
		ArtifactProbe:   s.probeUploadedArtifacts,
	})
	if err != nil {
		if errors.Is(err, extpipe.ErrTrustedKeys) {
			obs.ServerError(w, "load trusted publisher keys failed", err)
			httpx.Error(w, "load trusted publisher keys failed", http.StatusInternalServerError)
			return
		}
		if errors.Is(err, extpipe.ErrPersist) {
			obs.ServerError(w, "save component failed", err)
			httpx.Error(w, "save component failed", http.StatusInternalServerError)
			return
		}
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	component, err := componentFromExtRecord(rec)
	if err != nil {
		obs.ServerError(w, "save component failed", err)
		httpx.Error(w, "save component failed", http.StatusInternalServerError)
		return
	}
	secret, err := s.afterNotifierInstall(r.Context(), rec, claims.Subject)
	if err != nil {
		obs.ServerError(w, "install component failed", err)
		httpx.Error(w, errNotifierSecret.Error(), http.StatusInternalServerError)
		return
	}
	s.componentsChanged(r.Context(), "", "")
	httpx.WriteJSON(w, http.StatusOK, componentInstallResponse{installedComponent: component, Secret: secret})
}

func publicBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(forwarded, ",")[0]))
	}
	host := r.Host
	return scheme + "://" + host
}

func (s *Service) handleUninstallComponent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpx.Error(w, "component name required", http.StatusBadRequest)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return
	}
	if err := s.corePipeline().Uninstall(r.Context(), componentKindForName(rec), rec.Name, claims.Subject); err != nil {
		if errors.Is(err, extpipe.ErrSettingsStore) || errors.Is(err, extpipe.ErrPersist) {
			obs.ServerError(w, "delete component failed", err)
			httpx.Error(w, "delete component failed", http.StatusInternalServerError)
			return
		}
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.afterNotifierUninstall(r.Context(), rec)
	s.afterMCPUninstall(r.Context(), rec)
	s.componentsChanged(r.Context(), "", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSetComponentRollout(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpx.Error(w, "component name required", http.StatusBadRequest)
		return
	}
	var req setRolloutRequest
	if !httpx.DecodeJSON(w, r, 16*1024, &req) {
		return
	}
	if !extpipe.ValidRollout(req.Rollout) {
		httpx.Error(w, "rollout must be auto, optin, or off", http.StatusBadRequest)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return
	}
	if err := s.corePipeline().SetRollout(r.Context(), componentKindForName(rec), rec.Name, req.Rollout, claims.Subject); err != nil {
		if errors.Is(err, extpipe.ErrSettingsStore) || errors.Is(err, extpipe.ErrPersist) {
			obs.ServerError(w, "set rollout failed", err)
			httpx.Error(w, "set rollout failed", http.StatusInternalServerError)
			return
		}
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.componentsChanged(r.Context(), "", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSetComponentTenants(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpx.Error(w, "component name required", http.StatusBadRequest)
		return
	}
	var req setTenantsRequest
	if !httpx.DecodeJSON(w, r, 64*1024, &req) {
		return
	}
	// Explicit semantics, fail closed: allTenants lifts the restriction (and
	// must not be combined with a list); otherwise enabledTenants must name at
	// least one tenant — an empty list is rejected, never read as "all".
	if req.AllTenants && len(req.EnabledTenants) > 0 {
		httpx.Error(w, "allTenants and enabledTenants are mutually exclusive", http.StatusBadRequest)
		return
	}
	// Platform content shares the route and its semantics (?kind=expert|
	// skill|prompt picks it; a bare name held by both registries is a 409).
	if c, ok, handled := s.platformContentByName(w, r, name); handled {
		if ok {
			s.setPlatformContentTenants(w, r, claims, c, req)
		}
		return
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return
	}
	pipe := s.corePipeline()
	kind := componentKindForName(rec)
	var err error
	switch {
	case req.AllTenants:
		err = pipe.ClearTenants(r.Context(), kind, rec.Name, claims.Subject)
	default:
		err = pipe.SetTenants(r.Context(), kind, rec.Name, req.EnabledTenants, claims.Subject)
	}
	if err != nil {
		if errors.Is(err, extpipe.ErrSettingsStore) || errors.Is(err, extpipe.ErrPersist) {
			obs.ServerError(w, "set tenants failed", err)
			httpx.Error(w, "set tenants failed", http.StatusInternalServerError)
			return
		}
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.componentsChanged(r.Context(), "", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) listInstalledComponents(ctx context.Context) ([]installedComponent, error) {
	recs, err := s.corePipeline().List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]installedComponent, 0, len(recs))
	for _, rec := range recs {
		component, err := componentFromExtRecord(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, component)
	}
	return out, nil
}

func (s *Service) addComponentAdoption(ctx context.Context, components []installedComponent) error {
	countsByKind := map[string]map[string]int{}
	if s.ComponentInstalls != nil {
		for _, component := range components {
			if _, ok := countsByKind[component.Kind]; ok {
				continue
			}
			counts, err := s.ComponentInstalls.CountComponentInstalls(ctx, component.Kind)
			if err != nil {
				return err
			}
			countsByKind[component.Kind] = counts
		}
	}
	total := 0
	if s.MemberCount != nil {
		total = s.MemberCount()
	}
	for i := range components {
		count := countsByKind[components[i].Kind][components[i].Name]
		components[i].MemberInstalls = count
		components[i].TotalMembers = total
		components[i].Adoption = componentAdoption{MemberInstalls: count, TotalMembers: total}
	}
	return nil
}
