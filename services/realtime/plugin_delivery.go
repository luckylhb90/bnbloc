package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// maxAckDetailBytes bounds the desktop-reported failure detail kept in audit.
const maxAckDetailBytes = 512

var errTooManyPluginStreams = errors.New("too many plugin event streams for this member")

// registerPluginRoutes mounts the member plugin event stream and its ack.
// Both act only for the authenticated subject (X-Bnbloc-User).
func (s *Service) registerPluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/my/plugin-events", s.handlePluginEvents)
	mux.HandleFunc("POST /v1/my/plugin-events/{id}/ack", s.handlePluginEventAck)
}

// handlePluginEvents streams the caller's plugin hook deliveries as SSE
// (event: plugin_event, data: wire.PluginDelivery) and catalog hints (event:
// components_changed, data: wire.ComponentsChanged). The member's desktop
// holds it open while its sidecar manager runs, with ?plugins=1 while it
// runs a plugin sidecar.
func (s *Service) handlePluginEvents(w http.ResponseWriter, r *http.Request) {
	subject := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if subject == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		obs.ServerError(w, "streaming unsupported", nil)
		httpx.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// ?plugins=1: the desktop runs a plugin sidecar. Without it the stream
	// carries catalog hints only.
	st, err := s.PluginHooks.subscribe(subject, tenant.FromContext(r.Context()), r.URL.Query().Get("plugins") == "1")
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	defer s.PluginHooks.unsubscribe(st)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	// Same keep-alive contract as the room stream: an idle stream would be
	// dropped by proxies and by the desktop's read-idle timeout.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-st.closed:
			return // the member is owned by another replica now
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case delivery := <-st.ch:
			data, err := json.Marshal(delivery)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", delivery.ID, wire.PluginEventKind, data)
			flusher.Flush()
		case hint := <-st.hints:
			data, err := json.Marshal(hint)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wire.ComponentsChangedKind, data)
			flusher.Flush()
		}
	}
}

// handlePluginEventAck records the desktop's outcome for one delivery. Only
// the member the delivery was sent to may acknowledge it; an unknown, expired
// or foreign id is 404.
func (s *Service) handlePluginEventAck(w http.ResponseWriter, r *http.Request) {
	subject := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if subject == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var ack wire.PluginDeliveryAck
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &ack) {
		return
	}
	if ack.Status != wire.PluginDeliveryDelivered && ack.Status != wire.PluginDeliveryFailed {
		httpx.Error(w, `status must be "delivered" or "failed"`, http.StatusBadRequest)
		return
	}
	if !s.PluginHooks.ack(r.Context(), subject, r.PathValue("id"), ack) {
		httpx.Error(w, "delivery not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *PluginHookDispatcher) subscribe(subject, tenantID string, plugins bool) (*pluginStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for st := range d.streams {
		if st.subject == subject {
			n++
		}
	}
	if n >= maxPluginStreamsPerSubject {
		return nil, errTooManyPluginStreams
	}
	st := &pluginStream{subject: subject, tenantID: tenantID, plugins: plugins, ch: make(chan wire.PluginDelivery, pluginStreamBuffer),
		hints: make(chan wire.ComponentsChanged, 1), closed: make(chan struct{})}
	d.streams[st] = struct{}{}
	return st, nil
}

// unsubscribe detaches a stream. Deliveries already sent on it stay pending
// until acked (the ack is a separate request) or expired.
func (d *PluginHookDispatcher) unsubscribe(st *pluginStream) {
	d.mu.Lock()
	delete(d.streams, st)
	d.mu.Unlock()
}

// closeStreams ends the streams of every member owned reports false for (the
// member's plugin streams moved to another replica) and returns how many.
// Deliveries already sent stay pending here until acked or expired: the ack
// is routed to the member's owner, so an in-flight one may expire (audited
// failed) — the same outcome as a desktop that dropped its stream.
func (d *PluginHookDispatcher) closeStreams(owned func(member string) bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for st := range d.streams {
		if !owned(st.subject) {
			delete(d.streams, st)
			close(st.closed)
			n++
		}
	}
	return n
}

