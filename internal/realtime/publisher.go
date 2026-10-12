package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Publisher fans an event out to a room's live subscribers. ctx carries the
// tenant the event belongs to (tenant.FromContext): realtime persists feed
// events under that tenant, so it must be the room's tenant, never a default.
type Publisher interface {
	Publish(ctx context.Context, ev wire.Event)
}

// HubPublisher adapts a local *Hub to Publisher for in-process composition
// (single-binary embedding and tests).
type HubPublisher struct{ Hub *Hub }

// Publish fans the event out through the local hub.
func (p HubPublisher) Publish(_ context.Context, ev wire.Event) { p.Hub.Publish(ev) }

// HTTPPublisher forwards events to bnbloc-realtime's internal ingest endpoint
// (POST /internal/v1/rooms/{roomId}/publish) so a separate service process
// (activity, room) reaches realtime subscribers. It is the ONE cross-process
// publisher: every request asserts the calling service's identity, carries the
// event's tenant, and is svcauth-signed over both, so realtime's internal route
// accepts it only from a holder of BNBLOC_INTERNAL_KEY and persists it under the
// right tenant.
//
// Publishing is best-effort: realtime reduces latency but is not authority, so
// a failed publish is logged and never fails the caller's authoritative write.
type HTTPPublisher struct {
	BaseURL string
	// Service names the calling service; asserted as svcauth.ServiceSubject.
	Service string
	// Key signs the assertion; empty only in dev (svcauth dev mode).
	Key  string
	HTTP *http.Client
}

// NewHTTPPublisher constructs the cross-process publisher for service, signing
// with the configured internal key. An empty baseURL disables publishing.
func NewHTTPPublisher(baseURL, service string) *HTTPPublisher {
	return &HTTPPublisher{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Service: service,
		Key:     svcauth.KeyFromEnv(),
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Publish posts the event to realtime's internal ingest under ctx's tenant.
func (p *HTTPPublisher) Publish(ctx context.Context, ev wire.Event) {
	if p == nil || p.BaseURL == "" || ev.RoomID == "" {
		return
	}
	tenantID := tenant.FromContext(ctx)
	if tenantID == "" {
		// Publishing without a tenant would persist the event under the default
		// tenant, where a non-default tenant's history never finds it. Every
		// caller binds the room's tenant; a missing one is a caller bug.
		log.Printf("realtime publisher(%s): dropping %s for room=%s: no tenant in context", p.Service, ev.Kind, ev.RoomID)
		return
	}
	buf, err := json.Marshal(ev)
	if err != nil {
		log.Printf("realtime publisher(%s): marshal %s: %v", p.Service, ev.Kind, err)
		return
	}
	p.post(ctx, "/internal/v1/rooms/"+url.PathEscape(ev.RoomID)+"/publish", tenantID, buf, ev.Kind+" room="+ev.RoomID)
}

// ComponentsChanged tells realtime that the executable component catalog
// changed (POST /internal/v1/components/changed): realtime queues a
// components_changed hint on the member desktop streams it concerns, on
// every replica, so desktops refresh their catalog now instead of at their
// next poll. Best-effort like Publish: the change itself already succeeded.
func (p *HTTPPublisher) ComponentsChanged(ctx context.Context, c wire.ComponentsChanged) {
	if p == nil || p.BaseURL == "" {
		return
	}
	buf, err := json.Marshal(c)
	if err != nil {
		log.Printf("realtime publisher(%s): marshal components_changed: %v", p.Service, err)
		return
	}
	p.post(ctx, "/internal/v1/components/changed", c.TenantID, buf, "components_changed")
}

// post makes one signed internal call to realtime as p.Service in tenantID.
func (p *HTTPPublisher) post(ctx context.Context, path, tenantID string, buf []byte, what string) {
	// The publish outlives neither its timeout nor needs the caller's
	// cancellation: a handler that has already written its response must still
	// deliver the event.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	req, err := svcauth.Key(p.Key).NewRequest(pctx, http.MethodPost, p.BaseURL+path, bytes.NewReader(buf), svcauth.ServiceSubject(p.Service), tenantID)
	if err != nil {
		log.Printf("realtime publisher(%s): request: %v", p.Service, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("realtime publisher(%s): publish %s: %v", p.Service, what, err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		log.Printf("realtime publisher(%s): publish %s: status %d", p.Service, what, resp.StatusCode)
	}
}
