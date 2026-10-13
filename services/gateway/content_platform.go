package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform content (docs/prd/2026-10-12-experts-and-teams.md §1, step 4):
// a super admin promotes any content component version to every
// organization through the existing signed install. Signing stays offline:
//
//	GET  /v1/platform/content-components[?scope=platform|org]   platform content, and org content across organizations to promote
//	GET  /v1/platform/content-components/{id}/package[?version=&publisher=]   the signable package of a version
//	POST /v1/platform/components   {package, signature}   (the existing install; a content kind lands here)
//	POST /v1/platform/content-components/{id}/unpublish | /restore
//	DELETE /v1/platform/content-components/{id}
//
// `bnbloc content export` fetches the package, `bnbloc cores install
// --package … --signature …` installs it signed, and `bnbloc content
// promote` does both with the publisher key. The platform row has no
// tenant and no owner; its version carries the signature.

// handleListPlatformContent lists platform content and, to promote,
// organization content across organizations (any status).
func (s *Service) handleListPlatformContent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var scopes []store.ContentScope
	switch scope := r.URL.Query().Get("scope"); scope {
	case "":
		scopes = []store.ContentScope{store.ContentScopePlatform, store.ContentScopeOrg}
	case wire.ContentScopePlatform, wire.ContentScopeOrg:
		scopes = []store.ContentScope{store.ContentScope(scope)}
	default:
		httpx.Error(w, "scope must be platform or org", http.StatusBadRequest)
		return
	}
	comps, err := s.ContentComponents.ListContentComponents(r.Context(), store.ContentComponentQuery{Scopes: scopes})
	if err != nil {
		obs.ServerError(w, "list platform content failed", err)
		httpx.Error(w, "list platform content failed", http.StatusInternalServerError)
		return
	}
	s.writeAdminContent(w, r, claims, comps)
}

