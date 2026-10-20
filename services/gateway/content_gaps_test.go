package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room share stands: the reply names the room, the author's share list
// shows it, and only the author revokes it (copies already given stay);
// later joiners are realtime's member-join step ("Gap closure" 2).
func TestContentRoomShareStandsUntilRevoked(t *testing.T) {
	g := newSharingGateway(t)
	src := g.create("maya", expertForm("Be strict."))
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{RoomID: "room-1", Note: "for the rota"})
	var rep wire.ContentShareReply
	_ = json.Unmarshal(raw, &rep)
	if code != http.StatusOK || rep.Room != "room-1" || len(rep.Shared) != 2 {
		t.Fatalf("room share = %d %s", code, raw)
	}
	code, raw = g.do("maya", http.MethodGet, "/v1/content-components/"+src.ID+"/shares", "", nil)
	var shares wire.ContentShares
	_ = json.Unmarshal(raw, &shares)
	if code != http.StatusOK || len(shares.Rooms) != 1 || shares.Rooms[0].RoomID != "room-1" || shares.Rooms[0].Version != "1.0.0" ||
		shares.Rooms[0].Note != "for the rota" || len(shares.Copies) != 2 {
		t.Fatalf("shares = %d %s", code, raw)
	}
	// A later share of a newer version moves the room share forward.
	g.newVersion("maya", src.ID, "1.0.0", "Be stricter.")
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{RoomID: "room-1"}); code != http.StatusOK {
		t.Fatalf("second room share = %d %s", code, raw)
	}
	if got, _ := g.svc.ContentComponents.ListContentRoomShares(context.Background(), store.ContentRoomShareQuery{RoomID: "room-1"}); len(got) != 1 || got[0].Version != "1.0.1" {
		t.Fatalf("room share after a newer share = %+v", got)
	}
	revoke := "/v1/content-components/" + src.ID + "/shares/rooms/room-1"
	if code, _ := g.do("jack", http.MethodDelete, revoke, "", nil); code != http.StatusNotFound {
		t.Fatalf("someone else's revoke = %d", code)
	}
	if code, _ := g.do("maya", http.MethodDelete, revoke, "", nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if code, _ := g.do("maya", http.MethodDelete, revoke, "", nil); code != http.StatusNotFound {
		t.Fatalf("second revoke = %d", code)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "shared") == nil {
		t.Fatal("revoking took jack's copy")
	}
	if a := g.actions(); !strings.HasSuffix(a, "unshare reviewer@1.0.1") {
		t.Fatalf("audit = %s", a)
	}
}

// promotePlatform exports, signs and installs maya's component of form as
// platform content, returning its id.
func (g *sharingGateway) promotePlatform(form map[string]any) string {
	g.t.Helper()
	src := g.create("maya", form)
	_, body := g.do("sam", http.MethodGet, "/v1/platform/content-components/"+src.ID+"/package", "", nil)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	trusted, _ := json.Marshal([]pkgmeta.TrustedKey{{KeyID: "0102030405060708", PublicKey: minisignTestPublicKey("0102030405060708", pub)}})
	_ = g.svc.PlatformSettings.PutPlatformSetting(context.Background(), store.PlatformSetting{Key: componentTrustedKeysKey, Value: string(trusted)})
	code, raw := g.json("sam", http.MethodPost, "/v1/platform/components", wire.ComponentInstallRequest{Package: string(body), Signature: signPackage(priv, body)})
	if code != http.StatusOK {
		g.t.Fatalf("install = %d %s", code, raw)
	}
	var inst contentInstallReply
	_ = json.Unmarshal(raw, &inst)
	return inst.ID
}

