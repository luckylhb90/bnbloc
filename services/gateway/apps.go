package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
)

// Interactive apps at the edge (docs/prd/2026-10-12-interactive-apps.md
// §4.1, §4.7, delivery step 3). An app is a published site; its id is the
// site artifact's id, so its data carries across versions. Its data API is
// served under three entry points, each a capability the request carries:
//
//	/v1/sites/{ticket}/~app/…                  a member's site ticket: the member (Kind member)
//	/v1/public/shares/{token}/site/~app/…      a public share link plus a visitor token: the visitor (Kind visitor)
//	/v1/rooms/{roomId}/apps/{artifactId}/…     a session: the member, or with ?job= the job's agent (Kind agent)
//
// with the same API under each (appAPI): c/{collection}[/{id}], me and
// events (the change feed); the room entry adds the owners' stats, export and data;
// sdk.js and sdk.d.ts are served under the two frame entries.
//
// appCapability is the ONE resolution of an app request's scope (tenant,
// room, app, participant): from the capability only, never from the query
// or body. A ticket names its room and artifact (it was minted for them), a
// share link resolves to them through the room service, the room entry
// takes them from its path under the session's room reach or a job grant
// in that room. Every call then goes through forwardApp (the room
// service's /internal/v1/app/…, signed as the gateway in the room's tenant
// with svcauth.Key.AssertApp) or the change feed (forwardAppEvents). The
// room service re-checks that the app is a site of that room.
//
// The frame entries take no cookie and no session: an app's frame has an
// opaque origin, so its calls are cross-origin, answered with CORS for any
// origin and no credentials (appFramePath, withCORS).

// appEntry is where an app request's capability comes from.
type appEntry int

const (
	appTicket appEntry = iota // a member's site ticket in the path
	appShare                  // a public share token in the path, plus a visitor token
	appRoom                   // a session, in the room of the path (?job=: the job's agent)
)

// appCapability is an app request's resolved scope.
type appCapability struct {
	// tenant is the room's tenant (the room service refuses another).
	tenant string
	scope  svcauth.AppScope
	// link is the capability's rate key: the share link, else the app.
	link string
	// still re-resolves the request's capability (serveApp sets it): false
	// once it no longer grants this scope. An open change feed asks it.
	still func(ctx context.Context) bool
}

// appRoute is how a route-table row serves an app request (Service.serve
// resolves the capability and spends the budgets first).
type appRoute struct {
	entry appEntry
	serve func(s *Service, w http.ResponseWriter, r *http.Request, c appCapability)
}

// appEntries are the three entry points: their path prefix and, for a
// route without a session, why.
var appEntries = []struct {
	entry  appEntry
	prefix string
	why    string
}{
	{appTicket, "/v1/sites/{ticket}/~app", "a member's site ticket in the path"},
	{appShare, "/v1/public/shares/{token}/site/~app", "a public share token in the path and a visitor token"},
	{appRoom, "/v1/rooms/{roomId}/apps/{artifactId}", ""},
}

// appAPI is the app data API under each entry: method, path below the
// prefix, and how it is served. ownersOnly routes are the room entry's
// only (an app's own page has no use for them).
var appAPI = []struct {
	method, path string
	ownersOnly   bool
	serve        func(s *Service, w http.ResponseWriter, r *http.Request, c appCapability)
}{
	{http.MethodPost, "/c/{collection}", false, forwardAppCollection},
	{http.MethodGet, "/c/{collection}", false, forwardAppCollection},
	{http.MethodGet, "/c/{collection}/{id}", false, forwardAppDoc},
	{http.MethodPut, "/c/{collection}/{id}", false, forwardAppDoc},
	{http.MethodPatch, "/c/{collection}/{id}", false, forwardAppDoc},
	{http.MethodDelete, "/c/{collection}/{id}", false, forwardAppDoc},
	{http.MethodGet, "/me", false, forwardAppPath("/me")},
	{http.MethodGet, "/events", false, (*Service).forwardAppEvents},
	{http.MethodGet, "/stats", true, forwardAppPath("/stats")},
	{http.MethodGet, "/export", true, forwardAppPath("/export")},
	{http.MethodDelete, "/data", true, forwardAppPath("/data")},
}

