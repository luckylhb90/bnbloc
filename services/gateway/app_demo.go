package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
)

// An app's "Try demo" (docs/prd/2026-10-13-app-gallery.md §7): the app
// component's site runs in the sandboxed site frame like any site, from a
// demo ticket in the member site path (/v1/sites/{ticket}/…, so the sites
// origin, the sandbox CSP and the desktop's site scheme apply unchanged),
// with the SDK in demo mode (appsdk.DemoJS): its data lives in the page's
// memory and is never sent.
//
// A demo can never reach a data space. A demo ticket (auth.KindAppDemo)
// names an app component version, not a room or a site artifact; the app
// data API under the ticket entry resolves only a member's site ticket
// (ticketApp, memberTicket), so every app data call under a demo ticket is
// a 404 before anything is forwarded, whatever the app's own code sends.

// appDemoTTL bounds a demo ticket: a demo is a short look, minted again
// for the next one.
const appDemoTTL = time.Hour

// appDemoScope is a demo ticket's ID: the component version it shows and
// the digest of its packed site.
func appDemoScope(componentID, version, siteSHA string) string {
	return componentID + "@" + version + "#" + siteSHA
}

func parseAppDemoScope(id string) (componentID, version, siteSHA string, ok bool) {
	rest, siteSHA, found := strings.Cut(id, "#")
	componentID, version, found2 := strings.Cut(rest, "@")
	if !found || !found2 || componentID == "" || version == "" || !digest.Valid(siteSHA) {
		return "", "", "", false
	}
	return componentID, version, siteSHA, true
}

// handleAppDemo is POST /v1/content-components/{id}/versions/{version}/demo:
// a demo ticket for an app version the caller may read (readableContent,
// the content routes' one read rule) → wire.AppDemo, its site URL on the
// sites origin when the deployment has one.
func (s *Service) handleAppDemo(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims, ctx := actor.Claims, r.Context()
	c, v, err := s.demoVersion(ctx, claims, r.PathValue("id"), r.PathValue("version"))
	if err != nil {
		contentStoreErr(w, "load the app failed", err)
		return
	}
	pack := s.appDemoPack(v)
	raw, err := pack(ctx)
	if err != nil {
		obs.ServerError(w, "pack the app's site failed", err)
		httpx.Error(w, "pack the app's site failed", http.StatusBadGateway)
		return
	}
	sha := digest.Of(raw)
	// The first file request is served from the cache this load fills.
	_, sites := s.bundleCaches()
	if _, err := sites.LoadPinned(sha, tarbundle.SiteLimits, func() ([]byte, error) { return raw, nil }); err != nil {
		obs.ServerError(w, "load the app's site failed", err)
		httpx.Error(w, "load the app's site failed", http.StatusInternalServerError)
		return
	}
	expires := s.nowTime().Add(appDemoTTL)
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        claims.Tenant,
		Role:          claims.Role,
		ExpiresAtUnix: expires.Unix(),
		Kind:          auth.KindAppDemo,
		ID:            appDemoScope(c.ID, v.Version, sha),
	})
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, wire.AppDemo{SiteURL: s.siteURL(memberSiteBase(ticket)), ExpiresAtMs: expires.UnixMilli()})
}

// demoVersion is an app work's version the claims may read; any other
// work or kind is a not-found (Try demo is an app's only).
func (s *Service) demoVersion(ctx context.Context, claims auth.Claims, id, version string) (store.ContentComponent, store.ContentComponentVersion, error) {
	c, err := s.readableContent(ctx, claims, id, "")
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, version)
	if err == nil && workTypeOf(c, v) != wire.WorkTypeApp {
		err = store.ErrContentNotFound
	}
	return c, v, err
}

// appDemoPack packs an app version's site (contentpkg.WorkSite, the files
// "Use this app" copies): deterministic, so a demo ticket pins its digest.
func (s *Service) appDemoPack(v store.ContentComponentVersion) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		pkg, err := s.contentPackage(ctx, v)
		if err != nil {
			return nil, err
		}
		return tarbundle.PackFiles(contentpkg.WorkSite(pkg), tarbundle.SiteLimits)
	}
}

// demoTicket verifies a demo ticket and its subject's read of the app,
// re-checked on every file: the site it shows. Anything else is
// errSiteNotFound.
func (s *Service) demoTicket(ctx context.Context, ticket string) (siteVersion, error) {
	claims, err := s.Signer.VerifyKind(ticket, auth.KindAppDemo)
	if err != nil {
		return siteVersion{}, errSiteNotFound
	}
	id, version, sha, ok := parseAppDemoScope(claims.ID)
	if !ok {
		return siteVersion{}, errSiteNotFound
	}
	_, v, err := s.demoVersion(ctx, claims, id, version)
	switch {
	case errors.Is(err, store.ErrContentNotFound):
		return siteVersion{}, errSiteNotFound
	case err != nil:
		return siteVersion{}, err
	}
	return siteVersion{ContentHash: sha, pack: s.appDemoPack(v)}, nil
}

// handleTicketSDK serves ~app/sdk.js under the member site entry: the SDK
// in demo mode under a demo ticket, else the SDK.
func (s *Service) handleTicketSDK() http.HandlerFunc {
	const js = "text/javascript; charset=utf-8"
	live, demo := s.handleAppSDK(appsdk.JS, js), s.handleAppSDK(appsdk.DemoJS, js)
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Signer.VerifyKind(r.PathValue("ticket"), auth.KindAppDemo); err == nil {
			demo(w, r)
			return
		}
		live(w, r)
	}
}
