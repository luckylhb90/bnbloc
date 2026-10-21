// Metrics: a tiny, dependency-free Prometheus text exposition. bnbloc keeps a
// minimal dependency surface (air-gapped friendly), so rather than pull in a
// client library this records a few in-process counters/latency buckets and
// renders them in the Prometheus text format at /metrics.
package obs

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// latencyBucketsMs are cumulative histogram upper bounds in milliseconds.
var latencyBucketsMs = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// Metrics collects per-(method,route,status) request counts and latency, plus
// arbitrary named counters (e.g. borrow allow/deny, rate-limit hits). Safe for
// concurrent use. One instance per service process.
type Metrics struct {
	service  string
	mu       sync.Mutex
	reqs     map[reqKey]*reqStat
	counters map[string]float64
	// families holds the labeled domain series (series.go).
	families map[string]*family
}

type reqKey struct {
	method string
	route  string
	status int
}

type reqStat struct {
	count      uint64
	sumMs      float64
	bucketHits []uint64 // len == len(latencyBucketsMs)
}

// NewMetrics constructs an empty collector for a service.
func NewMetrics(service string) *Metrics {
	return &Metrics{
		service:  service,
		reqs:     map[reqKey]*reqStat{},
		counters: map[string]float64{},
	}
}

// observeRequest records one HTTP request's route/status/latency.
func (m *Metrics) observeRequest(method, route string, status int, ms float64) {
	if m == nil {
		return
	}
	k := reqKey{method: method, route: route, status: status}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.reqs[k]
	if st == nil {
		st = &reqStat{bucketHits: make([]uint64, len(latencyBucketsMs))}
		m.reqs[k] = st
	}
	st.count++
	st.sumMs += ms
	for i, b := range latencyBucketsMs {
		if ms <= b {
			st.bucketHits[i]++
		}
	}
}

// Inc increments a named counter (e.g. "borrow_denied_total"). Labels may be
// baked into the name by the caller for simplicity.
func (m *Metrics) Inc(name string) { m.Add(name, 1) }

// Add adds to a named counter.
func (m *Metrics) Add(name string, v float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters[name] += v
	m.mu.Unlock()
}

// Handler renders the current metrics in Prometheus text format.
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.mu.Lock()
		defer m.mu.Unlock()

		fmt.Fprintf(w, "# HELP bnbloc_http_requests_total HTTP requests by route and status.\n")
		fmt.Fprintf(w, "# TYPE bnbloc_http_requests_total counter\n")
		keys := make([]reqKey, 0, len(m.reqs))
		for k := range m.reqs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].route != keys[j].route {
				return keys[i].route < keys[j].route
			}
			if keys[i].method != keys[j].method {
				return keys[i].method < keys[j].method
			}
			return keys[i].status < keys[j].status
		})
		for _, k := range keys {
			st := m.reqs[k]
			lbl := fmt.Sprintf(`service=%q,method=%q,route=%q,status="%d"`,
				m.service, k.method, k.route, k.status)
			fmt.Fprintf(w, "bnbloc_http_requests_total{%s} %d\n", lbl, st.count)
		}

		fmt.Fprintf(w, "# HELP bnbloc_http_request_duration_ms Request latency histogram (ms).\n")
		fmt.Fprintf(w, "# TYPE bnbloc_http_request_duration_ms histogram\n")
		for _, k := range keys {
			st := m.reqs[k]
			base := fmt.Sprintf(`service=%q,method=%q,route=%q,status="%d"`,
				m.service, k.method, k.route, k.status)
			for i, b := range latencyBucketsMs {
				fmt.Fprintf(w, "bnbloc_http_request_duration_ms_bucket{%s,le=%q} %d\n",
					base, strconv.FormatFloat(b, 'f', -1, 64), st.bucketHits[i])
			}
			fmt.Fprintf(w, "bnbloc_http_request_duration_ms_bucket{%s,le=\"+Inf\"} %d\n", base, st.count)
			fmt.Fprintf(w, "bnbloc_http_request_duration_ms_sum{%s} %s\n",
				base, strconv.FormatFloat(st.sumMs, 'f', -1, 64))
			fmt.Fprintf(w, "bnbloc_http_request_duration_ms_count{%s} %d\n", base, st.count)
		}

		if len(m.counters) > 0 {
			names := make([]string, 0, len(m.counters))
			for n := range m.counters {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				metric := "bnbloc_" + n
				fmt.Fprintf(w, "# TYPE %s counter\n", metric)
				fmt.Fprintf(w, "%s{service=%q} %s\n",
					metric, m.service, strconv.FormatFloat(m.counters[n], 'f', -1, 64))
			}
		}
		m.writeSeriesLocked(w)
	}
}

