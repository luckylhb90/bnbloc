package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// serviceCall is one call the fake service received, its signature checked.
type serviceCall struct {
	method, path, identity string
	signed                 bool
	body                   []byte
}

type serviceFixture struct {
	t      *testing.T
	svc    *Service
	srv    *httptest.Server
	jobs   *store.MemoryBridgeJobs
	secret string
	tokens map[string]string
	audit  *recordingAudit
	seq    int64

	mu    sync.Mutex
	calls []serviceCall
}

func (fx *serviceFixture) last(t *testing.T) serviceCall {
	t.Helper()
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if len(fx.calls) == 0 {
		t.Fatal("the service received nothing")
	}
	return fx.calls[len(fx.calls)-1]
}

func servicePackageBody(t *testing.T, name, baseURL string, caps []string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindServicePackageV1, "name": name, "version": "0.1.0", "publisher": "Acme Corp",
		"runtime": pkgmeta.RuntimeEndpoint, "contract": pkgmeta.ServiceContractV1, "capabilities": caps,
		"endpoint":    map[string]any{"baseUrl": baseURL},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// newServiceFixture installs service "conn" (every primitive) beside a
// gateway whose members are maya and jack (room-1, org "home"), and olga,
// the org admin.
func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	fx := &serviceFixture{t: t, tokens: map[string]string{}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/svc")
		if path == component.ServiceDiscoverPath {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "conn", "contract": component.ServiceContractV1})
			return
		}
		body, _ := io.ReadAll(r.Body)
		pq := path
		if r.URL.RawQuery != "" {
			pq += "?" + r.URL.RawQuery
		}
		identity := r.Header.Get(component.TurnHeader) + r.Header.Get(component.MemberHeader) + r.Header.Get(component.CallbackHeader) + r.Header.Get(component.SendHeader)
		signed := component.Verify(r.Header.Get(component.SignatureHeader), fx.secret, component.SignedRequest(r.Method, pq, identity, body), time.Now(), time.Minute) == nil
		fx.mu.Lock()
		fx.calls = append(fx.calls, serviceCall{method: r.Method, path: pq, identity: identity, signed: signed, body: body})
		fx.mu.Unlock()
		switch {
		case path == component.ServiceEgressHostsPath:
			w.Header().Set(component.AuditHeader, component.EncodeHeader(component.Audit{Action: "hosts", Target: "wiki.example.com", Outcome: "allow"}))
			_ = json.NewEncoder(w).Encode(component.EgressHosts{Hosts: []string{"wiki.example.com"}})
		case path == component.ServicePagesPath:
			_ = json.NewEncoder(w).Encode(component.PageList{Version: "v9", Pages: []component.Page{
				{Name: "connections", Label: "Connections", Audience: component.AudienceMember},
				{Name: "admin", Label: "Connectors", Audience: component.AudienceAdmin},
				{Name: "Bad Name", Label: "x", Audience: component.AudienceMember},
			}})
		case strings.HasPrefix(path, "/pages/connections/"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Set-Cookie", "s=1")
			_, _ = w.Write([]byte("<!doctype html><title>connections</title>"))
		case strings.HasPrefix(path, component.ServiceCardsPrefix):
			reply := component.CardReply{Text: "Allowed.", Done: true, Open: "http://plain.example.com/"}
			if r.URL.Query().Get("decision") == "continue" {
				// A service asks the platform to continue the agent of a job (and tries to
				// claim it already did).
				reply.Continue, reply.ContinueJob, reply.ContinueKey, reply.Continued = "The change is allowed: run it again.", "job_held", "h1", true
			}
			_ = json.NewEncoder(w).Encode(reply)
		case path == component.ServiceSendEgressPath:
			_ = json.NewEncoder(w).Encode(component.EgressResponse{Status: http.StatusNoContent})
		case strings.HasPrefix(path, component.ServiceCallbackPrefix):
			w.Header().Set("Location", "https://idp.example.com/done")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(backend.Close)
	svc, srv, token, key := signedPackageGatewayConfigured(t, true, false, true)
	fx.audit = &recordingAudit{}
	svc.ExtAudit = fx.audit
	svc.ComponentValues = store.NewMemoryComponentValues()
	body := servicePackageBody(t, "conn", backend.URL+"/svc", component.ServiceCapabilities)
	raw, code := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if code != http.StatusOK || installed.Kind != "service" || installed.Secret == "" {
		t.Fatalf("install = %d %s", code, raw)
	}
	fx.secret = installed.Secret
	svc.limiter.Get(1000, 1000, nil)
	svc.Verifier = subjectVerifier{}
	svc.TenantResolver = func(id auth.Identity, _ string) (string, string, error) {
		if id.Subject == "olga" {
			return "home", string(rbac.RoleAdmin), nil
		}
		return "home", string(rbac.RoleMember), nil
	}
	members := map[string]string{"maya": "owner", "jack": "member"}
	svc.Rooms = func(string, string) []string { return nil }
	svc.Membership = roleMembership(func(subject, room string) (string, bool) {
		if room != "room-1" {
			return "", false
		}
		return members[subject], members[subject] != ""
	})
	svc.RoomTenant = func(string) string { return "home" }
	fx.jobs = store.NewMemoryBridgeJobs()
	svc.ContentJobs = fx.jobs
	svc.BridgeSends = fx.jobs
	fx.svc, fx.srv = svc, srv
	for _, u := range []string{"maya", "jack", "olga"} {
		fx.tokens[u], _ = login(t, srv, u)
	}
	return fx
}

func (fx *serviceFixture) do(user, method, path string, body []byte, header http.Header) (*http.Response, []byte) {
	fx.t.Helper()
	req, _ := http.NewRequest(method, fx.srv.URL+path, bytes.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+fx.tokens[user])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// putJob leases a running job to bridge owner on behalf of requester.
func (fx *serviceFixture) putJob(id, requester, owner, target string) {
	fx.t.Helper()
	ctx := tenant.NewContext(context.Background(), "home")
	if err := fx.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-" + owner, OwnerUserID: owner, Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		fx.t.Fatal(err)
	}
	fx.seq++
	if _, err := fx.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-1", ChannelID: "c1", MessageSeq: fx.seq, RequesterUserID: requester, OwnerUserID: owner,
		Provider: "codex", TargetID: target, Status: store.AgentJobRunning, LeaseBridgeID: "br-" + owner, CreatedAtMs: 1}); err != nil {
		fx.t.Fatal(err)
	}
}

