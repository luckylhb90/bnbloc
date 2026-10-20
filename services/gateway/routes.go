package gateway

import (
	"context"
	"net/http"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/sharelink"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// The gateway's route table: every route the gateway serves itself is one
// row naming its edge policy — who may call it (a session with an org
// permission, or no session and why), the rate budget a call spends, and
// the parts of the deployment it needs — and Service.serve applies that
// policy before the handler runs. Proxied routes take the same checks from
// their parsed path (requiredPermission, rateLimited) through the same gate.
// A handler never authenticates or checks an org role itself
// (TestEveryGatewayRouteGated walks the table).

// sessionHandler serves a route for an authenticated session: claims are
// the caller's, acting in their effective tenant (tenant_scope.go).
type sessionHandler func(w http.ResponseWriter, r *http.Request, claims auth.Claims)

// rateBudget is the budget a call spends before its handler runs.
type rateBudget int

const (
	noBudget rateBudget = iota
	// costBudget: a cost-bearing write (creating a room, driving an agent,
	// writing content) spends the caller's per-subject budget (rateAllow).
	costBudget
	// guessBudget: an unauthenticated credential exchange spends the client
	// IP's guess budget (guessAllow): no trusted subject exists yet.
	guessBudget
	// visitorBudget: minting a public link's visitor token spends the
	// client IP's visitor budget (visitorAllow).
	visitorBudget
	// appReadBudget / appWriteBudget: an interactive app's data call spends
	// the client IP's, its link's and its participant's app budgets
	// (appRates), the write ones stricter.
	appReadBudget
	appWriteBudget
)

// dependency is a part of the deployment a route needs; a route whose
// dependency is not configured answers 503 (httpx.Unavailable).
type dependency struct {
	what string
	ok   func(*Service) bool
}

var (
	needContent = dependency{"content components", func(s *Service) bool { return s.ContentComponents != nil }}
	needOrgSync = dependency{"organization sync", func(s *Service) bool { return s.OrgSync != nil }}
)

// route is one gateway-served route. A session route runs session after
// the caller holds perm (rbac.PermRead: any member, viewers included); a
// public route carries no session, says why (the credential its handler
// checks itself) and runs public. An app route (apps.go) serves an
// interactive app's data under the capability its entry resolves: the
// room entry's is a session holding perm, the frame entries' carry none
// and say why.
type route struct {
	pattern string
	perm    rbac.Permission
	budget  rateBudget
	needs   []dependency
	session sessionHandler
	why     string
	public  http.HandlerFunc
	app     *appRoute
}

// sessionLess reports a route that takes no session: a public route, or an
// app route of a frame entry.
func (rt route) sessionLess() bool {
	return rt.public != nil || (rt.app != nil && rt.app.entry != appRoom)
}

// member is a session route any member may call.
func member(pattern string, h sessionHandler) route {
	return route{pattern: pattern, perm: rbac.PermRead, session: h}
}

// gated is a session route that needs perm.
func gated(pattern string, perm rbac.Permission, h sessionHandler) route {
	return route{pattern: pattern, perm: perm, session: h}
}

// open is a public route: why names the credential the handler checks.
func open(pattern, why string, h http.HandlerFunc) route {
	return route{pattern: pattern, why: why, public: h}
}

// spends sets the rate budget a call spends.
func (rt route) spends(b rateBudget) route { rt.budget = b; return rt }

// with adds the dependencies the route needs.
func (rt route) with(deps ...dependency) route { rt.needs = append(rt.needs, deps...); return rt }

// shareCredential is why a share link's route takes no session: the
// credential its handler opens the link with (openShare, shareAccess).
const shareCredential = "a share link's token, or a share ticket, in the path"

// routes is the table of every route the gateway serves itself.
func (s *Service) routes() []route {
	const (
		read     = rbac.PermRead
		create   = rbac.PermCreateRoom
		drive    = rbac.PermDriveAgent
		org      = rbac.PermManageOrg
		platform = rbac.PermManagePlatform
	)
	return append([]route{
		// Sign-in and sessions.
		open("POST /v1/auth/login", "an IdP token", s.handleLogin).spends(guessBudget),
		open("POST /v1/auth/refresh", "a refresh token", s.handleRefresh),
		open("POST /v1/auth/refresh/fork", "a refresh token", s.handleRefreshFork),
		open("POST /v1/auth/logout", "a refresh token", s.handleLogout),
		open("POST /v1/auth/api-token", "an API token", s.handleAPITokenExchange).spends(guessBudget),
		open("GET /v1/auth/sso-config", "the sign-in page asks which sign-in to offer", s.handleAuthProviderConfig),
		open("GET /v1/public/client-config", "deployment facts a client needs before sign-in: no data", s.handleClientConfig),
		open("GET "+publicAuthStartPath, "starts an SSO sign-in", s.handleSSOStart),
		open("GET /api/auth/metadata", "the IdP reads the SP metadata", s.handleSSOMetadata),
		open("GET /api/auth/logout", "the IdP's single logout", s.handleSSOLogout),
		open("POST /api/auth/logout", "the IdP's single logout", s.handleSSOLogout),
		// The ACS path is whatever the deployment registered with its IdP
		// (an IdP-hosted login page posts to the registered URL).
		open("POST "+s.publicACSPath(), "a SAML assertion the auth provider verifies", s.handleAuthProviderACS),
		open("POST /v1/auth/sso/exchange", "a one-time SSO hand-off code", s.handleSSOExchange),
		open("POST /v1/auth/sso/claim", "a one-time SSO hand-off nonce", s.handleSSOClaim),
		member("GET /v1/my/sessions", s.handleMySessions),
		member("DELETE /v1/my/sessions/{id}", s.handleRevokeMySession),
		member("DELETE /v1/my/sessions", s.handleRevokeOtherSessions),
		member("POST /v1/auth/switch-tenant", s.handleSwitchTenant),

		// Local agents and cores. A local agent run creates (adopts) a room
		// and drives an agent: room creation's permission and budget.
		gated("POST /v1/local-agent/run", create, s.handleLocalAgentRun).spends(costBudget),
		member("GET /v1/local-agent/targets", s.handleLocalAgentTargets),
		open("GET /v1/local-agent/discover", "dev only: 404 unless dev discovery is on, then the handler requires a session", s.handleLocalAgentDiscover),
		member("GET /v1/cores", s.handleCores),
		member("GET /v1/cores/{name}/artifact", s.handleCoreArtifact),

		// The member component surface: the rollout-filtered catalog and
		// opt-in installs.
		member("GET /v1/components", s.handleComponents),
		member("POST /v1/components/{name}/install", s.handleComponentInstall),
		member("DELETE /v1/components/{name}/install", s.handleComponentUninstall),
		// The platform and organization values of a component the member's
		// desktop runs (component_settings.go).
		member("GET /v1/components/{name}/settings/resolved", s.handleResolvedComponentSettings),

		// Desktop updates and releases.
		member("GET /v1/app/update/{target}/{currentVersion}", s.handleAppUpdate),
		member("GET /v1/app/update-policy", s.handleUpdatePolicy),
		member("GET /v1/app/releases", s.handleAppReleases),

		// Artifacts, previewers, sites and public shares.
		member("GET /v1/artifacts/{key...}", s.handleArtifactDownload),
		open("GET /v1/previewers/{name}/{version}/{ticket}/{path...}", "a previewer ticket in the path", s.handlePreviewerFile),
		open("GET /v1/settings-pages/{kind}/{name}/{version}/{ticket}/{path...}", "a settings page ticket in the path", s.handleSettingsPageFile),
		member("POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/site-ticket", s.handleSiteTicket),
		member("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/files/{path...}", s.handleSiteFileRead),
		member("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/cover", s.handleSiteCover),
		member("POST /v1/rooms/{roomId}/attachments/{attachmentId}/ticket", s.handleAttachmentTicket),
		open("GET /v1/sites/{ticket}", "a site ticket in the path", s.handleSiteRootRedirect),
		open("GET /v1/sites/{ticket}/{path...}", "a site ticket in the path", s.handleMemberSite),
		// A share link's routes take its credential in the path: the link's
		// token, or a share ticket in its place (share_access.go).
		member("POST /v1/share-tickets", s.handleShareTicket),
		open(sharelink.Pattern, shareCredential, s.handleShareLink),
		open("GET /v1/public/shares/{token}", shareCredential, s.handlePublicShare),
		open("GET /v1/public/shares/{token}/content", shareCredential, s.handlePublicShareContent),
		open("GET /v1/public/shares/{token}/site", shareCredential, s.handleSiteRootRedirect),
		open("GET /v1/public/shares/{token}/site/{path...}", shareCredential, s.handlePublicShareSite),
		open("POST /v1/public/shares/{token}/visitor", shareCredential, s.handleAppVisitor).spends(visitorBudget),

		// A notifier's signed call on a member's behalf.
		open("POST /v1/notifier/replies", "the notifier component's signature", s.handleNotifierReply),
		open("POST /v1/notifier/reply-refs", "the notifier component's signature", s.handleNotifierReplyRef),
		open("POST /v1/notifier/group-binds", "the notifier component's signature", s.handleNotifierGroupBind),
		// An endpoint component's signed ask for its own settings.
		open("GET /v1/component-settings", "the endpoint component's signature", s.handleEndpointComponentSettings),

		// Content components (content_components.go and siblings). A write
		// drives agents' configuration and spends the cost budget; whether
		// a route honors a job grant is the handler's (contentGrant).
		member("GET /v1/content-components", s.handleListContent).with(needContent),
		gated("POST /v1/content-components", drive, s.handleCreateContent).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/import", drive, s.handleImportContent).spends(costBudget).with(needContent),
		member("GET /v1/content-components/effective", s.handleContentEffective).with(needContent),
		gated("POST /v1/content-drafts", drive, s.handleStageContentDraft).spends(costBudget).with(needContent),
		member("GET /v1/content-components/{id}", s.handleGetContent).with(needContent),
		gated("DELETE /v1/content-components/{id}", drive, s.handleDeleteContent).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/versions", drive, s.handleNewContentVersion).spends(costBudget).with(needContent),
		member("GET /v1/content-components/{id}/versions/{version}", s.handleGetContentVersion).with(needContent),
		member("POST /v1/content-components/{id}/versions/{version}/ack", s.handleAckContent).with(needContent),
		member("GET /v1/content-uses", s.handleListContentUses).with(needContent),
		gated("PUT /v1/content-uses", drive, s.handlePutContentUse).spends(costBudget).with(needContent),
		gated("DELETE /v1/content-uses", drive, s.handleDeleteContentUse).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/prompts", drive, s.handleSavePrompt).spends(costBudget).with(needContent),
		member("POST /v1/content-components/{id}/prompts/{promptId}/use", s.handlePromptUse).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/shares", drive, s.handleShareContent).spends(costBudget).with(needContent),
		member("GET /v1/content-components/{id}/shares", s.handleListContentShares).with(needContent),
		gated("DELETE /v1/content-components/{id}/shares/rooms/{room}", drive, s.handleRevokeRoomShare).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/update", drive, s.handleUpdateContentCopy).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/publish", drive, s.handlePublishContent).spends(costBudget).with(needContent),

		// The app gallery (app_gallery.go): an app's cover, favorites, a
		// site published as an app, and "Use this app" (a copy in a room
		// the caller owns, or in a new room: room creation's permission).
		member("GET /v1/content-components/{id}/versions/{version}/cover", s.handleWorkCover).with(needContent),
		// A work version's stored cover (work_version_cover.go): a reader's
		// render, and the author's AI cover (an image call: cost budget).
		member("PUT /v1/content-components/{id}/versions/{version}/cover", s.handlePutWorkCover).with(needContent),
		gated("POST /v1/content-components/{id}/versions/{version}/cover/ai", drive, s.handleDrawStoredWorkCover).spends(costBudget).with(needContent),
		member("GET /v1/content-components/{id}/versions/{version}/file", s.handleWorkFile).with(needContent),
		member("POST /v1/content-components/{id}/versions/{version}/demo", s.handleAppDemo).spends(costBudget).with(needContent),
		member("PUT /v1/content-components/{id}/favorite", s.handleFavorite).spends(costBudget).with(needContent),
		member("DELETE /v1/content-components/{id}/favorite", s.handleFavorite).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/use-app", create, s.handleUseApp).spends(costBudget).with(needContent),
		gated("POST /v1/content-components/{id}/make-similar", create, s.handleMakeSimilar).spends(costBudget).with(needContent),
		// Room packages (room_components.go): what a room's editor saves of
		// it, and a new room made from one (room creation's permission).
		member("GET /v1/rooms/{roomId}/component-draft", s.handleRoomComponentDraft).with(needContent),
		gated("POST /v1/content-components/{id}/create-room", create, s.handleCreateRoomFromComponent).spends(costBudget).with(needContent),
		gated("POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-work", drive, s.handlePublishWork).spends(costBudget).with(needContent),
		gated("POST /v1/rooms/{roomId}/artifacts/{artifactId}/work-cover", drive, s.handleDrawWorkCover).spends(costBudget).with(needContent),
		// AI assist (assist.go): the assist model behind manual entry points.
		member("GET /v1/assist", s.handleAssistStatus).with(needContent),
		gated("POST /v1/assist/{task}", drive, s.handleAssist).spends(costBudget).with(needContent),
		member("GET /v1/rooms/{roomId}/artifacts/{artifactId}/recipe", s.handleDeliverableRecipe).with(needContent),
		// Media model calls (media.go): speech to text, image and video
		// generation through admin-configured providers. They feed message
		// composition, so they follow the assist routes' permission rule. A
		// video's status poll is not a cost: its submit spent the budget.
		member("GET /v1/media", s.handleMediaCapabilities),
		gated("POST /v1/media/transcriptions", drive, s.handleMediaTranscribe).spends(costBudget),
		gated("POST /v1/media/image", drive, s.handleMediaImage).spends(costBudget),
		gated("POST /v1/media/video", drive, s.handleMediaVideo).spends(costBudget),
		gated("GET /v1/media/video/{id}", drive, s.handleMediaVideoStatus),
		// Decision models (decisions.go): calibrated answers to bounded
		// questions, the same rules as the media calls.
		member("GET /v1/decisions", s.handleDecisionCapability),
		gated("POST /v1/decisions", drive, s.handleDecide).spends(costBudget),
		gated("POST /v1/systemone", drive, s.handleSystemOne).spends(costBudget),
		// A job's live view (job_live.go): a tool's frames, through the room
		// relay. Not a cost: the job is already running, and the frame
		// rate is the tool's backpressure.
		gated("POST /v1/live", drive, s.handleLiveFrame),

		// Organization administration (manage_org in the acting tenant).
		gated("GET /v1/tenant/component-settings", org, s.handleOrgSettingsComponents),
		gated("GET /v1/tenant/components/{name}/settings", org, s.handleComponentSettings(pkgmeta.SettingScopeOrg)),
		gated("PUT /v1/tenant/components/{name}/settings", org, s.handlePutComponentSettings(pkgmeta.SettingScopeOrg)),
		gated("GET /v1/tenant/assist-model", org, s.handleOrgAssistModel),
		gated("PUT /v1/tenant/assist-model", org, s.handlePutOrgAssistModel),
		gated("POST /v1/tenant/assist-model/test", org, s.handleTestOrgAssistModel),
		// The organization's own media providers (media.go): on a host the
		// platform allows, replacing the platform's for its members.
		gated("GET /v1/tenant/media/{capability}", org, s.handleOrgMediaProvider),
		gated("PUT /v1/tenant/media/{capability}", org, s.handlePutOrgMediaProvider),
		gated("POST /v1/tenant/media/{capability}/test", org, s.handleTestOrgMediaProvider),
		gated("GET /v1/tenant/decisions", org, s.handleOrgDecisionProvider),
		gated("PUT /v1/tenant/decisions", org, s.handlePutOrgDecisionProvider),
		gated("POST /v1/tenant/decisions/test", org, s.handleTestOrgDecisionProvider),
		gated("GET /v1/tenant/content-requests", org, s.handleListContentRequests).with(needContent),
		gated("GET /v1/tenant/content-requests/{id}", org, s.handleReviewContentRequest).with(needContent),
		gated("POST /v1/tenant/content-requests/{id}/approve", org, s.decideContentRequest(store.ContentRequestApproved)).spends(costBudget).with(needContent),
		gated("POST /v1/tenant/content-requests/{id}/decline", org, s.decideContentRequest(store.ContentRequestDeclined)).spends(costBudget).with(needContent),
		gated("GET /v1/tenant/content-components", org, s.handleListOrgContent).with(needContent),
		gated("POST /v1/tenant/content-components/{id}/unpublish", org, s.handleOrgContentStatus(store.ContentStatusRetired)).with(needContent),
		gated("POST /v1/tenant/content-components/{id}/restore", org, s.handleOrgContentStatus(store.ContentStatusActive)).with(needContent),
		gated("DELETE /v1/tenant/content-components/{id}", org, s.handleRemoveOrgContent).with(needContent),

		// Platform administration (super admins).
		gated("GET /v1/platform/update-config", platform, s.handlePlatformUpdateConfig),
		gated("PUT /v1/platform/update-config", platform, s.handlePlatformUpdateConfigPut),
		gated("GET /v1/platform/components", platform, s.handleListComponents),
		gated("POST /v1/platform/components", platform, s.handleInstallComponent),
		gated("DELETE /v1/platform/components/{name}", platform, s.handleUninstallComponent),
		gated("PUT /v1/platform/components/{name}/tenants", platform, s.handleSetComponentTenants),
		gated("PUT /v1/platform/components/{name}/rollout", platform, s.handleSetComponentRollout),
		gated("POST /v1/platform/components/{name}/secret/rotate", platform, s.handleRotateComponentSecret),
		gated("POST /v1/platform/components/{name}/test", platform, s.handleTestComponent),
		gated("GET /v1/platform/components/{name}/settings", platform, s.handleComponentSettings(pkgmeta.SettingScopePlatform)),
		gated("PUT /v1/platform/components/{name}/settings", platform, s.handlePutComponentSettings(pkgmeta.SettingScopePlatform)),
		gated("POST /v1/platform/mcp-packages", platform, s.handleBuildMCPPackage),
		gated("GET /v1/platform/component-keys", platform, s.handleListComponentTrustedKeys),
		gated("POST /v1/platform/component-keys", platform, s.handleAddComponentTrustedKey),
		gated("DELETE /v1/platform/component-keys/{keyId}", platform, s.handleDeleteComponentTrustedKey),
		gated("GET /v1/platform/artifacts", platform, s.handleArtifactCapability),
		gated("POST /v1/platform/artifacts", platform, s.handleArtifactUpload),
		gated("GET /v1/platform/app-releases", platform, s.handlePlatformReleases),
		gated("PUT /v1/platform/app-releases/meta", platform, s.handlePlatformReleaseMetaPut),
		gated("PUT /v1/platform/app-releases/{version}", platform, s.handlePlatformReleasePut),
		gated("DELETE /v1/platform/app-releases/{version}", platform, s.handlePlatformReleaseDelete),
		gated("GET /v1/platform/versions", platform, s.handlePlatformVersions),
		gated("GET /v1/platform/api-tokens", platform, s.handleListAPITokens),
		gated("POST /v1/platform/api-tokens", platform, s.handleCreateAPIToken),
		gated("DELETE /v1/platform/api-tokens/{id}", platform, s.handleRevokeAPIToken),
		gated("POST /v1/platform/alerts", platform, s.handlePlatformAlert),
		gated("GET /v1/platform/assist-model", platform, s.handleAssistModel),
		gated("PUT /v1/platform/assist-model", platform, s.handlePutAssistModel),
		gated("POST /v1/platform/assist-model/test", platform, s.handleTestAssistModel),
		// Media providers (media.go): speech-to-text, image and video
		// generation.
		gated("GET /v1/platform/media/{capability}", platform, s.handleMediaProvider),
		gated("PUT /v1/platform/media/{capability}", platform, s.handlePutMediaProvider),
		gated("POST /v1/platform/media/{capability}/test", platform, s.handleTestMediaProvider),
		// The decision model (decisions.go).
		gated("GET /v1/platform/decisions", platform, s.handleDecisionProvider),
		gated("PUT /v1/platform/decisions", platform, s.handlePutDecisionProvider),
		gated("POST /v1/platform/decisions/test", platform, s.handleTestDecisionProvider),
		gated("GET /v1/platform/content-components", platform, s.handleListPlatformContent).with(needContent),
		gated("GET /v1/platform/content-components/{id}/package", platform, s.handleExportContentPackage).with(needContent),
		gated("POST /v1/platform/content-components/{id}/unpublish", platform, s.handlePlatformContentStatus(store.ContentStatusRetired)).with(needContent),
		gated("POST /v1/platform/content-components/{id}/restore", platform, s.handlePlatformContentStatus(store.ContentStatusActive)).with(needContent),
		gated("DELETE /v1/platform/content-components/{id}", platform, s.handleRemovePlatformContent).with(needContent),
		gated("GET /v1/platform/org-sync/rules", platform, s.handleOrgSyncRules).with(needOrgSync),
		gated("PUT /v1/platform/org-sync/rules", platform, s.handleOrgSyncPutRules).with(needOrgSync),
		gated("POST /v1/platform/org-sync/preview", platform, s.handleOrgSyncPreview).with(needOrgSync),
		gated("POST /v1/platform/org-sync/deprovision", platform, s.handleOrgSyncDeprovision).with(needOrgSync),
		gated("POST /v1/platform/org-sync/resync", platform, s.handleOrgSyncResync).with(needOrgSync),
	},
		// Interactive apps' data under its three entry points, the SDK and
		// the reserved ~app/ namespace (apps.go).
		s.appRoutes()...)
}

