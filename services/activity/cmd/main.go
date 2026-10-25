// Command bnbloc-activity runs the agent activity ingest service.
package main

import (
	"context"
	"log"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/services/activity"
)

func main() {
	cfg := config.FromEnv("BNBLOC_ACTIVITY", ":8081")
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		log.Fatalf("bnbloc-activity: store: %v", err)
	}
	members, err := membership.FromEnv()
	if err != nil {
		log.Fatalf("bnbloc-activity: %v", err)
	}
	metrics := obs.NewMetrics("bnbloc-activity")
	go store.RunReplicaMetrics(context.Background(), metrics, backends.DB, 15*time.Second)
	activity.RegisterMetrics(metrics)
	pulseEm, _, err := pulse.FromEnv(context.Background(), "activity")
	if err != nil {
		log.Fatalf("bnbloc-activity: %v", err)
	}
	go pulse.RunMetrics(context.Background(), metrics, pulseEm, 15*time.Second)
	svc := &activity.Service{
		Pulse:       pulseEm,
		Metrics:     metrics,
		Store:       backends.Activity,
		Publisher:   realtime.NewHTTPPublisher(config.FromEnvRaw("BNBLOC_REALTIME_BASE_URL"), "activity"),
		Membership:  members,
		BridgeJobs:  backends.BridgeJobs,
		BridgeSends: backends.BridgeSends,
		// Team agent dashboard read side (same shared DB).
		Team:     backends.TeamAgents(),
		Usage:    backends.AgentUsage(),
		Rooms:    backends.Room,
		Channels: backends.Channel,
		// Room agent catalogs: an agent-host member contributes only the
		// room's platform agents (platform grants + policy, shared DB).
		Grants:   backends.Grant,
		Settings: backends.Settings,
		// Team plans for a bridge's job are admitted by its lease.
		Leases: dispatchReader(backends.BridgeJobs),
		// Agent run records (R10) over the job queue; the feed resolves a
		// run's reply to its room sequence.
		Runs: backends.AgentRuns(),
		Feed: backends.Chat,
		// Single-host serve-all testing must be enabled on the SERVER; a bridge
		// cannot grant it to itself. Never in production.
		AllowServeAllOwners: allowServeAll(),
		// Usage budgets (R12): warnings go through the shared notification
		// outbox (realtime delivers them); the tenant registry names budget
		// members and the org admins a tenant budget warns.
		Budgets: backends.Budgets,
		Tenants: backends.Tenant,
		Notifications: notify.NewEmitter(notify.Stores{
			Prefs: backends.Notifications, Outbox: backends.Notifications, Settings: backends.Settings,
			Installs: backends.ComponentInstalls, WebPush: backends.WebPush,
		}, ""),
		PublicURL: config.FromEnvRaw(notify.PublicURLEnv),
	}
	// Jobs nothing can finish (no bridge ever advertised the agent, or a worker
	// died repeatedly) must reach a terminal state on their own, otherwise a
	// member waits on a queued job forever.
	go sweepJobs(context.Background(), svc)
	if err := obs.ServeWithHealth("bnbloc-activity", cfg.ListenAddr, svcauth.Middleware(svcauth.KeyFromEnv(), svc.Handler()), backends.HealthCheck(), obs.WithMetrics(metrics)); err != nil {
		log.Fatalf("bnbloc-activity: %v", err)
	}
}

// dispatchReader is the job store's lease read side (nil when the
// configured store has none).
func dispatchReader(jobs store.BridgeJobStore) store.LeasedJobReader {
	d, _ := jobs.(store.LeasedJobReader)
	return d
}

// allowServeAll reads the dev-only BNBLOC_ACTIVITY_ALLOW_SERVE_ALL gate. It is
// fatal in production when on (config.RefuseInProduction, the one guard of a
// dev-only switch): one member's bridge driving (and attributing replies for)
// every member's agent is a single-host testing mode only.
func allowServeAll() bool {
	const env = "BNBLOC_ACTIVITY_ALLOW_SERVE_ALL"
	if err := config.RefuseInProduction(env); err != nil {
		log.Fatalf("bnbloc-activity: %v (a single-host testing mode)", err)
	}
	if !config.Bool(env, false) {
		return false
	}
	log.Printf("WARNING bnbloc-activity: serve-all bridges enabled (BNBLOC_ACTIVITY_ALLOW_SERVE_ALL); single-host testing only")
	return true
}

// sweepJobs also refreshes the online-bridges gauge on the same tick.
func sweepJobs(ctx context.Context, svc *activity.Service) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	svc.SampleBridgeMetrics(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			svc.SweepAgentJobs(ctx)
			svc.SampleBridgeMetrics(ctx)
		}
	}
}
