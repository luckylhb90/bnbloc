// Command bnbloc-gateway runs the OIDC relying-party gateway: token exchange,
// session verification, room-scope enforcement, and reverse proxy.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/orgsync"
	"github.com/luckylhb90/bnbloc/internal/provisioning"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/services/gateway"
	"github.com/luckylhb90/bnbloc/services/gateway/webui"
)

// devVerifier accepts any non-empty token and derives the subject from it, for
// local development only. Production injects an OIDC discovery-backed verifier.
type devVerifier struct{}

func (devVerifier) Verify(token string) (auth.Identity, error) {
	if token == "" {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	// The subject is the token as typed: subjects are case-sensitive
	// everywhere, so "maya" and "Maya" are two identities.
	return auth.Identity{Subject: strings.TrimSpace(token)}, nil
}

// resolveVerifier returns the verifier behind POST /v1/auth/login: a real OIDC
// verifier when BNBLOC_OIDC_ISSUER_URL and BNBLOC_OIDC_CLIENT_ID are set.
// Without OIDC, production gets auth.DisabledVerifier — direct token login is
// refused and sign-in happens only through an installed auth-provider (SSO),
// which is rehydrated from platform settings after boot and so cannot be
// checked here. That is fail-closed without blocking an SSO-only deployment: a
// production gateway with neither source simply lets nobody in (bootstrap the
// first super-admin with `bnbloc-gateway mint-admin-token`). Development keeps
// the dev verifier, which accepts any identity string.
func resolveVerifier() auth.IDPVerifier {
	issuer := config.FromEnvRaw("BNBLOC_OIDC_ISSUER_URL")
	clientID := config.FromEnvRaw("BNBLOC_OIDC_CLIENT_ID")
	if issuer != "" && clientID != "" {
		log.Printf("bnbloc-gateway: using OIDC issuer %s", issuer)
		return auth.NewOIDCVerifier(issuer, clientID)
	}
	if config.Production() {
		log.Printf("bnbloc-gateway: no OIDC configured; direct login is DISABLED (%s=production). Sign in through an installed auth-provider (SSO); bootstrap a super-admin with `bnbloc-gateway %s <subject>`", config.EnvVar, mintAdminTokenCmd)
		return auth.DisabledVerifier{}
	}
	log.Printf("bnbloc-gateway: using dev verifier (no OIDC configured)")
	return devVerifier{}
}

// resolveCores builds the env-configured core list (BNBLOC_CORES, through
// coreset.FromEnv) and logs which cores resolved an endpoint.
func resolveCores() []coreset.Entry {
	entries := coreset.FromEnv()
	for _, e := range entries {
		if e.Endpoint.OK() {
			log.Printf("bnbloc-gateway: core %q endpoint %s", e.Name, e.Endpoint.BaseURL)
		} else {
			log.Printf("bnbloc-gateway: core %q (no endpoint; discovery deferred to first use)", e.Name)
		}
	}
	return entries
}

// resolveSigningKey returns the session-token signing key. A stable key from
// BNBLOC_SESSION_SIGNING_KEY keeps sessions valid across gateway restarts and
// across replicas; when unset a random key is generated and a warning is logged
// (dev only — every restart then invalidates all existing sessions).
func resolveSigningKey() []byte {
	if v := config.FromEnvRaw("BNBLOC_SESSION_SIGNING_KEY"); v != "" {
		return []byte(v)
	}
	if config.Production() {
		log.Fatalf("bnbloc-gateway: %s=production requires BNBLOC_SESSION_SIGNING_KEY (an ephemeral key logs everyone out on restart and breaks multi-replica sessions)", config.EnvVar)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	log.Printf("bnbloc-gateway: WARNING no BNBLOC_SESSION_SIGNING_KEY set; using an ephemeral key — all sessions are invalidated on restart")
	return key
}

// tenantWiring bundles the login-time tenant hooks that share one tenant store:
// the org mapping/provisioning resolver and the suspended-tenant gate.
type tenantWiring struct {
	Resolve    func(auth.Identity, string) (string, string, error)
	Suspended  func(tenantID string) bool
	MemberRole func(tenantID, userID string) (role string, found bool)
	Settings   store.PlatformSettingsStore
	ExtAudit   store.ExtensionAuditStore
	// ComponentInstalls backs the member component catalog's opt-in installs.
	ComponentInstalls store.ComponentInstallStore
	// ContentComponents and ContentJobs back the content component routes.
	ContentComponents store.ContentComponentStore
	ContentJobs       gateway.ContentJobs
	// NotifierReplies resolves notification reply tokens (N1).
	NotifierReplies store.NotificationReplyTokenStore
	// SSOHandoffs and RefreshTokens are the shared auth state every gateway
	// replica reads (SSO hand-offs, refresh-token families).
	SSOHandoffs   store.SSOHandoffStore
	RefreshTokens store.RefreshTokenStore
	// APITokens holds super-admin-issued API tokens; Alerts queues admin
	// alerts into the shared notification outbox (realtime delivers).
	APITokens store.APITokenStore
	Alerts    *notify.Emitter
	// OrgSync applies the organization-sync rules (R11).
	OrgSync *orgsync.Syncer
	// MemberCount counts distinct members across tenants (adoption stats).
	MemberCount func() (int, error)
	// RoomMembers lists a room's human members and OrgAdmins an
	// organization's org admins (content sharing and approval).
	RoomMembers func(ctx context.Context, roomID string) ([]string, error)
	OrgAdmins   func(tenantID string) ([]string, error)
	// Health pings the control-plane store for the readiness probe; nil when no
	// DB is configured (dev/in-memory), which reads as always-ready.
	Health func() error
}

// resolveTenantWiring builds the login-time tenant/org mapping + provisioning
// hook and the suspended-tenant gate. It shares the control-plane MySQL
// (BNBLOC_MYSQL_DSN) for the tenant registry; with no DSN it uses an in-memory
// registry (dev/single-process). BNBLOC_TENANT_AUTOPROVISION=false locks it to
// pre-provisioned tenants.
func resolveTenantWiring() tenantWiring {
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		if config.Production() {
			log.Fatalf("bnbloc-gateway: tenant store: %v", err)
		}
		log.Printf("bnbloc-gateway: WARNING tenant store unavailable (%v); tenancy falls back to default", err)
		return tenantWiring{}
	}
	res := provisioning.New(backends.Tenant)
	res.AutoProvision = config.Bool("BNBLOC_TENANT_AUTOPROVISION", true)
	// Platform super-admins (cross-tenant) are configured, not provisioned:
	// BNBLOC_SUPER_ADMINS is a comma-separated list of verified subject ids or
	// emails (an email is the natural choice for an operator). A subject
	// matches exactly; an email entry matches the identity's email
	// case-insensitively (provisioning.Resolver.isSuperAdmin).
	if admins := superAdmins(); len(admins) > 0 {
		res.SuperAdmins = make(map[string]bool, len(admins))
		for _, a := range admins {
			if a = strings.TrimSpace(a); a != "" {
				res.SuperAdmins[a] = true
			}
		}
	}
	tenants := backends.Tenant
	suspended := func(tenantID string) bool {
		t, err := tenants.GetTenant(context.Background(), tenantID)
		return err == nil && t.Status == store.TenantSuspended
	}
	// A failed read refuses (found=false, logged): never a role.
	memberRole := func(tenantID, userID string) (string, bool) {
		m, ok, err := tenants.GetMember(context.Background(), tenantID, userID)
		if err != nil {
			log.Printf("gateway: read membership of %s in %s: %v", userID, tenantID, err)
			return "", false
		}
		if !ok {
			return "", false
		}
		return string(m.Role), true
	}
	// Distinct subjects across all tenants (adoption denominators); a subject
	// in two tenants counts once.
	memberCount := func() (int, error) {
		all, err := tenants.ListTenants(context.Background())
		if err != nil {
			return 0, err
		}
		seen := map[string]bool{}
		for _, t := range all {
			members, err := tenants.ListMembers(context.Background(), t.TenantID)
			if err != nil {
				return 0, err
			}
			for _, m := range members {
				seen[m.UserID] = true
			}
		}
		return len(seen), nil
	}
	// A room's human members (a content share with a room); ctx carries the
	// room's tenant.
	roomMembers := func(ctx context.Context, roomID string) ([]string, error) {
		members, err := backends.Room.Members(ctx, roomID)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, m := range members {
			if !m.AgentHost {
				out = append(out, m.UserID)
			}
		}
		return out, nil
	}
	orgAdmins := func(tenantID string) ([]string, error) {
		members, err := tenants.ListMembers(context.Background(), tenantID)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, m := range members {
			if m.Role == store.OrgRoleAdmin {
				out = append(out, m.UserID)
			}
		}
		return out, nil
	}
	return tenantWiring{Resolve: res.Resolve, RoomMembers: roomMembers, OrgAdmins: orgAdmins, Suspended: suspended, MemberRole: memberRole, Settings: backends.Settings, ExtAudit: backends.ExtAudit, ComponentInstalls: backends.ComponentInstalls, NotifierReplies: backends.ReplyTokens, SSOHandoffs: backends.SSOHandoffs, RefreshTokens: backends.RefreshTokens, MemberCount: memberCount, Health: backends.HealthCheck(),
		APITokens: backends.APITokens,
		// Built-in content (the Expert builder) overlays the stored components.
		ContentComponents: contentbuiltin.Overlay(backends.ContentComponents),
		ContentJobs:       contentJobs(backends.BridgeJobs),
		OrgSync: &orgsync.Syncer{
			Settings: backends.Settings, Tenants: backends.Tenant, Rooms: backends.Room,
			State: backends.OrgSync, Sessions: backends.RefreshTokens, Audit: backends.ExtAudit,
		},
		// Producer-only emitter for admin alerts (the one producer path:
		// notifiers, desktop notification plugins, browser push); the
		// realtime worker drains the shared outbox. An alert is room-less
		// and links only to its sender's url.
		Alerts: notify.NewEmitter(notify.Stores{
			Prefs: backends.Notifications, Outbox: backends.Notifications, Settings: backends.Settings,
			Installs: backends.ComponentInstalls, WebPush: backends.WebPush,
		}, ""),
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == mintAdminTokenCmd {
		runMintAdminToken(os.Args[2:])
		return
	}
	cfg := config.FromEnv("BNBLOC_GATEWAY", ":8080")
	// The gateway signs every proxied identity; production must never run it
	// unsigned (svcauth dev mode) — backends would then trust any caller.
	if err := svcauth.CheckKey(svcauth.KeyFromEnv()); err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	// A dev-only switch (BNBLOC_DEV_DISCOVERY) never runs in production.
	if err := gateway.CheckProductionEnv(); err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	tw := resolveTenantWiring()
	allowUnsigned := config.Bool("BNBLOC_EXT_ALLOW_UNSIGNED", false)
	// Organization MCP connectors are sealed under the secret store key;
	// unset turns them off, a malformed key refuses to start.
	secretBox, err := sealbox.FromEnv()
	if err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	if secretBox == nil {
		log.Printf("bnbloc-gateway: %s unset; organization MCP connectors are off", sealbox.KeyEnv)
	}
	members, err := membership.FromEnv()
	if err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	// Sites (and their app API) get an origin of their own when the
	// deployment names one; a malformed value refuses to start.
	sitesOrigin, err := gateway.ParseSitesOrigin(config.FromEnvRaw(gateway.SitesOriginEnv), config.FromEnvRaw(notify.PublicURLEnv))
	if err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	svc := &gateway.Service{
		Verifier: resolveVerifier(),
		Signer:   auth.NewSigner(resolveSigningKey()),
		Backends: gateway.Backends{
			Room:       config.FromEnvRaw("BNBLOC_ROOM_BASE_URL"),
			Activity:   config.FromEnvRaw("BNBLOC_ACTIVITY_BASE_URL"),
			ShareAgent: config.FromEnvRaw("BNBLOC_SHAREAGENT_BASE_URL"),
			Realtime:   config.FromEnvRaw("BNBLOC_REALTIME_BASE_URL"),
			Relay:      config.FromEnvRaw("BNBLOC_RELAY_BASE_URL"),
		},
		Rooms:             gateway.NewRoomMembershipResolver(config.FromEnvRaw("BNBLOC_ROOM_BASE_URL")),
		Membership:        members,
		RoomTenant:        gateway.NewRoomTenantResolver(config.FromEnvRaw("BNBLOC_ROOM_BASE_URL")),
		SessionTTL:        time.Hour,
		AuthACSPath:       resolveACSPath(),
		WebUI:             webui.Handler(),
		WebBuild:          webui.Build(),
		PlatformSettings:  tw.Settings,
		ExtAudit:          tw.ExtAudit,
		AllowExtUnsigned:  allowUnsigned,
		SecretBox:         secretBox,
		ComponentInstalls: tw.ComponentInstalls,
		ContentComponents: tw.ContentComponents,
		ContentJobs:       tw.ContentJobs,
		NotifierReplies:   tw.NotifierReplies,
		MemberCount:       tw.MemberCount,
		RoomMembers:       tw.RoomMembers,
		OrgAdmins:         tw.OrgAdmins,
		AllowedOrigins:    config.List("BNBLOC_ALLOWED_ORIGINS"),
		TrustedProxies:    resolveTrustedProxies(),
		PublicURL:         config.FromEnvRaw(notify.PublicURLEnv),
		SitesOrigin:       sitesOrigin,
		DevDiscovery:      gateway.DevDiscoveryFromEnv(),
		// Deployment without curated release history: a download link still
		// activates the UI's download affordances (one synthetic release).
		DesktopDownloadURL: strings.TrimSpace(config.FromEnvRaw("BNBLOC_DESKTOP_DOWNLOAD_URL")),
		TenantResolver:     tw.Resolve,
		TenantSuspended:    tw.Suspended,
		TenantMemberRole:   tw.MemberRole,
		SSOHandoffs:        tw.SSOHandoffs,
		RefreshTokens:      tw.RefreshTokens,
		APITokens:          tw.APITokens,
		Alerts:             tw.Alerts,
		OrgSync:            tw.OrgSync,
		SuperAdmins:        superAdmins(),
	}
	// Boot is the first component reconcile, on the SAME pipeline the
	// periodic reconcile and the install/uninstall handlers use, so the live
	// set it records is the one later reconciles diff against. Activation
	// puts the drivable installed cores in svc.Cores (coreset.EntryFor); the
	// env-configured ones join them, built by the same EntryFor.
	svc.ExtPipe = gateway.NewCorePipeline(svc, tw.ExtAudit)
	gateway.RehydrateInstalledComponents(context.Background(), svc.ExtPipe, extreg.ServiceGateway)
	svc.AddEnvCores(resolveCores())
	// Installs land on one replica; every replica converges on the durable
	// records within one interval.
	go svc.RunComponentReconcile(context.Background())
	// Gateway replicas share their per-subject/per-IP rate limits by each
	// enforcing 1/N of every budget, N being the live replica count read
	// from BNBLOC_GATEWAY_PEERS (unset = 1).
	peers, err := shard.CountFromEnv("BNBLOC_GATEWAY")
	if err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
	peers.Start(context.Background())
	svc.Peers = peers
	// The gateway is the public edge: /metrics is never served on its main
	// listener; BNBLOC_METRICS_ADDR exposes it on an internal one.
	if err := obs.ServeWithHealth("bnbloc-gateway", cfg.ListenAddr, svc.Handler(), tw.Health, obs.PublicListener()); err != nil {
		log.Fatalf("bnbloc-gateway: %v", err)
	}
}

// contentJobs is the job store's read side content resolution needs (both
// store implementations provide it).
func contentJobs(jobs store.BridgeJobStore) gateway.ContentJobs {
	cj, _ := jobs.(gateway.ContentJobs)
	return cj
}

// mintAdminTokenCmd is the operator bootstrap subcommand. A production gateway
// has no password login, so the FIRST super-admin of an SSO-only deployment
// (before any auth-provider is installed) needs another way in. Whoever can
// exec the gateway binary with its environment already holds the session
// signing key, so minting a short-lived token there grants nothing new:
//
//	docker compose exec gateway /usr/local/bin/bnbloc mint-admin-token admin@corp.example
//
// prints a 15-minute super-admin session token for a subject listed in
// BNBLOC_SUPER_ADMINS (use it as BNBLOC_TOKEN for the bnbloc CLI or paste it
// into the web session) to install the auth-provider component.
const mintAdminTokenCmd = "mint-admin-token"

func runMintAdminToken(args []string) {
	if len(args) != 1 {
		log.Fatalf("usage: bnbloc-gateway %s <super-admin subject or email>", mintAdminTokenCmd)
	}
	key := config.FromEnvRaw("BNBLOC_SESSION_SIGNING_KEY")
	token, err := gateway.MintAdminToken(args[0], superAdmins(), key, time.Now())
	if err != nil {
		log.Fatalf("bnbloc-gateway %s: %v", mintAdminTokenCmd, err)
	}
	fmt.Println(token)
}

// resolveACSPath reads BNBLOC_AUTH_ACS_PATH: the public path of the ACS URL
// registered with the IdP (e.g. /api/sso/acs). Empty = /api/auth/acs. A value
// that is not an absolute path under /api/ is fatal (it would shadow API or UI
// routes).
func resolveACSPath() string {
	p := strings.TrimSpace(config.FromEnvRaw("BNBLOC_AUTH_ACS_PATH"))
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/api/") || strings.ContainsAny(p, " ?#{}") {
		log.Fatalf("bnbloc-gateway: BNBLOC_AUTH_ACS_PATH must be a path under /api/ (got %q)", p)
	}
	return p
}

// resolveTrustedProxies parses BNBLOC_TRUSTED_PROXIES: comma-separated CIDRs
// or single IPs of the reverse proxies in front of the gateway (e.g. the edge
// Caddy's network). Their X-Forwarded-For is believed for the per-IP login and
// invite limits; unset trusts none (the TCP peer is the client). A malformed
// entry is fatal so a typo cannot silently disable or widen the trust.
func resolveTrustedProxies() []netip.Prefix {
	var out []netip.Prefix
	for _, raw := range config.List("BNBLOC_TRUSTED_PROXIES") {
		if p, err := netip.ParsePrefix(raw); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			log.Fatalf("bnbloc-gateway: BNBLOC_TRUSTED_PROXIES: invalid entry %q", raw)
		}
		out = append(out, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
	}
	return out
}

// superAdmins is BNBLOC_SUPER_ADMINS, the configured platform super-admin
// list (verified subject ids or emails), read the one way for login
// provisioning, admin alerts and the bootstrap token.
func superAdmins() []string { return config.List("BNBLOC_SUPER_ADMINS") }
