package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/pluginruntime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// DesktopChannel is the outbox notifier value of the built-in desktop
// channel: a member's own notification plugin (the notification.v1 hook,
// run by their desktop app), with the server notifiers as its durable
// fallback. A component name can never contain ':' (pkgmeta), so it never
// collides with an installed notifier.
//
// Any producer (realtime, shareagent, the gateway) queues ONE DesktopChannel
// item per event while a notification plugin is distributed to the tenant
// (Emitter.Emit). Only realtime holds the members' plugin event streams, so
// its outbox worker routes it (Worker.attemptDesktop):
//
//  1. First claim: the recipients split into members whose desktop holds a
//     plugin stream (Desktop.HasDesktop) and everyone else. The split is
//     persisted on the item (NotificationDispatched: claim kept, outcome
//     deadline desktopOutcomeWindow) BEFORE any side effect; then everyone
//     else is queued to the server notifiers under the event id, and each
//     routed member's notification is handed to their desktop.
//  2. Each member's outcome is recorded as it arrives: delivered drops them
//     from the item; not delivered (failed, expired, nothing entitled)
//     queues their server delivery under FallbackRowID and drops them. The
//     last outcome finishes the item.
//  3. A claim after the deadline (realtime restarted, or an outcome could
//     not be recorded) falls back for every member still pending. Every
//     server row id is derived (event id, FallbackRowID) and the outbox
//     insert is idempotent, so a repeated fallback queues nothing twice.
const DesktopChannel = "builtin:desktop"

// desktopOutcomeWindow is how long a routed member's outcome may take before
// a later claim presumes it lost and falls back: it outlasts the plugin
// dispatcher's acknowledgement timeout (60 s) plus its expiry sweep.
const desktopOutcomeWindow = 2 * time.Minute

// DesktopRoute hands one member's notification to their own desktop
// notification plugin (the notification.v1 hook over the member's plugin
// event stream). Realtime's plugin hook dispatcher implements it.
type DesktopRoute interface {
	// HasDesktop reports, without I/O, whether member currently holds a
	// plugin event stream in tenantID (a desktop that may run a plugin).
	HasDesktop(tenantID, member string) bool
	// DeliverNotification hands n to member's entitled notification plugins
	// without blocking. done runs exactly once, on its own goroutine: true
	// when a plugin delivered it; false when none took it (none entitled or
	// running, dropped, failed, or not acknowledged in time).
	DeliverNotification(tenantID string, n wire.PluginNotification, done func(delivered bool))
}