// serve applies a route's policy, then runs its handler.
func (s *Service) serve(rt route) http.HandlerFunc {
	if rt.app != nil {
		return s.serveApp(rt)
	}
	if rt.session == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.spendIP(w, r, rt.budget) || !s.ready(w, rt.needs) {
				return
			}
			rt.public(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.authenticate(r)
		if err != nil {
			httpx.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.permitted(w, claims, rt.perm) || !s.ready(w, rt.needs) || !s.spend(w, claims, rt.budget) {
			return
		}
		rt.session(w, r, claims)
	}
}

// serveApp applies an app route's policy, then serves it (apps.go): the
// client IP's app budget, the capability its entry resolves (the room
// entry: the session and the route's org permission first), then the
// link's and the participant's app budgets.
func (s *Service) serveApp(rt route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !s.appSpend(w, appByIP, rt.budget, s.clientIP(r)) || !s.ready(w, rt.needs) {
			return
		}
		var claims auth.Claims
		if rt.app.entry == appRoom {
			var err error
			if claims, err = s.authenticate(r); err != nil {
				httpx.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !s.permitted(w, claims, rt.perm) {
				return
			}
		}
		c, err := s.appCapabilityOf(r, rt.app.entry, claims)
		if err != nil {
			writeAppErr(w, err)
			return
		}
		if !s.appSpend(w, appByLink, rt.budget, c.link) || !s.appSpend(w, appByParticipant, rt.budget, c.tenant+"\x00"+c.scope.ParticipantKey()) {
			return
		}
		c.still = func(ctx context.Context) bool {
			again, err := s.appCapabilityOf(r.WithContext(ctx), rt.app.entry, claims)
			return err == nil && again.scope == c.scope
		}
		rt.app.serve(s, w, r, c)
	}
}

// spendIP spends a public route's per-client-IP budget; 429 when it is
// spent.
func (s *Service) spendIP(w http.ResponseWriter, r *http.Request, b rateBudget) bool {
	ip := s.clientIP(r)
	switch {
	case b == guessBudget && !s.guessAllow("ip:"+ip),
		b == visitorBudget && !s.visitorAllow(ip):
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return false
	}
	return true
}

// permitted is the one org-permission check at the edge: a gateway-served
// route's and every proxied request's (edgeGate). It answers 403 itself.
func (s *Service) permitted(w http.ResponseWriter, claims auth.Claims, perm rbac.Permission) bool {
	if perm != rbac.PermRead && !rbac.Can(claims.Role, perm) {
		httpx.Error(w, "insufficient org role", http.StatusForbidden)
		return false
	}
	return true
}

// spend consumes the caller's budget for a cost-bearing call; 429 when it
// is spent.
func (s *Service) spend(w http.ResponseWriter, claims auth.Claims, b rateBudget) bool {
	if b == costBudget && !s.rateAllow(claims.Subject) {
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return false
	}
	return true
}

// ready answers 503 for the first dependency the deployment lacks.
func (s *Service) ready(w http.ResponseWriter, needs []dependency) bool {
	for _, d := range needs {
		if !d.ok(s) {
			httpx.Unavailable(w, d.what)
			return false
		}
	}
	return true
}
