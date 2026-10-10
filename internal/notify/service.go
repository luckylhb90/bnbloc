package notify

import (
	"context"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// PublicURLEnv names the public web origin notification deep links point at.
const PublicURLEnv = "BNBLOC_PUBLIC_URL"

// Stores are the persistence dependencies of the notification subsystem.
type Stores struct {
	Prefs    store.NotificationPrefsStore
	Outbox   store.NotificationOutboxStore
	Settings store.PlatformSettingsStore
	Installs store.ComponentInstallStore
	Audit    store.ExtensionAuditStore
	// Replies stores the reply tokens minted at delivery (N1); nil makes
	// notifications unanswerable.
	Replies store.NotificationReplyTokenStore
	// RoomName resolves a room's display name for payloads (optional).
	RoomName func(ctx context.Context, roomID string) string

	// WebPush holds members' browser push subscriptions (N4); nil = no
	// built-in push channel.
	WebPush store.WebPushSubscriptionStore
}

// Service bundles the producer (Observer over an Emitter), the delivery
// Worker and the member Handlers for the service that owns notifications
// (realtime). Notifiers is the directory its component pipeline activates
// notifier records into (Notifiers.Installer).
type Service struct {
	Notifiers *Directory
	Emitter   *Emitter
	Observer  *Observer
	Worker    *Worker
	Handlers  *Handlers

	// Push is the built-in Web Push channel (routes + delivery).
	Push *WebPush
}

// Options tune NewService; zero values read the environment.
type Options struct {
	PublicURL string
	// WebPush is the deployment's VAPID identity (zero = push disabled:
	// the routes answer 404 and queued push items are given up).
	WebPush WebPushConfig
	// Metrics receives the delivery outcome and outbox series (nil = off).
	Metrics *obs.Metrics
}

// NewService wires the notification subsystem over st. Membership authorizes
// mention candidates (a mention only counts for a room member).
func NewService(st Stores, membership Membership, opts Options) *Service {
	emitter := NewEmitter(st, opts.PublicURL)
	secrets := &SecretStore{Settings: st.Settings}
	worker := &Worker{Outbox: st.Outbox, Notifiers: emitter.Notifiers, Secrets: secrets, Audit: st.Audit}
	if st.Replies != nil {
		worker.Replies = &ReplyTokens{Store: st.Replies}
	}
	worker.Push = &WebPush{Config: opts.WebPush, Subs: st.WebPush}
	// The worker routes DesktopChannel items; the realtime main wires
	// worker.Desktop to its plugin hook dispatcher.
	worker.Emitter = emitter
	worker.defaults()
	worker.Metrics = opts.Metrics
	RegisterMetrics(opts.Metrics)
	emitter.OnEnqueue = worker.Wake
	return &Service{
		Notifiers: emitter.Notifiers,
		Emitter:   emitter,
		Observer:  NewObserver(emitter, membership, DefaultQueueSize),
		Worker:    worker,
		Push:      worker.Push,
		Handlers: &Handlers{
			Prefs: st.Prefs, Notifiers: emitter.Notifiers, Installs: st.Installs,
			Secrets: secrets, PublicURL: emitter.PublicURL, Audit: st.Audit,
		},
	}
}

// Start runs the observer and the delivery worker until ctx ends.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go s.Observer.Run(ctx)
	go s.Worker.Run(ctx)
}

// Observe is the publish tap (non-blocking; nil-safe).
func (s *Service) Observe(ctx context.Context, ev wire.Event) {
	if s == nil {
		return
	}
	s.Observer.Observe(ctx, ev)
}

// Register mounts the member routes (nil-safe).
func (s *Service) Register(mux *http.ServeMux) {
	if s == nil {
		return
	}
	s.Handlers.Register(mux)
	s.Push.Register(mux)
}

// NewEmitter builds the Emitter every producer uses (realtime's own via
// NewService; shareagent and the gateway producer-only: the realtime Worker
// drains the shared outbox within its poll interval). publicURL "" reads
// PublicURLEnv.
func NewEmitter(st Stores, publicURL string) *Emitter {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		publicURL = strings.TrimRight(strings.TrimSpace(config.FromEnvRaw(PublicURLEnv)), "/")
	}
	return &Emitter{
		Prefs: st.Prefs, Outbox: st.Outbox, Notifiers: NewDirectory(st.Settings), Plugins: NewPluginDirectory(st.Settings),
		Installs: st.Installs, RoomName: st.RoomName, PublicURL: publicURL, Push: st.WebPush,
	}
}
