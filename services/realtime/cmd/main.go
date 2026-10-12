// Command bnbloc-realtime runs the presence + business event fan-out service.
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/retention"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

func main() {
	cfg := config.FromEnv("BNBLOC_REALTIME", ":8082")
	members := membershipFor(cfg.RoomBaseURL)
	// One registry for request metrics and the domain series (published
	// events, SSE connections, notification delivery; docs/monitoring.md).
	metrics := obs.NewMetrics("bnbloc-realtime")
	realtime.RegisterMetrics(metrics)
	// Scale-out (BNBLOC_REALTIME_PEERS + _SELF): each room and member is owned
	// by one replica; unset is a single node that owns every key.
	peers, err := shard.FromEnv("BNBLOC_REALTIME")
	if err != nil {
		log.Fatalf("bnbloc-realtime: %v", err)
	}
	peers.Instrument(metrics, "bnbloc-realtime")
	svc := &realtimehttp.Service{
		Hub:        realtime.NewHub(256, 64),
		Membership: members,
		// Global search reads only the rooms bnbloc-room lists for the caller.
		Rooms: members,
		Peers: realtimehttp.NewPeers(peers),
	}
	// Optional durable chat history: when a MySQL DSN is configured, persist the
	// room feed so clients can page history beyond the in-memory replay buffer.
	// Empty DSN keeps realtime in-memory-only (local dev / tests).
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		log.Fatalf("bnbloc-realtime: store: %v", err)
	}
	// This replica is live in its own view while its readiness check passes
	// (the other replicas probe its /readyz for the same answer).
	peers.CheckSelfWith(backends.HealthCheck())
	svc.Chat = backends.Chat
	// Channel routing: requests omitting channelId resolve the room's default
	// channel, the event streams filter by channel, and unread aggregates
	// per channel.
	svc.Channels = backends.Channel
	svc.BridgeJobs = backends.BridgeJobs
	if d, ok := backends.BridgeJobs.(realtimehttp.DispatchStore); ok {
		svc.Dispatches = d
	}
	svc.Audit = backends.ExtAudit
	// A chat to a platform agent whose host is offline is refused before a
	// job exists (shared grants, policy and bridge advertisements).
	svc.PlatformAgents = &realtimehttp.PlatformAgentGuard{Grants: backends.Grant, Settings: backends.Settings, Bridges: backends.BridgeJobs}
	svc.Attachments = backends.Attachments
	// Team features: prompt library, message tasks, and global search over
	// the room feed this service persists.
	svc.Prompts = backends.Prompts
	svc.Tasks = backends.MessageTasks
	svc.Artifacts = backends.Artifacts
	// Message search backend (docs/prd/2026-10-10-enterprise-scale.md §4):
	// the store's FULLTEXT index, or OpenSearch when
	// BNBLOC_SEARCH_OPENSEARCH_URL is set (probed here; refused at startup
	// in production when unusable). An external index is kept current by
	// the updater: the hub's commit hook and edits/deletes queue messages,
	// never blocking the write; retention purges its expired documents.
	search.RegisterMetrics(metrics)
	searchIndex, searchUpdates, err := search.FromEnv(context.Background(), backends.Chat, metrics)
	if err != nil {
		log.Fatalf("bnbloc-realtime: search: %v", err)
	}
	log.Printf("bnbloc-realtime: message search backend: %s", searchIndex.Backend())
	svc.Search, svc.SearchUpdates = searchIndex, searchUpdates
	if searchUpdates != nil {
		svc.Hub.Committed = searchUpdates.Committed
		go searchUpdates.Run(context.Background())
	}
	// Member notifications: the publish tap feeds a bounded producer queue and
	// a worker drains the durable outbox to every notifier component visible
	// to the event's tenant.
	rooms := backends.Room
	// Browser push (N4) is on when the deployment configured a VAPID pair.
	webPush, err := notify.WebPushConfigFromEnv()
	if err != nil {
		log.Fatalf("bnbloc-realtime: %v", err)
	}
	if webPush.Enabled() {
		log.Printf("bnbloc-realtime: browser push notifications enabled (VAPID subject %s)", webPush.Subject)
	} else {
		log.Printf("bnbloc-realtime: browser push notifications disabled (no %s/%s)", notify.WebPushPublicKeyEnv, notify.WebPushPrivateKeyEnv)
	}
	svc.Notifications = notify.NewService(notify.Stores{
		Prefs: backends.Notifications, Outbox: backends.Notifications,
		Settings: backends.Settings, Installs: backends.ComponentInstalls, Audit: backends.ExtAudit,
		WebPush: backends.WebPush,
		Replies: backends.ReplyTokens,
		RoomName: func(ctx context.Context, roomID string) string {
			if r, err := rooms.GetRoom(ctx, roomID); err == nil {
				return r.Name
			}
			return ""
		},
	}, members, notify.Options{WebPush: webPush, Metrics: metrics})
	// Realtime hosts plugins and notifiers: boot rehydrates their install
	// records, and a periodic reconcile picks up installs, upgrades, rollout
	// changes and uninstalls made through the gateway without a restart.
	dispatcher := realtimehttp.NewPluginHookDispatcher(backends.ExtAudit, backends.ComponentInstalls, nil)
	componentPipe := realtimehttp.NewComponentPipeline(backends.Settings, backends.ExtAudit, dispatcher, backends.ComponentInstalls, nil, svc.Notifications.Notifiers.Installer())
	componentPipe.AllowUnsigned = truthy(config.FromEnvRaw("BNBLOC_EXT_ALLOW_UNSIGNED"))
	if err := componentPipe.Rehydrate(context.Background(), extreg.ServiceRealtime); err != nil {
		log.Printf("bnbloc-realtime: component rehydrate failed: %v", err)
	}
	go componentPipe.RunReconcile(context.Background(), extreg.ServiceRealtime, extpipe.ReconcileInterval, "bnbloc-realtime")
	svc.PluginHooks = dispatcher
	// The outbox worker routes DesktopChannel items: a member whose desktop
	// runs a notification plugin (notification.v1) gets their notifications
	// there; the server notifiers are the durable fallback.
	// With peers, a member's desktop streams live on the member's owner; the
	// worker (which runs on every replica) asks and delivers there.
	svc.Notifications.Worker.Desktop = realtimehttp.MemberDesktopRoute(dispatcher, svc.Peers)
	svc.Notifications.Start(context.Background())
	// Scheduled prompts (N2): realtime owns chat publish, so it runs the
	// scheduler; runs post as the schedule's creator through the chat path.
	svc.Schedules = backends.Schedules
	svc.RoomRoles = backends.Room
	go svc.RunScheduler(context.Background())
	// blobs stays nil when the store is unavailable (the retention worker
	// then skips the blob collection).
	var blobs blobstore.BlobStore
	if b, err := blobstore.FromEnv(); err != nil {
		log.Printf("bnbloc-realtime: attachments disabled: %v", err)
		svc.BlobError = err.Error()
	} else {
		svc.Blobs, blobs = b, b
	}
	// The disk backend is one replica's filesystem: with peers, a blob
	// written through one replica would be missing on the others.
	if _, disk := svc.Blobs.(*blobstore.DiskStore); disk && peers != nil {
		log.Fatalf("bnbloc-realtime: BNBLOC_REALTIME_PEERS is set but the blob store is the local disk; more than one replica requires the S3 backend (BNBLOC_ATTACHMENTS_S3_BUCKET)")
	}
	direct, err := blobstore.DirectDownloadsFromEnv()
	if err != nil {
		log.Fatalf("bnbloc-realtime: %v", err)
	}
	if _, ok := svc.Blobs.(blobstore.Presigner); direct && !ok {
		log.Printf("bnbloc-realtime: BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS is on but the blob store cannot presign (not S3); downloads stream")
	}
	svc.DirectDownloads = direct
	// Data retention (docs/prd/2026-10-10-enterprise-scale.md §3): every
	// replica runs the worker; the cluster-wide retention lock lets one pass
	// at a time delete expired rows and collect unreferenced blobs. It only
	// touches the store and the blob store, never a room hub.
	retention.RegisterMetrics(metrics)
	go (&retention.Worker{
		Store: store.NewRetention(backends), Tenants: backends.Tenant, Settings: backends.Settings,
		Audit: backends.ExtAudit, Blobs: blobs, Metrics: metrics,
		Search: searchIndex,
	}).Run(context.Background())
	// Seed the hub's per-room sequence from the durable max so sequences continue
	// monotonically across restarts (keeps unread counts and dedup correct).
	chat := backends.Chat
	// A read error is returned, never treated as 0: the hub then fails that
	// publish and re-seeds on the next one instead of restarting the counter.
	svc.Hub.SeedSeq = func(roomID string) (int64, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// "" is room-wide: seq allocation must span every channel.
		return chat.MaxRoomSeq(ctx, roomID, "")
	}
	svc.Hub.Metrics = metrics
	handler := svc.Handler()
	// The first refresh runs before serving, so the replica starts with a
	// real view of its peers.
	peers.Start(context.Background())
	if err := obs.ServeWithHealth("bnbloc-realtime", cfg.ListenAddr, svcauth.Middleware(svcauth.KeyFromEnv(), handler), backends.HealthCheck(), obs.WithMetrics(metrics)); err != nil {
		log.Fatalf("bnbloc-realtime: %v", err)
	}
}

// membershipFor returns a bnbloc-room-backed membership client when a room base
// URL is configured, else a static set for local development.
func membershipFor(roomBaseURL string) interface {
	realtimehttp.Membership
	realtimehttp.RoomDirectory
} {
	if roomBaseURL != "" {
		return membership.NewRoomClient(roomBaseURL)
	}
	return membership.NewStatic()
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