// desktopItem is the body of a DesktopChannel outbox item.
type desktopItem struct {
	// Payload is the notifier payload; Recipients are the members still to
	// route (before the split) or still awaiting their desktop's outcome.
	Payload wire.NotificationPayload `json:"payload"`
	// ChannelID and Seq anchor the plugin payload at the message.
	ChannelID string `json:"channelId,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	// Routed is set once the split is persisted; Server are the members
	// queued to the server notifiers under the event id.
	Routed bool     `json:"routed,omitempty"`
	Server []string `json:"server,omitempty"`
}

// deepLink is the message anchor a DesktopChannel item keeps.
type deepLink struct {
	channelID string
	seq       int64
}

// NewPluginDirectory reads the plugin install records (the notification
// plugins that make an event go through DesktopChannel).
func NewPluginDirectory(settings store.PlatformSettingsStore) *Directory {
	pipe := &extpipe.Pipeline{Settings: settings}
	return &Directory{records: func(ctx context.Context) ([]extpipe.Record, error) {
		return pipe.List(ctx, pkgmeta.KindPlugin)
	}}
}

// desktopRoutable reports whether a notification plugin is distributed to
// tenantID. Member entitlement (opt-in install, session tenant) is checked
// by the worker per member; a member it does not admit falls back. A read
// error answers false: the event goes straight to the server notifiers.
func (e *Emitter) desktopRoutable(ctx context.Context, tenantID string) bool {
	if e.Plugins == nil {
		return false
	}
	recs, err := e.Plugins.Records(ctx)
	if err != nil {
		return false
	}
	for _, rec := range recs {
		if pluginruntime.Subscribes(rec, pkgmeta.NotificationHook, tenantID) {
			return true
		}
	}
	return false
}

// EnqueueServer queues payload's server delivery for members under rowID:
// one item per notifier component visible to the tenant (opt-in ones only
// for members who installed them). Re-queueing the same rowID is a no-op.
func (e *Emitter) EnqueueServer(ctx context.Context, payload wire.NotificationPayload, rowID string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	targets, err := e.Notifiers.Targets(ctx, payload.TenantID)
	if err != nil {
		return err
	}
	_, err = e.enqueue(ctx, payload, rowID, e.notifierChannels(ctx, targets, members), e.now(), deepLink{})
	return err
}

// pluginNotification renders a DesktopChannel item as one member's
// notification.v1 payload, with the reply addressing spelled out for a chat
// post.
func pluginNotification(d desktopItem, member string) wire.PluginNotification {
	p := d.Payload
	n := wire.PluginNotification{
		ID: p.ID, Type: p.Type, UserID: member, RoomID: p.RoomID, RoomName: p.RoomName,
		ChannelID: d.ChannelID, Seq: d.Seq, Actor: p.Actor, Title: p.Title, Text: p.Text, URL: p.URL, AtMs: p.AtMs,
	}
	if p.Reply != nil && p.RoomID != "" {
		r := &wire.PluginNotificationReply{RoomID: p.RoomID, ChannelID: p.Reply.ChannelID, To: wire.PluginReplyToRoom}
		if p.Reply.Recipient != nil {
			agent := *p.Reply.Recipient
			r.To, r.Agent = wire.PluginReplyToAgent, &agent
		}
		n.Reply = r
	}
	return n
}

// attemptDesktop processes one claimed DesktopChannel item (see
// DesktopChannel for the protocol).
func (w *Worker) attemptDesktop(ctx context.Context, token string, it store.NotificationOutboxItem) {
	tctx := tenant.NewContext(ctx, it.TenantID)
	var d desktopItem
	if err := json.Unmarshal(it.EventJSON, &d); err != nil || w.Emitter == nil {
		reason := "desktop routing unavailable in this process"
		if err != nil {
			reason = "undecodable outbox item: " + err.Error()
		}
		w.finish(tctx, it, store.NotificationAttempt{Token: token, Outcome: store.NotificationFailed, Error: reason})
		return
	}
	if d.Routed {
		w.desktopDeadline(tctx, token, it, d)
		return
	}
	var routed []string
	for _, r := range d.Payload.Recipients {
		if w.Desktop != nil && w.Desktop.HasDesktop(it.TenantID, r.UserID) {
			routed = append(routed, r.UserID)
		} else {
			d.Server = append(d.Server, r.UserID)
		}
	}
	d.Routed = true
	d.Payload.Recipients = recipientIDs(routed)
	body, err := json.Marshal(d)
	if err != nil {
		w.finish(tctx, it, store.NotificationAttempt{Token: token, Outcome: store.NotificationFailed, Error: err.Error()})
		return
	}
	deadline := w.now().Add(desktopOutcomeWindow).UnixMilli()
	// Persist the split before any side effect: a crash from here on is
	// resumed by the deadline claim with exactly this split.
	if err := w.Outbox.RecordNotificationAttempt(ctx, store.NotificationAttempt{
		TenantID: it.TenantID, ID: it.ID, Notifier: it.Notifier, Token: token,
		Outcome: store.NotificationDispatched, NextAttemptAtMs: deadline, EventJSON: body, AtMs: w.now().UnixMilli(),
	}); err != nil {
		slog.Warn("bnbloc notifications: record desktop dispatch failed",
			"event", "bnbloc.notify.record_failed", "id", it.ID, "notifier", it.Notifier, "error", err)
		return
	}
	run := &desktopRun{w: w, it: it, token: token, item: d, deadline: deadline}
	if err := w.Emitter.EnqueueServer(tctx, d.Payload, d.Payload.ID, d.Server); err != nil {
		// Left to the deadline claim, which re-queues the server rows; the
		// item must not finish before they exist.
		slog.Warn("bnbloc notifications: server enqueue failed",
			"event", "bnbloc.notify.enqueue_failed", "id", it.ID, "error", err)
		run.serverPending = true
	}
	if len(routed) == 0 {
		if !run.serverPending {
			w.finish(tctx, it, store.NotificationAttempt{Token: token, Outcome: store.NotificationDelivered})
		}
		return
	}
	for _, m := range routed {
		member := m
		w.Desktop.DeliverNotification(it.TenantID, pluginNotification(d, member), func(delivered bool) { run.outcome(member, delivered) })
	}
}

// desktopDeadline handles a routed item claimed again after its outcome
// deadline (a restart, or an outcome that could not be recorded): it
// re-queues the server rows and falls back for every member still pending.
// All row ids are derived, so nothing is queued twice.
func (w *Worker) desktopDeadline(ctx context.Context, token string, it store.NotificationOutboxItem, d desktopItem) {
	err := w.Emitter.EnqueueServer(ctx, d.Payload, d.Payload.ID, d.Server)
	var fellBack []string
	for _, r := range d.Payload.Recipients {
		if err != nil {
			break
		}
		err = w.Emitter.EnqueueServer(ctx, d.Payload, FallbackRowID(d.Payload.ID, r.UserID), []string{r.UserID})
		fellBack = append(fellBack, r.UserID)
	}
	if err != nil {
		w.finish(ctx, it, store.NotificationAttempt{Token: token, Outcome: store.NotificationRetry, Error: "fallback enqueue: " + err.Error()})
		return
	}
	detail := "desktop outcome deadline passed"
	if len(fellBack) > 0 {
		detail += "; fell back to the server notifiers for: " + strings.Join(fellBack, ", ")
	}
	w.finish(ctx, it, store.NotificationAttempt{Token: token, Outcome: store.NotificationDelivered, Error: detail})
}

// finish records a final (or retry) attempt on a DesktopChannel item, with
// the worker's usual retry cap, audit and metrics.
func (w *Worker) finish(ctx context.Context, it store.NotificationOutboxItem, a store.NotificationAttempt) {
	a.TenantID, a.ID, a.Notifier = it.TenantID, it.ID, it.Notifier
	if a.Outcome == store.NotificationRetry {
		if it.Attempts+1 >= w.MaxAttempts {
			a.Outcome = store.NotificationFailed
		} else {
			a.NextAttemptAtMs = w.now().Add(w.backoff(it.Attempts)).UnixMilli()
		}
	}
	a.AtMs = w.now().UnixMilli()
	if err := w.Outbox.RecordNotificationAttempt(ctx, a); err != nil {
		slog.Warn("bnbloc notifications: record attempt failed",
			"event", "bnbloc.notify.record_failed", "id", it.ID, "notifier", it.Notifier, "error", err)
		return
	}
	w.audit(ctx, it, a)
	w.observeAttempt(a.Outcome)
}

// desktopRun is one routed DesktopChannel item awaiting its members'
// desktop outcomes in this process. Outcomes arrive on their own goroutines
// and are applied one at a time.
type desktopRun struct {
	w        *Worker
	it       store.NotificationOutboxItem
	token    string
	deadline int64

	mu            sync.Mutex
	item          desktopItem // Payload.Recipients = members still pending
	serverPending bool        // the server rows could not be queued
	fellBack      []string
	closed        bool // the lease was lost or the item finished
}

// outcome applies one member's desktop outcome: a member whose plugin did
// not deliver gets their server delivery; the item then shrinks to the
// members still pending, or finishes when none is left.
func (r *desktopRun) outcome(member string, delivered bool) {
	ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), r.it.TenantID), 10*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	resolved := true
	if !delivered {
		if err := r.w.Emitter.EnqueueServer(ctx, r.item.Payload, FallbackRowID(r.item.Payload.ID, member), []string{member}); err != nil {
			// The member stays pending: the deadline claim falls back.
			slog.Warn("bnbloc notifications: desktop fallback enqueue failed",
				"event", "bnbloc.notify.enqueue_failed", "id", r.it.ID, "user", member, "error", err)
			resolved = false
		} else {
			r.fellBack = append(r.fellBack, member)
		}
	}
	if resolved {
		var keep []wire.NotificationRecipient
		for _, rc := range r.item.Payload.Recipients {
			if rc.UserID != member {
				keep = append(keep, rc)
			}
		}
		r.item.Payload.Recipients = keep
	}
	if len(r.item.Payload.Recipients) == 0 && !r.serverPending {
		detail := ""
		if len(r.fellBack) > 0 {
			detail = "fell back to the server notifiers for: " + strings.Join(r.fellBack, ", ")
		}
		r.w.finish(ctx, r.it, store.NotificationAttempt{Token: r.token, Outcome: store.NotificationDelivered, Error: detail})
		r.closed = true
		return
	}
	if !resolved {
		return // nothing changed on the item
	}
	body, err := json.Marshal(r.item)
	if err == nil {
		err = r.w.Outbox.RecordNotificationAttempt(ctx, store.NotificationAttempt{
			TenantID: r.it.TenantID, ID: r.it.ID, Notifier: r.it.Notifier, Token: r.token,
			Outcome: store.NotificationDispatched, NextAttemptAtMs: r.deadline, EventJSON: body, AtMs: r.w.now().UnixMilli(),
		})
	}
	if err != nil {
		// Lease lost (the deadline passed and the item was claimed again):
		// that claim settles the members still pending.
		slog.Warn("bnbloc notifications: record desktop outcome failed",
			"event", "bnbloc.notify.record_failed", "id", r.it.ID, "user", member, "error", err)
		r.closed = true
	}
}