// handleExportContentPackage answers the signable package of one version
// of any stored content component (a built-in already ships everywhere).
// The response bytes are exactly what gets signed.
func (s *Service) handleExportContentPackage(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	id := r.PathValue("id")
	if contentbuiltin.IsBuiltinID(id) {
		httpx.Error(w, "built-in content already reaches every organization", http.StatusConflict)
		return
	}
	c, err := s.ContentComponents.GetContentComponent(ctx, id)
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	q := r.URL.Query()
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(q.Get("version"), c.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	if v.BundleBlobKey != digest.Key(v.SHA256) {
		httpx.Error(w, "this version's bundle is not in the gateway artifact store", http.StatusConflict)
		return
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	publisher := firstNonEmpty(q.Get("publisher"), "self")
	pkg := contentpkg.PlatformPackage(m, v.SHA256, pkgmeta.GatewayArtifactURL(s.publicOrigin(r), v.BundleBlobKey), publisher)
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		obs.ServerError(w, "encode content package failed", err)
		httpx.Error(w, "encode content package failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.package.json"`, m.Name, m.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}

// contentInstallReply answers a platform content install (the CLI reads
// name).
type contentInstallReply struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
	Status  string `json:"status"`
}

// installPlatformContent installs a signed content package (POST
// /v1/platform/components with a content kind): the signature is checked
// against the trusted keys, the bundle it pins is read from the artifact
// store, validated again and must be the version the package names; the
// platform row is created or moved forward to it. Rollout off installs it
// unpublished; content has no opt-in rollout.
func (s *Service) installPlatformContent(w http.ResponseWriter, r *http.Request, claims auth.Claims, req wire.ComponentInstallRequest) {
	ctx := r.Context()
	if s.ContentComponents == nil {
		obs.ServerError(w, "content components unavailable", nil)
		httpx.Error(w, "content components unavailable", http.StatusServiceUnavailable)
		return
	}
	status := store.ContentStatusActive
	switch extpipe.NormalizeRollout(req.Rollout) {
	case extpipe.RolloutOptIn:
		httpx.Error(w, "content has no opt-in rollout: members pick experts and turn packs on themselves (use auto or off)", http.StatusBadRequest)
		return
	case extpipe.RolloutOff:
		status = store.ContentStatusRetired
	}
	pkg, keyID, err := s.corePipeline().VerifyPackage(ctx, []byte(req.Package), []byte(req.Signature))
	if errors.Is(err, extpipe.ErrTrustedKeys) {
		obs.ServerError(w, "load trusted publisher keys failed", err)
		httpx.Error(w, "load trusted publisher keys failed", http.StatusInternalServerError)
		return
	}
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := pkgmeta.ValidateGatewayArtifactURLs(pkg, s.publicOrigin(r)); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sha := pkg.Artifacts[contentpkg.PlatformBundleArtifact].SHA256
	key := digest.Key(sha)
	raw, err := s.readArtifactBlob(ctx, key, contentpkg.BundleLimits().MaxArchiveBytes())
	if err != nil {
		httpx.Error(w, "read the package's bundle: "+err.Error(), http.StatusBadRequest)
		return
	}
	cpkg, err := contentpkg.Validate(raw, sha, contentOptions())
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := cpkg.Manifest
	if m.Kind != pkg.Kind || m.Name != pkg.Name || m.Version != pkg.Version {
		httpx.Error(w, fmt.Sprintf("the bundle is %s %s %s, the package names %s %s %s", shortKind(m.Kind), m.Name, m.Version, shortKind(pkg.Kind), pkg.Name, pkg.Version), http.StatusBadRequest)
		return
	}
	now := s.nowTime().UnixMilli()
	ver, err := contentVersionRow(cpkg, key, len(raw), claims.Subject, now)
	if err != nil {
		obs.ServerError(w, "encode content manifest failed", err)
		httpx.Error(w, "encode content manifest failed", http.StatusInternalServerError)
		return
	}
	ver.Signature = strings.TrimSpace(req.Signature)
	c, err := s.putPlatformContent(r, m, ver, status)
	var conflict platformConflict
	switch {
	case errors.As(err, &conflict):
		httpx.Error(w, conflict.Error(), http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "install platform content failed", err)
		httpx.Error(w, "install platform content failed", http.StatusInternalServerError)
		return
	}
	signed := "unsigned (dev)"
	if keyID != "" {
		signed = "signed by key " + keyID
	}
	s.auditContent(ctx, "install", claims.Subject, "", c, m.Version, contentshare.Label(c)+" v"+m.Version+" installed for every organization by "+claims.Subject+", "+signed+", publisher "+pkg.Publisher)
	httpx.WriteJSON(w, http.StatusOK, contentInstallReply{ID: c.ID, Name: c.Name, Kind: shortKind(c.Kind), Version: m.Version, Scope: string(c.Scope), Status: string(c.Status)})
}

// platformConflict is an install that would rewrite an installed version.
type platformConflict struct{ msg string }

func (p platformConflict) Error() string { return p.msg }

// putPlatformContent stores a verified version as platform content: a new
// platform row, or the existing one (same kind and name) moved forward. A
// version installed again with the same bundle only sets the status.
func (s *Service) putPlatformContent(r *http.Request, m contentpkg.Manifest, ver store.ContentComponentVersion, status store.ContentStatus) (store.ContentComponent, error) {
	ctx := r.Context()
	now := ver.CreatedAtMs
	for attempt := 0; attempt < 2; attempt++ {
		rows, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{Kind: m.Kind, Scopes: []store.ContentScope{store.ContentScopePlatform}})
		if err != nil {
			return store.ContentComponent{}, err
		}
		var cur *store.ContentComponent
		for i := range rows {
			if rows[i].Name == m.Name && rows[i].TenantID == "" && rows[i].Owner == "" {
				cur = &rows[i]
			}
		}
		if cur == nil {
			c := store.ContentComponent{ID: ids.New("cc_"), Kind: m.Kind, Name: m.Name, Scope: store.ContentScopePlatform, Status: status,
				DisplayName: m.DisplayName, Description: m.Description, Category: m.Category, CreatedAtMs: now, UpdatedAtMs: now}
			switch err := s.ContentComponents.CreateContentComponent(ctx, c, ver); {
			case errors.Is(err, store.ErrContentExists):
				continue
			case err != nil:
				return store.ContentComponent{}, err
			}
			c.LatestVersion = ver.Version
			return c, nil
		}
		c := *cur
		if have, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, ver.Version); err == nil {
			if have.SHA256 != ver.SHA256 {
				return store.ContentComponent{}, platformConflict{fmt.Sprintf("version %s is installed with other content: publish a newer version", ver.Version)}
			}
		} else if !errors.Is(err, store.ErrContentNotFound) {
			return store.ContentComponent{}, err
		} else {
			if cmp, err := semver.Compare(ver.Version, c.LatestVersion); err != nil || cmp <= 0 {
				return store.ContentComponent{}, platformConflict{fmt.Sprintf("version %s is not newer than the installed %s", ver.Version, c.LatestVersion)}
			}
			next := c
			next.DisplayName, next.Description, next.Category, next.UpdatedAtMs = m.DisplayName, m.Description, m.Category, now
			if err := s.ContentComponents.AddContentComponentVersion(ctx, next, c.LatestVersion, ver); err != nil {
				return store.ContentComponent{}, err
			}
			c = next
			c.LatestVersion = ver.Version
		}
		if c.Status != status {
			if err := s.ContentComponents.SetContentComponentStatus(ctx, c.ID, status, now); err != nil {
				return store.ContentComponent{}, err
			}
			c.Status = status
		}
		return c, nil
	}
	return store.ContentComponent{}, fmt.Errorf("install %s: platform content changed concurrently", m.Name)
}

// platformContent loads a platform row.
func (s *Service) platformContent(w http.ResponseWriter, r *http.Request) (store.ContentComponent, bool) {
	c, err := s.ContentComponents.GetContentComponent(r.Context(), r.PathValue("id"))
	if err == nil && c.Scope != store.ContentScopePlatform {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return store.ContentComponent{}, false
	}
	return c, true
}

func (s *Service) handlePlatformContentStatus(status store.ContentStatus) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		c, ok := s.platformContent(w, r)
		if !ok {
			return
		}
		s.setContentStatus(w, r, claims, c, status)
	}
}

func (s *Service) handleRemovePlatformContent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	c, ok := s.platformContent(w, r)
	if !ok {
		return
	}
	s.removeContent(w, r, claims, c)
}

// setPlatformContentTenants limits platform content to tenants or lifts the
// limit (nil; setTenantsRequest.scope checked the request). Every member surface reads it through extpipe.MemberVisibility.
func (s *Service) setPlatformContentTenants(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent, tenants []string) {
	if err := s.ContentComponents.SetContentComponentTenants(r.Context(), c.ID, tenants, s.nowTime().UnixMilli()); err != nil {
		contentStoreErr(w, "set tenants failed", err)
		return
	}
	scope := "every organization"
	if tenants != nil {
		scope = strings.Join(tenants, ", ")
	}
	s.auditContent(r.Context(), "set-tenants", claims.Subject, "", c, c.LatestVersion, contentshare.Label(c)+" limited to "+scope+" by "+claims.Subject)
	w.WriteHeader(http.StatusNoContent)
}
