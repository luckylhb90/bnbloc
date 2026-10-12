package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

const componentTrustedKeysKey = "component.trustedKeys"

func (s *Service) handleListComponents(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
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

func (s *Service) handleInstallComponent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
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
		ArtifactBaseURL: s.publicOrigin(r),
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

func (s *Service) handleUninstallComponent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
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

func (s *Service) handleSetComponentRollout(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var req setRolloutRequest
	if !httpx.DecodeJSON(w, r, 16*1024, &req) {
		return
	}
	if !extpipe.ValidRollout(req.Rollout) {
		httpx.Error(w, "rollout must be auto, optin, or off", http.StatusBadRequest)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
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

func (s *Service) handleSetComponentTenants(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var req setTenantsRequest
	if !httpx.DecodeJSON(w, r, 64*1024, &req) {
		return
	}
	tenants, err := req.scope()
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Platform content shares the route and its semantics (?kind=expert|
	// skill|prompt picks it; a bare name held by both registries is a 409).
	c, ok := s.requestComponent(w, r, r.PathValue("name"), true)
	if !ok {
		return
	}
	if c.content != nil {
		s.setPlatformContentTenants(w, r, claims, *c.content, tenants)
		return
	}
	rec := c.record
	pipe := s.corePipeline()
	kind := componentKindForName(rec)
	if tenants == nil {
		err = pipe.ClearTenants(r.Context(), kind, rec.Name, claims.Subject)
	} else {
		err = pipe.SetTenants(r.Context(), kind, rec.Name, tenants, claims.Subject)
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
	kinds := make([]string, 0, len(components))
	for _, c := range components {
		kinds = append(kinds, c.Kind)
	}
	counts, err := s.componentInstallCounts(ctx, kinds)
	if err != nil {
		return err
	}
	total := 0
	if s.MemberCount != nil {
		total = s.MemberCount()
	}
	for i := range components {
		components[i].Adoption = componentAdoption{MemberInstalls: counts[componentKey(components[i].Kind, components[i].Name)], TotalMembers: total}
	}
	return nil
}

// componentInstallCounts is the one count of members' opt-in installs, by
// componentKey, for the given kinds (each read once): the platform list's
// adoption and the member catalog's memberInstalls both read it.
func (s *Service) componentInstallCounts(ctx context.Context, kinds []string) (map[string]int, error) {
	out := map[string]int{}
	if s.ComponentInstalls == nil {
		return out, nil
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		counts, err := s.ComponentInstalls.CountComponentInstalls(ctx, kind)
		if err != nil {
			return nil, err
		}
		for name, n := range counts {
			out[componentKey(kind, name)] = n
		}
	}
	return out, nil
}
