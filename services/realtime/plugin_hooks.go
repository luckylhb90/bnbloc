package realtimehttp

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/pluginruntime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// PluginHookDispatcher is the plugin hook authority. Plugin sidecars run on
// member machines (supervised by each member's desktop), which the server
// never dials. So delivery is PER MEMBER: every desktop that runs plugin
// sidecars holds an authenticated stream (GET /v1/my/plugin-events), and the
// dispatcher sends each room event to the streams of members who
//
//   - belong to the event's room (membership is checked per event),
//   - may use the plugin: its rollout is not off, its tenant scope admits both
//     the event's tenant and the member's session tenant, and an opt-in
//     plugin was installed by that member (component_installs), and
//   - subscribe to the event's hook through that plugin.
//
// Two members of one room who both run the same plugin each get the event on
// their own sidecar — inherent to per-member sidecars. A member with no
// connected desktop misses the event (no offline queue).
//
// The notification.v1 hook is not a room event: the notification outbox
// worker hands ONE member's notification to their own streams
// (DeliverNotification, notify.DesktopRoute) and learns its outcome once
// (deliveryGroup): delivered by a plugin, or not — the worker then queues the
// member's server-notifier delivery from its durable outbox row. The desktop
// posts the envelope to its local sidecar and acknowledges the outcome, which
// the dispatcher audits (extension_audit: deliver delivered|failed).
//
// Install records (platform settings, written by the gateway's pipeline in
// another process) are the only source of truth, read through a short-TTL
// snapshot (recordsTTL); a failed read delivers nothing (fail closed).
//
// The publish path never blocks: Deliver hands the event to a bounded queue
// drained by one fan-out worker (membership probes run there), each stream
// has a bounded buffer, and the pending-ack table is capped. Anything that
// does not fit is dropped and audited as failed.
type PluginHookDispatcher struct {
	mu       sync.Mutex
	records  func(context.Context) ([]extpipe.Record, error)
	snapshot recordSnapshot
	audit    store.ExtensionAuditStore
	installs store.ComponentInstallStore
	now      func() int64

	queue     chan pluginEvent
	startOnce sync.Once
	streams   map[*pluginStream]struct{}
	pending   map[string]pendingDelivery
	seq       atomic.Uint64
	dropped   atomic.Int64
}

const (
	// recordsTTL bounds how stale the dispatcher's view of the install records
	// may be: the worst-case delay before an uninstall, rollout or scope change
	// made in another process takes effect. It also bounds the per-stream
	// cache of a member's opt-in installs.
	recordsTTL = 5 * time.Second
	// pluginQueueSize bounds the publish → fan-out hand-off.
	pluginQueueSize = 256
	// pluginStreamBuffer bounds one desktop stream's undelivered frames.
	pluginStreamBuffer = 64
	// maxPluginStreamsPerSubject bounds concurrent streams of one member (a
	// few desktops, plus a reconnect overlapping its predecessor).
	maxPluginStreamsPerSubject = 8
	// maxPendingDeliveries caps deliveries awaiting an ack across all streams.
	maxPendingDeliveries = 4096
	// pendingAckTimeout is how long a delivery may stay unacknowledged before
	// it is audited as failed.
	pendingAckTimeout = 60 * time.Second
	// pendingSweepInterval is how often unacknowledged deliveries expire.
	pendingSweepInterval = 10 * time.Second
)

// recordSnapshot is the last successful read of the plugin install records.
type recordSnapshot struct {
	recs  []extpipe.Record
	atMs  int64
	valid bool
}

// pluginEvent is one item queued for fan-out: a published room event, or
// (notification set) one member's notification for the notification.v1 hook.
type pluginEvent struct {
	tenantID string
	ev       wire.Event
	members  membership.Reader

	notification *wire.PluginNotification
	group        *deliveryGroup
}

// deliveryGroup tracks every desktop delivery of one member's notification
// and reports its outcome once: delivered as soon as any delivery is
// acknowledged delivered, not delivered when every delivery ended otherwise.
// pending starts at 1 — the fan-out's own hold, released when it has
// offered everything — so an early failure can never report "not
// delivered" while more offers are still being made.
type deliveryGroup struct {
	mu      sync.Mutex
	pending int
	done    func(delivered bool)
}

func newDeliveryGroup(done func(delivered bool)) *deliveryGroup {
	return &deliveryGroup{pending: 1, done: done}
}