// A bridge's call for a job it drives reaches the service's turn prefix,
// signed, with the turn attested (drive own for the requester's own agent,
// borrowed otherwise); the room and job leave the query; the service's
// audit row is recorded for the requester. Another member's bridge, an
// unknown job and a service the organization does not have are refused.
func TestTurnProxyAttestsTheTurn(t *testing.T) {
	fx := newServiceFixture(t)
	resp, raw := fx.do("maya", http.MethodGet, "/v1/my/turn-services", nil, nil)
	var list wire.TurnServiceList
	_ = json.Unmarshal(raw, &list)
	if resp.StatusCode != http.StatusOK || len(list.Services) != 1 || list.Services[0].Name != "conn" || len(list.Services[0].Capabilities) != len(component.ServiceCapabilities) {
		t.Fatalf("turn services = %d %s", resp.StatusCode, raw)
	}
	fx.putJob("job-own", "maya", "maya", "")
	fx.putJob("job-borrowed", "jack", "maya", "")
	for job, drive := range map[string]string{"job-own": component.DriveOwn, "job-borrowed": component.DriveBorrowed} {
		resp, raw := fx.do("maya", http.MethodGet, "/v1/turn-proxy/conn/egress/hosts?room=room-1&job="+job+"&x=1", nil, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "wiki.example.com") {
			t.Fatalf("%s: turn proxy = %d %s", job, resp.StatusCode, raw)
		}
		call := fx.last(t)
		var turn component.Turn
		if err := component.DecodeHeader(call.identity, &turn); err != nil {
			t.Fatal(err)
		}
		if !call.signed || call.path != component.ServiceEgressHostsPath+"?x=1" || turn.Job != job || turn.Room != "room-1" || turn.Org != "home" ||
			turn.Drive != drive || turn.AgentOwner != "maya" || turn.Channel != "c1" || turn.Origin == "" {
			t.Fatalf("%s: call = %+v turn = %+v", job, call, turn)
		}
	}
	found := false
	for _, a := range fx.audit.recs {
		if a.Kind == "service" && a.Name == "conn" && a.Action == "hosts" && a.Actor == "jack" && a.Outcome == "allow" && a.Detail == "wiki.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit = %+v (the service's row, for the requester)", fx.audit.recs)
	}
	if resp, _ := fx.do("jack", http.MethodGet, "/v1/turn-proxy/conn/egress/hosts?room=room-1&job=job-own", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another member's bridge = %d", resp.StatusCode)
	}
	if resp, _ := fx.do("maya", http.MethodGet, "/v1/turn-proxy/conn/egress/hosts?room=room-1&job=nope", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job = %d", resp.StatusCode)
	}
	if resp, _ := fx.do("maya", http.MethodGet, "/v1/turn-proxy/other/egress/hosts?room=room-1&job=job-own", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown service = %d", resp.StatusCode)
	}
	if resp, _ := fx.do("maya", http.MethodGet, "/v1/turn-proxy/conn/egress/hosts", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no job = %d", resp.StatusCode)
	}
}

