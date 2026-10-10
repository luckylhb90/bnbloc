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
	// The publish outlives neither its timeout nor needs the caller's
	// cancellation: a handler that has already written its response must still
	// deliver the event.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	target := p.BaseURL + "/internal/v1/rooms/" + url.PathEscape(ev.RoomID) + "/publish"
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, target, bytes.NewReader(buf))
	if err != nil {
		log.Printf("realtime publisher(%s): request: %v", p.Service, err)
		return
	}
	subject := svcauth.ServiceSubject(p.Service)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(svcauth.HeaderUser, subject)
	req.Header.Set(svcauth.HeaderTenant, tenantID)
	svcauth.Sign(req, p.Key, subject)
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("realtime publisher(%s): publish %s room=%s: %v", p.Service, ev.Kind, ev.RoomID, err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		log.Printf("realtime publisher(%s): publish %s room=%s: status %d", p.Service, ev.Kind, ev.RoomID, resp.StatusCode)
	}
}