func (g *deliveryGroup) add() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending++
	g.mu.Unlock()
}

// resolve records one outcome. The first delivered one, or the last one
// when none was delivered, reports the group's outcome (on its own
// goroutine: the receiver writes the outbox); later outcomes are ignored.
func (g *deliveryGroup) resolve(delivered bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending--
	var done func(bool)
	if delivered || g.pending == 0 {
		done, g.done = g.done, nil
	}
	g.mu.Unlock()
	if done != nil {
		go done(delivered)
	}
}

// pluginStream is one desktop's connected plugin event stream.
type pluginStream struct {
	subject  string
	tenantID string // the member's session tenant (what their desktop lists)
	// plugins: the desktop runs a plugin sidecar (?plugins=1). Only such a
	// stream receives plugin deliveries and counts for notification
	// routing; a desktop without plugins holds the stream for catalog
	// hints alone.
	plugins bool
	ch      chan wire.PluginDelivery
	// hints carries components_changed hints (buffer 1: a hint already
	// queued covers any number of later ones; the desktop re-reads its
	// whole catalog).
	hints chan wire.ComponentsChanged
	// closed ends the stream: the member moved to another replica
	// (closeStreams); the desktop reconnects to the new owner.
	closed chan struct{}

	// installs caches the member's opt-in plugin installs (fan-out worker only).
	installs     map[string]bool
	installsAtMs int64
	installsOK   bool
}

// pendingDelivery is a delivery sent to a stream and awaiting its ack.
type pendingDelivery struct {
	plugin   string
	subject  string
	tenantID string // the event's tenant
	event    string
	atMs     int64
	// group is set for a notification delivery: its outcome decides the
	// member's server-notifier fallback.
	group *deliveryGroup
}

// NewPluginHookDispatcher creates the realtime plugin hook dispatcher. It
// delivers nothing until NewComponentPipeline wires it to the install records.
func NewPluginHookDispatcher(audit store.ExtensionAuditStore, installs store.ComponentInstallStore, now func() int64) *PluginHookDispatcher {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &PluginHookDispatcher{
		audit:    audit,
		installs: installs,
		now:      now,
		queue:    make(chan pluginEvent, pluginQueueSize),
		streams:  map[*pluginStream]struct{}{},
		pending:  map[string]pendingDelivery{},
	}
}

// NewComponentPipeline builds realtime's component lifecycle pipeline — the
// kinds realtime hosts: plugins (hook authority) plus any extra installers
// (the notifier directory's) — and wires the dispatcher to read the same
// install records the pipeline persists.
func NewComponentPipeline(settings store.PlatformSettingsStore, audit store.ExtensionAuditStore, dispatcher *PluginHookDispatcher, installs store.ComponentInstallStore, now func() int64, extra ...extpipe.KindInstaller) *extpipe.Pipeline {
	if dispatcher == nil {
		dispatcher = NewPluginHookDispatcher(audit, installs, now)
	}
	installers := map[extreg.Kind]extpipe.KindInstaller{pkgmeta.KindPlugin: pluginruntime.Installer(dispatcher.Activate, dispatcher.Deactivate)}
	for _, in := range extra {
		installers[in.Kind] = in
	}
	pipe := &extpipe.Pipeline{
		Settings:   settings,
		Audit:      audit,
		Installers: installers,
		Now:        now,
	}
	dispatcher.audit = audit
	dispatcher.installs = installs
	if settings != nil {
		dispatcher.records = func(ctx context.Context) ([]extpipe.Record, error) {
			return pipe.List(ctx, pkgmeta.KindPlugin)
		}
	}
	return pipe
}

// Activate registers an active plugin record. There is no server-side driver
// (the sidecar lives on member machines); the decoded config is the registry
// instance, and the record snapshot is invalidated so the change applies to
// the next event at once.
func (d *PluginHookDispatcher) Activate(_ context.Context, _ extpipe.Record, cfg pluginruntime.Config, _ extgov.Guard) (any, error) {
	d.invalidateRecords()
	return cfg, nil
}

// Deactivate drops a plugin: the snapshot is invalidated so it stops at once.
func (d *PluginHookDispatcher) Deactivate(_ context.Context, _ extpipe.Record) error {
	d.invalidateRecords()
	return nil
}

func (d *PluginHookDispatcher) invalidateRecords() {
	d.mu.Lock()
	d.snapshot = recordSnapshot{}
	d.mu.Unlock()
}