// appRoutes are the route-table rows of the app data API: every entry ×
// every call, reads on the read budgets and writes on the stricter write
// budgets (appRates); the room entry's writes need drive_agent (a viewer
// may read an app's data, not change it); the frame entries add the SDK
// and reserve the rest of ~app/.
func (s *Service) appRoutes() []route {
	var out []route
	for _, e := range appEntries {
		for _, a := range appAPI {
			if a.ownersOnly && e.entry != appRoom {
				continue
			}
			rt := route{pattern: a.method + " " + e.prefix + a.path, why: e.why, app: &appRoute{entry: e.entry, serve: a.serve}}
			rt.perm, rt.budget = rbac.PermRead, appReadBudget
			if a.method != http.MethodGet {
				rt.budget = appWriteBudget
				if e.entry == appRoom {
					rt.perm = rbac.PermDriveAgent
				}
			}
			out = append(out, rt)
		}
		if e.entry == appRoom {
			continue
		}
		out = append(out,
			open("GET "+e.prefix+"/sdk.js", "the app SDK: a static file of the gateway's, no data", s.handleAppSDK(appsdk.JS, "text/javascript; charset=utf-8")),
			open("GET "+e.prefix+"/sdk.d.ts", "the app SDK's types: a static file of the gateway's, no data", s.handleAppSDK(appsdk.Types, "text/plain; charset=utf-8")),
			open("GET "+e.prefix+"/{rest...}", "reserves the app API's namespace inside a site: 404", handleAppNotFound),
		)
	}
	return out
}

// appFramePath reports a path under a frame entry's ~app/: answered with
// CORS for any origin, without credentials (withCORS).
func appFramePath(path string) bool {
	return sitePath(path) && strings.Contains(path, "/~app/")
}

// appCORSHeaders are the request headers an app's frame sends.
var appCORSHeaders = strings.Join([]string{"Content-Type", "If-Match", "If-None-Match", "Accept", wire.AppVisitorHeader}, ", ")

// appError is a refused app request: its status, the code the SDK acts on
// (wire.AppErrorCode, or none) and the message.
type appError struct {
	status int
	code   string
	msg    string
}

func (e *appError) Error() string { return e.msg }

var errAppNotFound = &appError{status: http.StatusNotFound, msg: "app not found"}

// writeAppErr answers a capability resolution failure: the one status
// mapping of the app entries.
func writeAppErr(w http.ResponseWriter, err error) {
	var ae *appError
	var ge *grantError
	switch {
	case errors.As(err, &ae):
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, ae.status, httpx.ErrorBody{Error: ae.msg, Code: ae.code})
	case errors.As(err, &ge):
		writeGrantErr(w, err)
	case errors.Is(err, errShareGuessing), errors.Is(err, errShareNotFound):
		writeShareErr(w, err)
	case errors.Is(err, errSiteNotFound):
		writeAppErr(w, errAppNotFound)
	default:
		obs.ServerError(w, "resolve app capability failed", err)
		httpx.Error(w, "resolve app capability failed", http.StatusBadGateway)
	}
}

// appCapabilityOf resolves an app request's capability for its entry. The
// claims are the room entry's session (zero for the frame entries).
func (s *Service) appCapabilityOf(r *http.Request, entry appEntry, claims auth.Claims) (appCapability, error) {
	var (
		c   appCapability
		err error
	)
	switch entry {
	case appTicket:
		c, err = s.ticketApp(r)
	case appShare:
		c, err = s.shareApp(r)
	default:
		c, err = s.roomApp(r, claims)
	}
	if err == nil && !c.scope.Valid() {
		err = errAppNotFound
	}
	return c, err
}