// Platform content takes the component tenant scope through the existing
// route and its fail-closed semantics; every member surface follows it
// ("Gap closure" 3).
func TestPlatformContentTenantScope(t *testing.T) {
	g := newSharingGateway(t)
	id := g.promotePlatform(expertForm("Be strict."))
	put := func(user, path string, body any) int {
		code, _ := g.json(user, http.MethodPut, path, body)
		return code
	}
	tenants := "/v1/platform/components/reviewer/tenants"
	if code := put("ada", tenants+"?kind=expert", map[string]any{"enabledTenants": []string{"other"}}); code != http.StatusForbidden {
		t.Fatalf("org admin = %d", code)
	}
	if code := put("sam", tenants+"?kind=expert", map[string]any{"enabledTenants": []string{}}); code != http.StatusBadRequest {
		t.Fatalf("empty list = %d", code)
	}
	if code := put("sam", tenants+"?kind=expert", map[string]any{"enabledTenants": []string{"other"}, "allTenants": true}); code != http.StatusBadRequest {
		t.Fatalf("both = %d", code)
	}
	if code := put("sam", "/v1/platform/components/nothing/tenants?kind=expert", map[string]any{"allTenants": true}); code != http.StatusNotFound {
		t.Fatalf("unknown = %d", code)
	}
	if code := put("sam", tenants+"?kind=expert", map[string]any{"enabledTenants": []string{" other ", "other"}}); code != http.StatusNoContent {
		t.Fatalf("limit = %d", code)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "platform") != nil {
		t.Fatal("home still sees platform content limited to other")
	}
	if findContent(g.library("kim", "expert"), "reviewer", "platform") == nil {
		t.Fatal("other lost platform content limited to it")
	}
	code, raw := g.do("sam", http.MethodGet, "/v1/platform/content-components?scope=platform", "", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"enabledTenants":["other"]`) {
		t.Fatalf("platform listing = %d %s", code, raw)
	}
	// Without a kind the name is unambiguous here.
	if code := put("sam", tenants, map[string]any{"allTenants": true}); code != http.StatusNoContent {
		t.Fatalf("clear = %d", code)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "platform") == nil {
		t.Fatal("cleared scope: home does not see it")
	}
	if c, _ := g.svc.ContentComponents.GetContentComponent(context.Background(), id); c.EnabledTenants != nil {
		t.Fatalf("stored tenants = %v", c.EnabledTenants)
	}
	// A name held by an executable record and platform content needs ?kind=.
	form := expertForm("Notify.")
	form["form"].(map[string]any)["name"] = "n1" // the test notifier's name
	g.promotePlatform(form)
	if code := put("sam", "/v1/platform/components/n1/tenants", map[string]any{"allTenants": true}); code != http.StatusConflict {
		t.Fatalf("ambiguous = %d", code)
	}
	if code := put("sam", "/v1/platform/components/n1/tenants?kind=expert", map[string]any{"enabledTenants": []string{"home"}}); code != http.StatusNoContent {
		t.Fatalf("content n1 = %d", code)
	}
	if a := g.actions(); !strings.Contains(a, "set-tenants reviewer@1.0.0") || !strings.Contains(a, "set-tenants n1@1.0.0") {
		t.Fatalf("audit = %s", a)
	}
}

// An organization has one component of a kind and name: asking to publish
// a name another author's org component holds, or approving it, is refused
// with what to do ("Gap closure" 4).
func TestOrgContentNamesAreUnique(t *testing.T) {
	g := newSharingGateway(t)
	maya := g.create("maya", expertForm("Be strict."))
	bob := g.create("bob", expertForm("Be kind."))
	publish := func(user, id string) (int, wire.ContentPublishReply, string) {
		code, raw := g.json(user, http.MethodPost, "/v1/content-components/"+id+"/publish", wire.ContentPublishInput{Scope: "org"})
		var rep wire.ContentPublishReply
		_ = json.Unmarshal(raw, &rep)
		return code, rep, string(raw)
	}
	_, mayaReq, _ := publish("maya", maya.ID)
	code, bobReq, raw := publish("bob", bob.ID) // both ask before either is approved
	if code != http.StatusCreated {
		t.Fatalf("bob's request = %d %s", code, raw)
	}
	if code, raw := g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+mayaReq.Request.ID+"/approve", nil); code != http.StatusOK {
		t.Fatalf("approve maya = %d %s", code, raw)
	}
	code, raw2 := g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+bobReq.Request.ID+"/approve", nil)
	if code != http.StatusConflict || !strings.Contains(string(raw2), "already has a expert named reviewer (by maya)") && !strings.Contains(string(raw2), "already has an expert named reviewer (by maya)") ||
		!strings.Contains(string(raw2), "ask bob to rename theirs") {
		t.Fatalf("approve a clashing name = %d %s", code, raw2)
	}
	if r, _ := g.svc.ContentComponents.GetContentRequest(context.Background(), bobReq.Request.ID); r.Status != store.ContentRequestPending {
		t.Fatalf("refused approval left the request %s", r.Status)
	}
	jack := g.create("jack", expertForm("Be quick."))
	code, _, raw = publish("jack", jack.ID)
	if code != http.StatusConflict || !strings.Contains(raw, "rename yours: save it under a new name") {
		t.Fatalf("request a taken name = %d %s", code, raw)
	}
	// maya's own next version is not a clash with her own publication.
	g.newVersion("maya", maya.ID, "1.0.0", "Be stricter.")
	if code, _, raw := publish("maya", maya.ID); code != http.StatusCreated && code != http.StatusConflict {
		t.Fatalf("maya's next request = %d %s", code, raw)
	}
}

// A new version of a component the organization uses opens its publish
// request by itself, one pending per component, admins notified once;
// members stay on the approved version ("Gap closure" 5).
func TestNewVersionResubmitsToTheOrganization(t *testing.T) {
	g := newSharingGateway(t)
	src := g.create("maya", expertForm("Be strict."))
	// A component the organization does not use: no request.
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", map[string]any{"form": expertForm("v2")["form"], "baseVersion": "1.0.0"})
	if code != http.StatusCreated || decodeDetail(t, raw).Resubmitted != nil {
		t.Fatalf("unpublished new version = %d %s", code, raw)
	}
	_, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"})
	var pub wire.ContentPublishReply
	_ = json.Unmarshal(raw, &pub)
	if code, raw := g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+pub.Request.ID+"/approve", nil); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, raw)
	}
	save := func(base, text string) *wire.ContentPublishRequest {
		t.Helper()
		code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", map[string]any{"form": expertForm(text)["form"], "baseVersion": base})
		if code != http.StatusCreated {
			t.Fatalf("new version = %d %s", code, raw)
		}
		return decodeDetail(t, raw).Resubmitted
	}
	first := save("1.0.1", "v3")
	if first == nil || first.Version != "1.0.2" || first.ApprovedVersion != "1.0.1" || first.Status != "pending" || first.RequestedBy != "maya" {
		t.Fatalf("resubmitted = %+v", first)
	}
	if n := g.queued(wire.NotificationContentReview, "content-request:"+first.ID); n.Recipients[0].UserID != "ada" {
		t.Fatalf("admin notification = %+v", n)
	}
	second := save("1.0.2", "v4")
	if second == nil || second.Version != "1.0.3" || second.ID == first.ID || !strings.Contains(second.Note, "replaces the request for 1.0.2") {
		t.Fatalf("second resubmission = %+v", second)
	}
	if r, _ := g.svc.ContentComponents.GetContentRequest(context.Background(), first.ID); r.Status != store.ContentRequestWithdrawn {
		t.Fatalf("superseded request = %s", r.Status)
	}
	pending, _ := g.svc.ContentComponents.ListContentRequests(context.Background(), store.ContentRequestQuery{ComponentID: src.ID, Status: store.ContentRequestPending})
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending = %+v", pending)
	}
	time.Sleep(150 * time.Millisecond)
	if _, ok := g.outbox.Item("home", notify.EventID("home", wire.NotificationContentReview, "content-request:"+second.ID), "n1"); ok {
		t.Fatal("a superseding resubmission notified the admins again")
	}
	if v := findContent(g.library("jack", "expert"), "reviewer", "org"); v == nil || v.LatestVersion != "1.0.1" {
		t.Fatalf("members moved before approval: %+v", v)
	}
	// Taken out of the organization: no more requests.
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "personal"}); code != http.StatusOK {
		t.Fatalf("withdraw = %d", code)
	}
	if rs := save("1.0.3", "v5"); rs != nil {
		t.Fatalf("resubmitted after withdrawal: %+v", rs)
	}
}

// recordingRealtime fronts the test realtime and records the internal
// publishes the gateway makes.
type recordingRealtime struct {
	mu     sync.Mutex
	events []wire.Event
}

func (rr *recordingRealtime) wrap(t *testing.T, target string) string {
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/publish") && strings.HasPrefix(r.URL.Path, "/internal/v1/rooms/") {
			raw, _ := io.ReadAll(r.Body)
			var ev wire.Event
			_ = json.Unmarshal(raw, &ev)
			rr.mu.Lock()
			rr.events = append(rr.events, ev)
			rr.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (rr *recordingRealtime) kinds(kind string) []wire.Event {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	var out []wire.Event
	for _, ev := range rr.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// A save by the room tools tells the job's room (content_saved), so the
// workshop needs no re-list after every reply (a job never publishes); a
// member's own save in the Library publishes nothing ("Gap closure" 9).
func TestGrantedSaveAndPublishEmitContentSaved(t *testing.T) {
	g := newSharingGateway(t)
	rr := &recordingRealtime{}
	g.svc.Backends.Realtime = rr.wrap(t, g.svc.Backends.Realtime)
	ctx := tenant.NewContext(context.Background(), "home")
	now := time.Now().UnixMilli()
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	g.create("maya", expertForm("Own save.")) // the Library form: no event
	if got := rr.kinds(wire.EventContentSaved); len(got) != 0 {
		t.Fatalf("a member's own save published %+v", got)
	}
	code, raw := g.do("jack", http.MethodPost, "/v1/content-components?job=job-1&room=room-1", "application/x-tar", packageTar(t, draftExpert("drafted", nil)))
	if code != http.StatusCreated {
		t.Fatalf("granted save = %d %s", code, raw)
	}
	saved := decodeDetail(t, raw).Component
	// A job never publishes to the organization (the member's own click).
	if code, raw = g.json("jack", http.MethodPost, "/v1/content-components/"+saved.ID+"/publish?job=job-1&room=room-1", wire.ContentPublishInput{Scope: "org"}); code != http.StatusForbidden {
		t.Fatalf("granted publish = %d %s", code, raw)
	}
	got := rr.kinds(wire.EventContentSaved)
	if len(got) != 1 {
		t.Fatalf("content_saved events = %+v", got)
	}
	var first wire.ContentSaved
	b, _ := json.Marshal(got[0].Payload)
	_ = json.Unmarshal(b, &first)
	if got[0].RoomID != "room-1" || first.ComponentID != saved.ID || first.Owner != "maya" || first.Action != wire.ContentSavedSaved || !first.Created ||
		first.Kind != "expert" || first.Version != "1.0.0" || first.Name != "drafted" {
		t.Fatalf("saved event = %+v %+v", got[0], first)
	}
}