// pluginStreams are the connected streams of desktops running plugin
// sidecars: the only ones plugin deliveries go to.
func (d *PluginHookDispatcher) pluginStreams() []*pluginStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*pluginStream, 0, len(d.streams))
	for st := range d.streams {
		if st.plugins {
			out = append(out, st)
		}
	}
	return out
}

// hasStream reports whether subject holds any member stream here.
func (d *PluginHookDispatcher) hasStream(subject string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for st := range d.streams {
		if st.subject == subject {
			return true
		}
	}
	return false
}

// componentsChanged queues a catalog hint on every connected stream it
// concerns (the hint's tenant, "" = all; its member, "" = all) without
// blocking, and reports how many streams it reached. A stream with a hint
// already queued needs no second one.
func (d *PluginHookDispatcher) componentsChanged(c wire.ComponentsChanged) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for st := range d.streams {
		if (c.TenantID != "" && st.tenantID != c.TenantID) || (c.Member != "" && st.subject != c.Member) {
			continue
		}
		select {
		case st.hints <- c:
		default:
		}
		n++
	}
	return n
}

// offer queues one delivery on a stream without blocking. A full pending
// table or stream buffer drops it, audited as failed. group (a notification
// delivery) learns every outcome.
func (d *PluginHookDispatcher) offer(st *pluginStream, plugin, tenantID, event string, envelope json.RawMessage, group *deliveryGroup) {
	id := strconv.FormatInt(d.now(), 36) + "-" + strconv.FormatUint(d.seq.Add(1), 36)
	p := pendingDelivery{plugin: plugin, subject: st.subject, tenantID: tenantID, event: event, atMs: d.now(), group: group}
	group.add()
	d.mu.Lock()
	if len(d.pending) >= maxPendingDeliveries {
		d.mu.Unlock()
		d.auditDelivery(p, wire.PluginDeliveryFailed, "dropped: too many deliveries awaiting acknowledgement")
		group.resolve(false)
		return
	}
	d.pending[id] = p
	d.mu.Unlock()
	select {
	case st.ch <- wire.PluginDelivery{ID: id, Plugin: plugin, Envelope: envelope}:
	default:
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		d.auditDelivery(p, wire.PluginDeliveryFailed, "dropped: member stream buffer full")
		group.resolve(false)
	}
}

// ack resolves a pending delivery for its own member and audits the outcome.
func (d *PluginHookDispatcher) ack(_ context.Context, subject, id string, ack wire.PluginDeliveryAck) bool {
	d.mu.Lock()
	p, ok := d.pending[id]
	if !ok || p.subject != subject {
		d.mu.Unlock()
		return false
	}
	delete(d.pending, id)
	d.mu.Unlock()
	detail := p.event
	if ack.Status == wire.PluginDeliveryFailed {
		detail = strings.TrimSpace(ack.Detail)
		if len(detail) > maxAckDetailBytes {
			detail = detail[:maxAckDetailBytes]
		}
		if detail == "" {
			detail = "desktop reported failure"
		}
		detail = p.event + ": " + detail
	}
	d.auditDelivery(p, ack.Status, detail)
	p.group.resolve(ack.Status == wire.PluginDeliveryDelivered)
	return true
}

// expirePending audits deliveries no desktop acknowledged in time as failed.
func (d *PluginHookDispatcher) expirePending() {
	cutoff := d.now() - pendingAckTimeout.Milliseconds()
	var expired []pendingDelivery
	d.mu.Lock()
	for id, p := range d.pending {
		if p.atMs <= cutoff {
			expired = append(expired, p)
			delete(d.pending, id)
		}
	}
	d.mu.Unlock()
	for _, p := range expired {
		d.auditDelivery(p, wire.PluginDeliveryFailed, p.event+": not acknowledged by the member's desktop")
		p.group.resolve(false)
	}
}

// auditDelivery records one delivery outcome: the plugin, the member whose
// desktop it went to (Actor), and the event's tenant.
func (d *PluginHookDispatcher) auditDelivery(p pendingDelivery, outcome, detail string) {
	if d.audit == nil {
		return
	}
	ctx := tenant.NewContext(context.Background(), p.tenantID)
	_ = d.audit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: string(pkgmeta.KindPlugin), Name: p.plugin, Action: "deliver", Actor: p.subject,
		Outcome: outcome, Detail: detail, TenantID: p.tenantID, AtMs: d.now(),
	})
}