// A member lists a service's pages (an admin page only for an org admin, a
// malformed one never) and opens one: its address carries a ticket, the
// page is served sandboxed from the service's /pages/<page>/ with the
// member attested and no cookie; a forged ticket is refused.
func TestComponentPagesByTicket(t *testing.T) {
	fx := newServiceFixture(t)
	for user, want := range map[string]int{"maya": 1, "olga": 2} {
		_, raw := fx.do(user, http.MethodGet, "/v1/my/component-pages", nil, nil)
		var list wire.ComponentPageList
		_ = json.Unmarshal(raw, &list)
		if len(list.Pages) != want || list.Pages[0].Version != "0.1.0" || list.Pages[0].ServiceVersion != "v9" {
			t.Fatalf("%s pages = %s", user, raw)
		}
	}
	resp, raw := fx.do("maya", http.MethodPost, "/v1/my/component-pages/conn/connections", []byte(`{"query":"system=wiki"}`), http.Header{"Content-Type": {"application/json"}})
	var sess wire.ComponentPageSession
	_ = json.Unmarshal(raw, &sess)
	if resp.StatusCode != http.StatusOK || !strings.Contains(sess.URL, component.ComponentPagePath) || !strings.HasSuffix(sess.URL, "/?system=wiki") {
		t.Fatalf("open = %d %s", resp.StatusCode, raw)
	}
	pagePath := strings.TrimPrefix(sess.URL, fx.srv.URL)
	resp, raw = fx.do("", http.MethodGet, pagePath, nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "connections") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("page = %d %v %s", resp.StatusCode, resp.Header, raw)
	}
	call := fx.last(t)
	var m component.Member
	_ = component.DecodeHeader(call.identity, &m)
	if !call.signed || call.path != "/pages/connections/?system=wiki" || m.Subject != "maya" || m.Org != "home" || m.Page != "connections" || !strings.HasSuffix(m.PageURL, "/") {
		t.Fatalf("page call = %+v member = %+v", call, m)
	}
	if resp, _ := fx.do("", http.MethodGet, component.ComponentPagePath+"forged/", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged ticket = %d", resp.StatusCode)
	}
	if resp, _ := fx.do("maya", http.MethodPost, "/v1/my/component-pages/conn/Bad", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad page name = %d", resp.StatusCode)
	}

	// Opened for a room: the page is told the room and the member's role
	// there; someone outside the room cannot open it for that room.
	resp, raw = fx.do("maya", http.MethodPost, "/v1/my/component-pages/conn/connections", []byte(`{"roomId":"room-1"}`), http.Header{"Content-Type": {"application/json"}})
	_ = json.Unmarshal(raw, &sess)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open for a room = %d %s", resp.StatusCode, raw)
	}
	_, _ = fx.do("", http.MethodGet, strings.TrimPrefix(sess.URL, fx.srv.URL), nil, nil)
	m = component.Member{}
	_ = component.DecodeHeader(fx.last(t).identity, &m)
	if m.Room != "room-1" || m.RoomRole != component.RoomOwner {
		t.Fatalf("the member of a room page = %+v", m)
	}
	if resp, _ := fx.do("olga", http.MethodPost, "/v1/my/component-pages/conn/connections", []byte(`{"roomId":"room-1"}`), http.Header{"Content-Type": {"application/json"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outside the room = %d", resp.StatusCode)
	}
}

// A callback is open and reaches the service's callback prefix signed; its
// redirect passes on.
func TestComponentCallbackForwards(t *testing.T) {
	fx := newServiceFixture(t)
	resp, _ := fx.do("", http.MethodGet, component.ComponentCallbackPath+"conn/oauth?code=c&state=s", nil, nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://idp.example.com/done" {
		t.Fatalf("callback = %d %v", resp.StatusCode, resp.Header)
	}
	if call := fx.last(t); !call.signed || call.path != "/callbacks/oauth?code=c&state=s" {
		t.Fatalf("callback call = %+v", call)
	}
	if resp, _ := fx.do("", http.MethodGet, component.ComponentCallbackPath+"nope/oauth", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown service callback = %d", resp.StatusCode)
	}
}

// The service keeps sealed values by signed calls: written, read back,
// listed and deleted; stored sealed; an unsigned call and an invalid path
// are refused; the values go with the service's uninstall.
func TestComponentStoreSignedAndSealed(t *testing.T) {
	fx := newServiceFixture(t)
	call := func(method, path string, body []byte, secret string) (int, []byte) {
		h := http.Header{component.ComponentHeader: {"service/conn"}}
		if secret != "" {
			h.Set(component.SignatureHeader, component.Sign(secret, time.Now(), component.SignedRequest(method, path, "", body)))
		}
		resp, raw := fx.do("", method, path, body, h)
		return resp.StatusCode, raw
	}
	key := component.StorePath(component.StoreMembers, "maya", "grant:wiki")
	if code, _ := call(http.MethodPut, key, []byte("refresh-SECRET"), fx.secret); code != http.StatusNoContent {
		t.Fatalf("put = %d", code)
	}
	v, ok, _ := fx.svc.ComponentValues.GetComponentValue(context.Background(), "service/conn", "members/maya", "grant:wiki")
	if !ok || strings.Contains(v.Sealed, "refresh-SECRET") {
		t.Fatalf("stored = %+v (sealed)", v)
	}
	if code, raw := call(http.MethodGet, key, nil, fx.secret); code != http.StatusOK || string(raw) != "refresh-SECRET" {
		t.Fatalf("get = %d %s", code, raw)
	}
	if code, raw := call(http.MethodGet, component.ComponentStorePath+"members/maya/", nil, fx.secret); code != http.StatusOK || !strings.Contains(string(raw), "grant:wiki") {
		t.Fatalf("list = %d %s", code, raw)
	}
	// The contract's own client reaches the same values.
	client := &component.ServiceClient{BaseURL: fx.srv.URL, Name: "conn", Secret: fx.secret}
	if v, ok, err := client.StoreGet(context.Background(), component.StoreMembers, "maya", "grant:wiki"); err != nil || !ok || string(v) != "refresh-SECRET" {
		t.Fatalf("client get = %q %v %v", v, ok, err)
	}
	if err := client.StorePut(context.Background(), component.StoreShared, "", "state:x", []byte("s")); err != nil {
		t.Fatal(err)
	}
	if keys, err := client.StoreKeys(context.Background(), component.StoreShared, ""); err != nil || len(keys) != 1 || keys[0] != "state:x" {
		t.Fatalf("client keys = %v %v", keys, err)
	}
	if code, _ := call(http.MethodGet, key, nil, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature = %d", code)
	}
	if code, _ := call(http.MethodGet, component.ComponentStorePath+"nobody/x", nil, fx.secret); code != http.StatusBadRequest {
		t.Fatalf("bad scope = %d", code)
	}
	if code, _ := call(http.MethodDelete, key, nil, fx.secret); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := call(http.MethodGet, key, nil, fx.secret); code != http.StatusNotFound {
		t.Fatalf("after delete = %d", code)
	}
	_, _ = call(http.MethodPut, key, []byte("v"), fx.secret)
	rec, _, _ := fx.svc.findComponentRecord(context.Background(), "service", "conn")
	fx.svc.afterEndpointUninstall(context.Background(), rec)
	if keys, _ := fx.svc.ComponentValues.ListComponentValueKeys(context.Background(), "service/conn", "members/maya"); len(keys) != 0 {
		t.Fatalf("an uninstalled service keeps %v", keys)
	}
}

// A card's call reaches the service's cards prefix as the member, for the
// card's room (their role read there), and answers the service's reply; an
// address to open that is not https is dropped; a room the member is not
// in is refused.
func TestComponentCardCall(t *testing.T) {
	fx := newServiceFixture(t)
	resp, raw := fx.do("maya", http.MethodPost, "/v1/my/component-cards/conn/approve", []byte(`{"query":"id=h1&decision=allow","roomId":"room-1"}`), http.Header{"Content-Type": {"application/json"}})
	var reply component.CardReply
	_ = json.Unmarshal(raw, &reply)
	if resp.StatusCode != http.StatusOK || reply.Text != "Allowed." || !reply.Done || reply.Open != "" {
		t.Fatalf("call = %d %s", resp.StatusCode, raw)
	}
	call := fx.last(t)
	var m component.Member
	_ = component.DecodeHeader(call.identity, &m)
	if !call.signed || call.method != http.MethodPost || call.path != "/cards/approve?id=h1&decision=allow" || m.Subject != "maya" || m.Room != "room-1" || m.RoomRole != component.RoomOwner {
		t.Fatalf("forwarded = %+v %+v", call, m)
	}
	if resp, _ := fx.do("olga", http.MethodPost, "/v1/my/component-cards/conn/approve", []byte(`{"roomId":"room-1"}`), http.Header{"Content-Type": {"application/json"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outside the room = %d", resp.StatusCode)
	}
}

// A card reply that asks to continue is carried out by realtime as the member
// (its job checks are there): the room, the job, the text and the once-key are
// what the service said; the card is told it happened, and the service's own
// claim that it did is never taken. Without a room, or when realtime refuses,
// nothing is claimed.
func TestComponentCardContinuePostsThroughRealtime(t *testing.T) {
	fx := newServiceFixture(t)
	var got struct {
		path, subject string
		in            wire.ContinueRequest
	}
	answer := wire.ContinueReply{Posted: true, Sequence: 7}
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.subject = r.URL.Path, r.Header.Get("X-Bnbloc-User")
		_ = json.NewDecoder(r.Body).Decode(&got.in)
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer rt.Close()
	fx.svc.Backends.Realtime = rt.URL
	header := http.Header{"Content-Type": {"application/json"}}
	call := func(body string) component.CardReply {
		resp, raw := fx.do("maya", http.MethodPost, "/v1/my/component-cards/conn/approve", []byte(body), header)
		var reply component.CardReply
		_ = json.Unmarshal(raw, &reply)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call = %d %s", resp.StatusCode, raw)
		}
		return reply
	}
	reply := call(`{"query":"id=h1&decision=continue","roomId":"room-1"}`)
	if !reply.Continued || reply.Continue != "" || reply.ContinueJob != "" || reply.ContinueKey != "" {
		t.Fatalf("reply = %+v; want continued, and the request fields not passed to the client", reply)
	}
	want := wire.ContinueRequest{Job: "job_held", Component: "conn", Text: "The change is allowed: run it again.", Key: "h1"}
	if got.path != "/internal/v1/rooms/room-1/continue" || got.in != want {
		t.Fatalf("realtime got %s %+v, want the room's continue route with %+v", got.path, got.in, want)
	}
	answer = wire.ContinueReply{Why: "that request is still running"}
	if r := call(`{"query":"id=h1&decision=continue","roomId":"room-1"}`); r.Continued {
		t.Fatalf("realtime refused and the card says continued: %+v", r)
	}
	got.path = ""
	if r := call(`{"query":"id=h1&decision=continue"}`); r.Continued || got.path != "" {
		t.Fatalf("a call without a room reached realtime or claimed it: %+v %q", r, got.path)
	}
	if r := call(`{"query":"id=h1&decision=allow","roomId":"room-1"}`); r.Continued {
		t.Fatalf("a reply that asked for nothing claims continued: %+v", r)
	}
}