// ticketApp: a member's site ticket names the room and the artifact it was
// minted for; the member's room-level read is re-checked like the site's
// files (memberTicket).
func (s *Service) ticketApp(r *http.Request) (appCapability, error) {
	claims, _, err := s.memberTicket(r.Context(), r.PathValue("ticket"))
	if err != nil {
		return appCapability{}, err
	}
	roomID, artifactID, _, _ := parseSiteTicketScope(claims.ID)
	return appCapability{
		tenant: claims.Tenant,
		scope:  svcauth.AppScope{RoomID: roomID, AppID: artifactID, Kind: svcauth.AppMember, Participant: claims.Subject},
		link:   "app:" + artifactID,
	}, nil
}

// shareApp: a public share link of a site, resolved on every call (a
// revoked or expired link, a disabled organization or a deleted room stops
// it at once), and the visitor token minted for that link.
func (s *Service) shareApp(r *http.Request) (appCapability, error) {
	token := strings.TrimSpace(r.Header.Get(wire.AppVisitorHeader))
	if token == "" {
		return appCapability{}, &appError{status: http.StatusUnauthorized, code: wire.AppVisitorRequired, msg: "a public link's app needs a visitor token from its share page"}
	}
	res, err := s.shareByToken(r, false, false)
	if err != nil {
		return appCapability{}, err
	}
	if res.Artifact.Kind != store.ArtifactKindSite {
		return appCapability{}, errAppNotFound
	}
	visitor, err := s.Signer.VerifyKind(token, auth.KindAppVisitor)
	if err != nil || visitor.ID != res.Share.ShareID || visitor.Subject == "" {
		return appCapability{}, &appError{status: http.StatusUnauthorized, code: wire.AppVisitorInvalid, msg: "the visitor token is not valid for this link"}
	}
	return appCapability{
		tenant: res.TenantID,
		scope:  svcauth.AppScope{RoomID: res.RoomID, AppID: res.Artifact.ArtifactID, Kind: svcauth.AppVisitor, Participant: visitor.Subject},
		link:   "share:" + res.Share.ShareID,
	}, nil
}

// roomApp: a session in the room of the path (its room reach), as the
// member; with ?job= a bridge running that job in the room, as the job's
// agent acting for its requester (jobGrant, the content routes' rule).
func (s *Service) roomApp(r *http.Request, caller auth.Claims) (appCapability, error) {
	roomID, artifactID := r.PathValue("roomId"), r.PathValue("artifactId")
	if roomctx.ValidateID(roomID) != nil {
		return appCapability{}, errAppNotFound
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		return appCapability{}, &appError{status: http.StatusForbidden, msg: "room scope not authorized"}
	}
	c := appCapability{
		tenant: claims.Tenant,
		scope:  svcauth.AppScope{RoomID: roomID, AppID: artifactID, Kind: svcauth.AppMember, Participant: claims.Subject},
		link:   "app:" + artifactID,
	}
	if job := strings.TrimSpace(r.URL.Query().Get("job")); job != "" {
		actor, err := s.jobGrant(r.Context(), caller, claims, roomID, job)
		if err != nil {
			return appCapability{}, err
		}
		c.tenant = actor.Tenant
		c.scope.Kind, c.scope.Participant = svcauth.AppAgent, jobAgent(*actor.Job)
	}
	return c, nil
}

// forwardAppCollection forwards c/{collection}.
func forwardAppCollection(s *Service, w http.ResponseWriter, r *http.Request, c appCapability) {
	s.forwardApp(w, r, c, "/c/"+url.PathEscape(r.PathValue("collection")))
}

// forwardAppDoc forwards c/{collection}/{id}.
func forwardAppDoc(s *Service, w http.ResponseWriter, r *http.Request, c appCapability) {
	s.forwardApp(w, r, c, "/c/"+url.PathEscape(r.PathValue("collection"))+"/"+url.PathEscape(r.PathValue("id")))
}

