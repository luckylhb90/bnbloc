// Command bnbloc-shareagent runs the borrow authority (owner room
// authorizations, reserve-then-settle), platform agents and the tenant
// administration service.
package main

import (
	"context"
	"log"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	shareagenthttp "github.com/luckylhb90/bnbloc/services/shareagent"
)

func main() {
	cfg := config.FromEnv("BNBLOC_SHAREAGENT", ":8083")
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		log.Fatalf("bnbloc-shareagent: store: %v", err)
	}
	grants := backends.Grant
	metrics := obs.NewMetrics("bnbloc-shareagent")
	shareagent.RegisterMetrics(metrics)
	rooms := membershipFor(cfg.RoomBaseURL)
	roomName := func(ctx context.Context, roomID string) string {
		if r, err := backends.Room.GetRoom(ctx, roomID); err == nil {
			return r.Name
		}
		return ""
	}
	svc := &shareagenthttp.Service{
		Metrics:    metrics,
		Grants:     grants,
		Membership: rooms,
		Tenants:    backends.Tenant,
		// Platform agents: the policy (platform setting), the hosts' live
		// bridge advertisements (online), and the room service's agent-host
		// memberships.
		Settings:   backends.Settings,
		Bridges:    backends.BridgeJobs,
		AgentHosts: rooms,
		RoomName:   roomName,
		// Borrow quota requests (F3).
		QuotaRequests: backends.QuotaRequests,
		// A new quota request notifies the owner through the shared
		// notification outbox (realtime delivers it).
		Notifications: notify.NewEmitter(notify.Stores{
			Prefs: backends.Notifications, Outbox: backends.Notifications, Settings: backends.Settings,
			Installs: backends.ComponentInstalls, WebPush: backends.WebPush,
			RoomName: roomName,
		}, ""),
		// Member removal, tenant suspension and the admin "sign out member"
		// action revoke refresh families in the store the gateway rotates.
		RefreshTokens: backends.RefreshTokens,
		// A member removed from a tenant loses their browser push
		// subscriptions there.
		PushSubscriptions: backends.WebPush,
		// Audit log (R13): administration events are appended to the shared
		// trail; the read side merges every trail.
		ExtAudit: backends.ExtAudit,
		AuditLog: backends.AuditLogReaders(),
		Now:      nowMs,
	}
	if err := obs.ServeWithHealth("bnbloc-shareagent", cfg.ListenAddr, svcauth.Middleware(svcauth.KeyFromEnv(), svc.Handler()), backends.HealthCheck(), obs.WithMetrics(metrics)); err != nil {
		log.Fatalf("bnbloc-shareagent: %v", err)
	}
}

// roomMembership is the room service as shareagent uses it: membership
// lookups and agent-host membership writes.
type roomMembership interface {
	shareagenthttp.Membership
	shareagenthttp.AgentHosts
}

// membershipFor returns a bnbloc-room-backed membership client when a room base
// URL is configured, else a static set for local development.
func membershipFor(roomBaseURL string) roomMembership {
	if roomBaseURL != "" {
		return membership.NewRoomClient(roomBaseURL)
	}
	return membership.NewStatic()
}

func nowMs() int64 { return time.Now().UnixMilli() }