// currentRecords returns the install records, from the snapshot while it is
// younger than recordsTTL, else from the store. A failed read clears the
// snapshot and returns the error: the caller delivers nothing (fail closed).
func (d *PluginHookDispatcher) currentRecords(ctx context.Context) ([]extpipe.Record, error) {
	if d.records == nil {
		return nil, nil
	}
	now := d.now()
	d.mu.Lock()
	snap := d.snapshot
	d.mu.Unlock()
	if snap.valid && now-snap.atMs < recordsTTL.Milliseconds() {
		return snap.recs, nil
	}
	recs, err := d.records(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.snapshot = recordSnapshot{}
		return nil, err
	}
	d.snapshot = recordSnapshot{recs: recs, atMs: now, valid: true}
	return recs, nil
}

// Hooked reports whether events of kind feed a plugin hook (only those are
// fanned out, locally or to peers).
func (d *PluginHookDispatcher) Hooked(kind string) bool {
	_, _, ok := hookForEvent(kind)
	return ok
}

// Deliver is the publish-path tap: it hands a hook-bearing event to the
// fan-out worker without blocking. With no desktop stream connected there is
// no one to deliver to, so nothing is queued.
func (d *PluginHookDispatcher) Deliver(ctx context.Context, members membership.Reader, ev wire.Event) {
	if _, _, ok := hookForEvent(ev.Kind); !ok {
		return
	}
	d.mu.Lock()
	idle := len(d.streams) == 0
	d.mu.Unlock()
	if idle {
		return
	}
	d.start()
	select {
	case d.queue <- pluginEvent{tenantID: tenant.FromContext(ctx), ev: ev, members: members}:
	default:
		if n := d.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Printf("realtime: plugin hook queue full; dropped %d event(s) so far", n)
		}
	}
}

// start launches the fan-out worker once. One worker keeps each stream's
// deliveries in publish order.
func (d *PluginHookDispatcher) start() {
	d.startOnce.Do(func() { go d.run() })
}

func (d *PluginHookDispatcher) run() {
	sweep := time.NewTicker(pendingSweepInterval)
	defer sweep.Stop()
	for {
		select {
		case item := <-d.queue:
			d.fanOut(item)
		case <-sweep.C:
			d.expirePending()
		}
	}
}

// HasDesktop reports whether member holds a plugin event stream whose
// session is in tenantID from a desktop running plugin sidecars
// (notification routing: a desktop that may run a notification plugin). A
// stream held only for catalog hints does not count.
func (d *PluginHookDispatcher) HasDesktop(tenantID, member string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for st := range d.streams {
		if st.plugins && st.subject == member && st.tenantID == tenantID {
			return true
		}
	}
	return false
}

// DeliverNotification hands one member's notification to the fan-out
// worker without blocking (notify.DesktopRoute). done reports the outcome
// once: true when a plugin on the member's desktop delivered it, false when
// none took it — none installed for them, dropped, failed, or
// unacknowledged within pendingAckTimeout.
func (d *PluginHookDispatcher) DeliverNotification(tenantID string, n wire.PluginNotification, done func(delivered bool)) {
	group := newDeliveryGroup(done)
	d.start()
	select {
	case d.queue <- pluginEvent{tenantID: tenantID, notification: &n, group: group}:
	default:
		if c := d.dropped.Add(1); c == 1 || c%100 == 0 {
			log.Printf("realtime: plugin hook queue full; dropped %d event(s) so far", c)
		}
		group.resolve(false)
	}
}

// fanOutNotification offers one member's notification to each of their own
// streams (session in the event's tenant: a reply is posted with that
// session) for every entitled plugin subscribing to notification.v1. The
// member was already chosen by the notification emitter, so there is no
// room-membership probe here.
func (d *PluginHookDispatcher) fanOutNotification(item pluginEvent) {
	defer item.group.resolve(false) // release the fan-out hold
	n := item.notification
	spec, ok := pkgmeta.LookupHook(pkgmeta.NotificationHook)
	if !ok {
		return
	}
	ctx := tenant.NewContext(context.Background(), item.tenantID)
	plugins := d.pluginsForHook(ctx, pkgmeta.NotificationHook, item.tenantID)
	if len(plugins) == 0 {
		return
	}
	envelope, err := json.Marshal(wire.PluginEnvelope{
		Event: spec.Event, Version: spec.Version, TenantID: item.tenantID, RoomID: n.RoomID, Payload: n,
	})
	if err != nil {
		return
	}
	for _, st := range d.pluginStreams() {
		if st.subject != n.UserID || st.tenantID != item.tenantID {
			continue
		}
		for _, rec := range plugins {
			if d.streamMayUse(ctx, st, rec) {
				d.offer(st, rec.Name, item.tenantID, pkgmeta.NotificationHook, envelope, item.group)
			}
		}
	}
}