// forwardAppPath forwards a fixed path.
func forwardAppPath(path string) func(*Service, http.ResponseWriter, *http.Request, appCapability) {
	return func(s *Service, w http.ResponseWriter, r *http.Request, c appCapability) { s.forwardApp(w, r, c, path) }
}

// appForwardHeaders are the client headers an app call carries to the
// room service; nothing else of the client's request goes (no cookie, no
// session, no visitor token).
var appForwardHeaders = []string{"Content-Type", "If-Match", "If-None-Match", "Accept"}

// forwardApp is the one forward of an app data call: to the room service's
// /internal/v1/app{path} with the query (a find's filter, sort, limit,
// after) and the body (bounded by wire.MaxAppRequestBytes), signed as the
// gateway in the room's tenant (Assert) with the resolved scope
// (AssertApp). The room service's status and body are the answer.
func (s *Service) forwardApp(w http.ResponseWriter, r *http.Request, c appCapability, path string) {
	target, ok := s.appUpstream(w, s.Backends.Room, "/internal/v1/app"+path)
	if !ok {
		return
	}
	target.RawQuery = r.URL.RawQuery
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxAppRequestBytes)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			s.appRequest(pr, c, target)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("Cache-Control", "no-store")
			return nil
		},
		ErrorHandler: obs.ProxyError("app data proxy failed"),
	}
	proxy.ServeHTTP(w, r)
}

// appRequest makes pr's outbound request the internal call for c: target,
// only the forwarded client headers, the gateway's identity in the room's
// tenant and the app scope. A scope AssertApp refused (appCapabilityOf
// checked it) leaves the request without one, which the room service
// refuses.
func (s *Service) appRequest(pr *httputil.ProxyRequest, c appCapability, target *url.URL) {
	pr.Out.URL = target
	pr.Out.Host = ""
	in := pr.In.Header
	pr.Out.Header = http.Header{}
	for _, h := range appForwardHeaders {
		if v := in.Get(h); v != "" {
			pr.Out.Header.Set(h, v)
		}
	}
	key := svcauth.Key(svcauth.KeyFromEnv())
	key.Assert(pr.Out, gatewayServiceSubject, c.tenant, "")
	_ = key.AssertApp(pr.Out, c.scope)
}

// appUpstream is a backend URL for an app call; it answers 503 when the
// deployment has no such backend.
func (s *Service) appUpstream(w http.ResponseWriter, base, path string) (*url.URL, bool) {
	base = strings.TrimRight(base, "/")
	if base == "" {
		httpx.Unavailable(w, "app data")
		return nil, false
	}
	u, err := url.Parse(base + path)
	if err != nil {
		obs.ServerError(w, "bad app upstream", err)
		httpx.Error(w, "bad app upstream", http.StatusInternalServerError)
		return nil, false
	}
	return u, true
}

// appFeedRecheck is how often an open change feed re-resolves its
// capability: a revoked link, a removed member or a finished job ends the
// feed within it.
const appFeedRecheck = 30 * time.Second