// routeWords is the vocabulary of static route segments across bnbloc
// services. routeLabel keeps a segment verbatim only when it is one of these
// words; every other segment (room/user/member ids, invite codes, relay keys,
// artifact hashes, versions, anything a client invents) collapses to ":id".
// The label space is therefore bounded by the vocabulary, never by client
// input, and no secret-bearing segment can reach /metrics. A route word missing
// here only costs label detail (it reads as ":id"), never cardinality.
var routeWords = map[string]bool{
	"v1": true, "internal": true, "relay": true, "api": true, "auth": true,
	"rooms": true, "rooms-unread": true, "users": true, "invites": true, "redeem": true,
	"join": true, "leave": true, "transfer": true, "members": true, "membership": true, "tenant": true,
	"artifacts": true, "artifact-anchors": true, "versions": true, "state": true, "content": true, "thumbnail": true,
	"channels": true, "agent-prefs": true,
	"events": true, "agents": true, "chat": true, "attachments": true, "history": true,
	"typing": true, "read": true, "messages": true, "publish": true, "blobs": true,
	"agent-activity": true, "session-state": true,
	"bridge": true, "heartbeat": true, "my": true, "jobs": true, "claim": true, "status": true, "lease": true,
	"agent-jobs": true, "cancel": true, "agent-catalogs": true,
	"stream":        true,
	"shared-agents": true, "shared-agents-usage": true, "authorization": true, "record": true,
	"tenants": true, "platform": true, "quota": true, "settings": true, "audit": true, "usage": true,
	"directory": true, "join-requests": true, "join-request": true, "approve": true, "reject": true, "role": true,
	"login": true, "switch-tenant": true, "sso-config": true, "sso": true, "exchange": true, "init": true,
	"acs": true, "metadata": true, "logout": true, "local-agent": true, "run": true, "targets": true,
	"discover": true, "cores": true, "artifact": true, "components": true, "install": true,
	"component-keys": true, "rollout": true, "app": true, "update": true, "update-policy": true,
	"update-config": true, "releases": true, "app-releases": true, "meta": true,
	"export": true, "import": true, "handoff": true, "feed": true,
	"prompts": true, "use": true, "tasks": true, "search": true,
	"content-components": true, "content-uses": true, "effective": true,
	"refresh": true, "fork": true, "sessions": true,
	"team": true, "borrow": true, "usage-report": true, "quota-requests": true, "decline": true,
	"schedules": true, "api-tokens": true, "api-token": true, "alerts": true,
	"plugin-events": true, "ack": true,
	"reserve": true, "budget": true, "budgets": true,
	"audit-log": true, "org-sync": true, "rules": true, "preview": true, "deprovision": true, "resync": true,
	"public": true, "shares": true, "resolve": true, "presign": true, "site": true, "sites": true, "site-ticket": true, "ticket": true, "files": true,
	"tenant-policy": true, "platform-agents": true, "agent-hosts": true,
	"profile": true, "preferences": true,
	// realtime replica-to-replica routes (scale-out by key ownership).
	"desktop": true, "notifications": true, "post": true,
	// agent dispatch (room tools): /v1 + /internal/v1 rooms/{id}/dispatch.
	"dispatch": true,
	// expert teams: which agent takes each role, and its estimate.
	"team-plan": true,
	// the works gallery: a deliverable published as a work, its recipe,
	// and a draft started from one.
	"publish-work": true, "recipe": true, "make-similar": true,
	// room components: a room's draft and a room made from one.
	"component-draft": true, "create-room": true,
	// component settings: /v1/{platform,org}/components/{name}/settings and
	// a member's /v1/components/{name}/settings/resolved.
	"org": true, "resolved": true,
	// content sharing and approval: /v1/content-components/{id}/shares|
	// update|publish, /v1/tenant/content-requests/{id}/approve|decline,
	// /v1/{tenant,platform}/content-components/{id}/unpublish|restore|package.
	"content-requests": true, "unpublish": true, "restore": true, "package": true,
	// interactive apps: the ~app subtree of a site ticket or share link,
	// /v1/rooms/{id}/apps/{app}/…, the visitor token, the room service's
	// /internal/v1/app/… and realtime's app-events.
	"~app": true, "apps": true, "me": true, "stats": true, "data": true,
	"sdk.js": true, "sdk.d.ts": true, "visitor": true, "app-events": true, "cover": true,
}

// idCollections name segments whose NEXT segment is always an identifier, even
// when that identifier happens to equal a route word (a room named "chat").
var idCollections = map[string]bool{
	"rooms": true, "users": true, "invites": true, "members": true, "membership": true,
	"artifacts": true, "versions": true, "channels": true, "messages": true, "attachments": true,
	"jobs": true, "agent-jobs": true, "stream": true,
	"shared-agents": true, "tenants": true, "join-requests": true, "components": true,
	"cores": true, "component-keys": true, "app-releases": true, "blobs": true,
	"prompts": true, "tasks": true,
	"sessions": true, "quota-requests": true,
	"schedules": true, "api-tokens": true, "plugin-events": true,
	// platform-agents is not a collection: /v1/platform/platform-agents/usage
	// names a route; a room's /platform-agents/{provider} still collapses
	// (a provider is not a route word).
	"agent-hosts": true,
	// /v1/content-components/{id}, /v1/tenant/content-requests/{id}: an id
	// is never read as a route word ("effective" and "import" are routes
	// under content-components, so only the request collection is listed).
	"content-requests": true,
	// /v1/rooms/{id}/apps/{app}: an app id is never read as a route word.
	"apps": true,
}

// maxRouteSegments bounds the label depth so an arbitrarily deep path cannot
// mint a new label per extra segment.
const maxRouteSegments = 8

// routeLabel reduces a path to a bounded, secret-free route label.
func routeLabel(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) == 1 && segs[0] == "" {
		return "/"
	}
	var b strings.Builder
	prevCollection := false
	for i, seg := range segs {
		if i == maxRouteSegments {
			b.WriteString("/...")
			break
		}
		b.WriteByte('/')
		switch {
		case prevCollection:
			b.WriteString(":id")
		case routeWords[seg]:
			b.WriteString(seg)
		default:
			b.WriteString(":id")
		}
		prevCollection = !prevCollection && idCollections[seg]
	}
	return b.String()
}

// methodLabel bounds the method label to the standard verbs; anything else a
// client sends is one "OTHER" bucket.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return m
	default:
		return "OTHER"
	}
}