// fanOut sends one event to every entitled member stream.
func (d *PluginHookDispatcher) fanOut(item pluginEvent) {
	if item.notification != nil {
		d.fanOutNotification(item)
		return
	}
	spec, hookID, ok := hookForEvent(item.ev.Kind)
	if !ok {
		return
	}
	ctx := tenant.NewContext(context.Background(), item.tenantID)
	plugins := d.pluginsForHook(ctx, hookID, item.tenantID)
	if len(plugins) == 0 {
		return
	}
	envelope, err := json.Marshal(wire.PluginEnvelope{
		Event:    spec.Event,
		Version:  spec.Version,
		TenantID: item.tenantID,
		RoomID:   item.ev.RoomID,
		Payload:  item.ev.Payload,
	})
	if err != nil {
		return
	}
	event := spec.Event + ".v" + spec.Version
	memberOf := map[string]bool{} // subject -> in the event's room (probed once per event)
	for _, st := range d.pluginStreams() {
		for _, rec := range plugins {
			if !d.streamMayUse(ctx, st, rec) {
				continue
			}
			isMember, probed := memberOf[st.subject]
			if !probed {
				// A platform agent host's membership is not the member's
				// own room: its desktop's plugins get no events from it.
				isMember = membership.HumanMember(item.members, item.ev.RoomID, st.subject)
				memberOf[st.subject] = isMember
			}
			if !isMember {
				break
			}
			d.offer(st, rec.Name, item.tenantID, event, envelope, nil)
		}
	}
}

// pluginsForHook returns the active records subscribing to hookID that are in
// scope for the event's tenant.
func (d *PluginHookDispatcher) pluginsForHook(ctx context.Context, hookID, tenantID string) []extpipe.Record {
	recs, err := d.currentRecords(ctx)
	if err != nil {
		return nil // fail closed: without the records nothing is known to be installed
	}
	var out []extpipe.Record
	for _, rec := range recs {
		if pluginruntime.Subscribes(rec, hookID, tenantID) {
			out = append(out, rec)
		}
	}
	return out
}

// streamMayUse is the member-visibility gate (extpipe.MemberVisibility, the
// rule the gateway's member component catalog applies) for the stream's
// member in their session tenant: an opt-in record must be installed by them.
func (d *PluginHookDispatcher) streamMayUse(ctx context.Context, st *pluginStream, rec extpipe.Record) bool {
	return extpipe.MemberVisibilityWith(extpipe.Viewer{Subject: st.subject, Tenant: st.tenantID}, extpipe.GateForRecord(rec), func() int64 {
		if d.streamInstalled(ctx, st, rec.Name) {
			return 1 // the cache keeps whether, not when
		}
		return 0
	}).Installed
}

// streamInstalled reports whether the stream's member opted in to the plugin,
// from a per-stream cache refreshed every recordsTTL. A failed read denies.
func (d *PluginHookDispatcher) streamInstalled(ctx context.Context, st *pluginStream, name string) bool {
	if d.installs == nil {
		return false
	}
	now := d.now()
	if !st.installsOK || now-st.installsAtMs >= recordsTTL.Milliseconds() {
		installs, err := d.installs.ListComponentInstalls(ctx, st.subject)
		if err != nil {
			st.installsOK = false
			return false
		}
		st.installs = map[string]bool{}
		for _, in := range installs {
			if in.Kind == string(pkgmeta.KindPlugin) {
				st.installs[in.Name] = true
			}
		}
		st.installsAtMs, st.installsOK = now, true
	}
	return st.installs[name]
}

func hookForEvent(kind string) (pkgmeta.HookSpec, string, bool) {
	for _, hookID := range pkgmeta.RegisteredHooks() {
		spec, _ := pkgmeta.LookupHook(hookID)
		if spec.Event == kind {
			return spec, hookID, true
		}
	}
	return pkgmeta.HookSpec{}, "", false
}