// forwardAppEvents serves an app's change feed (GET …/events, SSE): the
// room's app_changed stream from realtime (/internal/v1/rooms/{room}/
// app-events, which carries nothing else of the room), narrowed to this
// app (appEventsOf) and re-resolved every appFeedRecheck.
func (s *Service) forwardAppEvents(w http.ResponseWriter, r *http.Request, c appCapability) {
	target, ok := s.appUpstream(w, s.Backends.Realtime, "/internal/v1/rooms/"+url.PathEscape(c.scope.RoomID)+"/app-events")
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(s.appFeedRecheckEvery())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if c.still == nil || !c.still(ctx) {
					cancel()
					return
				}
			}
		}
	}()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = ""
			pr.Out.Header = http.Header{"Accept": {"text/event-stream"}}
			svcauth.Key(svcauth.KeyFromEnv()).Assert(pr.Out, gatewayServiceSubject, c.tenant, "")
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("Cache-Control", "no-store")
			if resp.StatusCode == http.StatusOK {
				resp.Body = appEventsOf(resp.Body, c.scope.AppID)
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler:  obs.ProxyError("app change feed proxy failed"),
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// appFeedRecheckEvery is appFeedRecheck (shortened by tests).
func (s *Service) appFeedRecheckEvery() time.Duration {
	if s.appFeedRecheck > 0 {
		return s.appFeedRecheck
	}
	return appFeedRecheck
}

// maxAppEventLine bounds one line of the upstream change stream.
const maxAppEventLine = 16 << 10

// appEventsOf narrows realtime's app change stream to one app: it writes
// only app_changed events of appID, re-encoded as wire.AppChanged, and the
// keep-alives. Whatever else the stream held never reaches the app.
func appEventsOf(upstream io.ReadCloser, appID string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer upstream.Close()
		sc := bufio.NewScanner(upstream)
		sc.Buffer(make([]byte, 0, 4096), maxAppEventLine)
		event := ""
		var err error
		for err == nil && sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				event = ""
			case strings.HasPrefix(line, ":"):
				_, err = io.WriteString(pw, ": keep-alive\n\n")
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:") && event == wire.EventAppChanged:
				var change wire.AppChanged
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &change) != nil || change.AppID != appID {
					continue
				}
				data, _ := json.Marshal(change)
				_, err = fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", wire.EventAppChanged, data)
			}
		}
		if err == nil {
			err = sc.Err()
		}
		pw.CloseWithError(err)
	}()
	return pr
}

// handleAppSDK serves an embedded SDK file, versioned with the gateway
// (revalidated by its digest).
func (s *Service) handleAppSDK(body []byte, contentType string) http.HandlerFunc {
	etag := `"` + digest.Of(body) + `"`
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}
}

// handleAppNotFound answers a path under ~app/ that is not the app API.
func handleAppNotFound(w http.ResponseWriter, _ *http.Request) {
	httpx.Error(w, "not an app API path", http.StatusNotFound)
}

// appVisitorTTL bounds a visitor token. The share page renews it while it
// is valid (same visitor); an expired one makes a new visitor.
const appVisitorTTL = 24 * time.Hour

// maxAppVisitorBody bounds the visitor route's body (a token to renew).
const maxAppVisitorBody = 4 << 10

// handleAppVisitor is POST /v1/public/shares/{token}/visitor: a visitor
// token for a share link of an app (wire.AppVisitorToken), bound to the
// link and a random visitor id, expiring with the link at the latest. A
// still-valid token of the same link in the body is renewed for the same
// visitor. Minting spends the client IP's visitor budget (the route row).
func (s *Service) handleAppVisitor(w http.ResponseWriter, r *http.Request) {
	res, ok := s.publicShare(w, r, false, false)
	if !ok {
		return
	}
	if res.Artifact.Kind != store.ArtifactKindSite {
		writeAppErr(w, errAppNotFound)
		return
	}
	var in wire.AppVisitorRenew
	if !httpx.DecodeOptionalJSON(w, r, maxAppVisitorBody, &in) {
		return
	}
	visitor := ""
	if in.Token != "" {
		if prev, err := s.Signer.VerifyKind(in.Token, auth.KindAppVisitor); err == nil && prev.ID == res.Share.ShareID {
			visitor = prev.Subject
		}
	}
	if visitor == "" {
		visitor = ids.New("vis_")
	}
	expires := s.nowTime().Add(appVisitorTTL)
	if end := res.Share.ExpiresAtMs; end > 0 && end < expires.UnixMilli() {
		expires = time.UnixMilli(end)
	}
	token := s.Signer.Issue(auth.Claims{
		Subject:       visitor,
		Tenant:        res.TenantID,
		Kind:          auth.KindAppVisitor,
		ID:            res.Share.ShareID,
		ExpiresAtUnix: expires.Unix(),
	})
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, wire.AppVisitorToken{Token: token, ExpiresAtMs: expires.UnixMilli()})
}
