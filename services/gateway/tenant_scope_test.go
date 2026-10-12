package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// actingContentGateway extends the content gateway with a platform super
// admin root (session tenant org-a) and a room room-b that lives in tenant
// org-b, where root is the owner and jack (session tenant home) a member.
func actingContentGateway(t *testing.T) *contentGateway {
	t.Helper()
	g := newContentGateway(t)
	g.tokens["root"] = g.svc.Signer.Issue(auth.Claims{Subject: "root", Tenant: "org-a", Role: string(rbac.RoleSuperAdmin), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	roomB := map[string]string{"root": "owner", "jack": "member"}
	inRoom, roomRole := g.svc.InRoom, g.svc.RoomRole
	g.svc.InRoom = func(subject, room string) bool {
		if room == "room-b" {
			return roomB[subject] != ""
		}
		return inRoom(subject, room)
	}
	g.svc.RoomRole = func(subject, room string) (string, bool) {
		if room == "room-b" {
			return roomB[subject], roomB[subject] != ""
		}
		return roomRole(subject, room)
	}
	g.svc.RoomTenant = func(room string) string {
		if room == "room-b" {
			return "org-b"
		}
		return "home"
	}
	return g
}

// doWith is do with extra request headers.
func (g *contentGateway) doWith(user, method, path string, hdr map[string]string, contentType string, body []byte) (int, []byte) {
	g.t.Helper()
	req, _ := http.NewRequest(method, g.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.tokens[user])
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// libraryIDs lists the ids in a caller's Library (GET /v1/content-components).
func (g *contentGateway) libraryIDs(user, query string, hdr map[string]string) map[string]bool {
	g.t.Helper()
	code, raw := g.doWith(user, http.MethodGet, "/v1/content-components"+query, hdr, "", nil)
	if code != http.StatusOK {
		g.t.Fatalf("list as %s%s = %d %s", user, query, code, raw)
	}
	var list struct{ Components []wire.ContentComponent }
	if err := json.Unmarshal(raw, &list); err != nil {
		g.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range list.Components {
		out[c.ID] = true
	}
	return out
}

func (g *contentGateway) storedTenant(id string) string {
	g.t.Helper()
	c, err := g.svc.ContentComponents.GetContentComponent(context.Background(), id)
	if err != nil {
		g.t.Fatal(err)
	}
	return c.TenantID
}

var actingB = map[string]string{actingTenantHeader: "org-b"}

// A super admin acting as tenant B (the header, or ?tenantId= which wins)
// creates, lists and saves new versions of their content in B; their
// session tenant's Library does not hold it.
func TestActingTenantScopesMemberContent(t *testing.T) {
	g := actingContentGateway(t)
	form, _ := json.Marshal(expertForm("acting"))
	code, raw := g.doWith("root", http.MethodPost, "/v1/content-components", actingB, "application/json", form)
	if code != http.StatusCreated {
		t.Fatalf("create acting as org-b = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	if got := g.storedTenant(c.ID); got != "org-b" {
		t.Fatalf("stored tenant = %q, want org-b (the acting tenant)", got)
	}
	if !c.Mine {
		t.Fatalf("created component not Mine in the acting tenant: %+v", c)
	}
	if !g.libraryIDs("root", "", actingB)[c.ID] {
		t.Fatal("library acting as org-b misses the component")
	}
	if !g.libraryIDs("root", "?tenantId=org-b", nil)[c.ID] {
		t.Fatal("library with ?tenantId=org-b misses the component")
	}
	if !g.libraryIDs("root", "?tenantId=org-b", map[string]string{actingTenantHeader: "org-x"})[c.ID] {
		t.Fatal("?tenantId= must win over the header")
	}
	if g.libraryIDs("root", "", nil)[c.ID] {
		t.Fatal("the session tenant's library (org-a) lists org-b content")
	}
	ver, _ := json.Marshal(expertForm("acting v2"))
	if code, raw := g.doWith("root", http.MethodPost, "/v1/content-components/"+c.ID+"/versions", actingB, "application/json", ver); code != http.StatusCreated {
		t.Fatalf("new version acting as org-b = %d %s", code, raw)
	}
	if code, _ := g.doWith("root", http.MethodPost, "/v1/content-components/"+c.ID+"/versions", nil, "application/json", ver); code != http.StatusNotFound {
		t.Fatalf("new version in the session tenant = %d, want 404 (not held there)", code)
	}
}

// A job-grant save in a room of tenant B lands in the requester's Library in
// B: visible to them when they act as B, not in their session tenant.
func TestJobGrantSaveVisibleInActingTenant(t *testing.T) {
	g := actingContentGateway(t)
	ctx := tenant.NewContext(context.Background(), "org-b")
	now := time.Now().UnixMilli()
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-b", RoomID: "room-b", MessageSeq: 1, RequesterUserID: "root", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	code, raw := g.do("jack", http.MethodPost, "/v1/content-components?job=job-b&room=room-b", "application/x-tar", packageTar(t, draftExpert("builder-made", nil)))
	if code != http.StatusCreated {
		t.Fatalf("job-grant save = %d %s", code, raw)
	}
	saved := decodeDetail(t, raw).Component
	if saved.Owner != "root" || g.storedTenant(saved.ID) != "org-b" {
		t.Fatalf("saved = owner %q tenant %q, want root in org-b", saved.Owner, g.storedTenant(saved.ID))
	}
	if !g.libraryIDs("root", "", actingB)[saved.ID] {
		t.Fatal("the requester's library acting as org-b misses the job-grant save")
	}
	if g.libraryIDs("root", "", nil)[saved.ID] {
		t.Fatal("the requester's session-tenant library lists the org-b save")
	}
	// Opened from the room (?room=), it reads in the room's tenant: Mine.
	code, raw = g.do("root", http.MethodGet, "/v1/content-components/"+saved.ID+"?room=room-b", "", nil)
	if code != http.StatusOK || !decodeDetail(t, raw).Component.Mine {
		t.Fatalf("read from the room = %d %s", code, raw)
	}
}

// A non-super-admin is bound to their session tenant: the header and
// ?tenantId= are ignored on gateway-served routes.
func TestActingTenantIgnoredForMembers(t *testing.T) {
	g := actingContentGateway(t)
	form, _ := json.Marshal(expertForm("member"))
	code, raw := g.doWith("maya", http.MethodPost, "/v1/content-components?tenantId=org-b", actingB, "application/json", form)
	if code != http.StatusCreated {
		t.Fatalf("member create = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	if got := g.storedTenant(c.ID); got != "home" {
		t.Fatalf("member's component stored in %q, want home (header ignored)", got)
	}
	if !g.libraryIDs("maya", "?tenantId=org-b", actingB)[c.ID] {
		t.Fatal("member's library must stay their session tenant's whatever the header")
	}
}

// Room-scoped content routes act in the room's tenant, for members and super
// admins alike, and refuse a room out of the caller's reach.
func TestContentRoomScopeActsInRoomTenant(t *testing.T) {
	g := actingContentGateway(t)
	form, _ := json.Marshal(expertForm("room"))
	code, raw := g.doWith("root", http.MethodPost, "/v1/content-components", actingB, "application/json", form)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	// root pins it in room-b without any acting tenant: the use is written in
	// the room's tenant and root (session org-a) may use it there.
	pin, _ := json.Marshal(wire.ContentUseRequest{Scope: "room", RoomID: "room-b", ComponentID: c.ID})
	if code, raw := g.doWith("root", http.MethodPut, "/v1/content-uses", nil, "application/json", pin); code != http.StatusNoContent {
		t.Fatalf("pin in room-b = %d %s", code, raw)
	}
	uses, err := g.svc.ContentComponents.ListContentUses(context.Background(), store.ContentUseRoom, "room-b")
	if err != nil || len(uses) != 1 || uses[0].TenantID != "org-b" {
		t.Fatalf("room-b uses = %+v %v, want one in org-b", uses, err)
	}
	// jack (session home) lists room-b's uses: acting in org-b.
	code, raw = g.do("jack", http.MethodGet, "/v1/content-uses?room=room-b", "", nil)
	var reply wire.ContentUsesReply
	_ = json.Unmarshal(raw, &reply)
	if code != http.StatusOK || len(reply.Room) != 1 || reply.Room[0].ComponentID != c.ID {
		t.Fatalf("jack's room-b uses = %d %s", code, raw)
	}
	// maya is not in room-b: refused, whatever the route.
	for _, path := range []string{"/v1/content-uses?room=room-b", "/v1/content-components/" + c.ID + "?room=room-b"} {
		if code, _ := g.do("maya", http.MethodGet, path, "", nil); code != http.StatusForbidden {
			t.Fatalf("maya %s = %d, want 403", path, code)
		}
	}
}

// The proxy: a super admin's acting tenant (header, ?tenantId= wins) scopes a
// tenant-level path, a room-scoped path acts in the room's tenant, and the
// header never reaches the backend.
func TestProxyEffectiveTenant(t *testing.T) {
	var gotTenant, gotHeader string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotHeader = r.Header.Get("X-Bnbloc-Tenant"), r.Header.Get(actingTenantHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	svc := newGateway(t, backend.URL)
	svc.Backends.ShareAgent = backend.URL
	svc.RoomTenant = func(room string) string {
		if room == "room-1" {
			return "org-b"
		}
		return ""
	}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token := svc.Signer.Issue(auth.Claims{Subject: "root", Tenant: "org-a", Role: string(rbac.RoleSuperAdmin), Rooms: []string{"room-1"}, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	call := func(path, header string) string {
		t.Helper()
		gotTenant, gotHeader = "", ""
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if header != "" {
			req.Header.Set(actingTenantHeader, header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if gotHeader != "" {
			t.Fatalf("%s: the acting-tenant header reached the backend", path)
		}
		return gotTenant
	}
	if got := call("/v1/tenant/usage", "org-x"); got != "org-x" {
		t.Fatalf("tenant path acting org-x = %q", got)
	}
	if got := call("/v1/tenant/usage?tenantId=org-y", "org-x"); got != "org-y" {
		t.Fatalf("tenant path ?tenantId=org-y = %q, want it to win", got)
	}
	if got := call("/v1/rooms/room-1/members", "org-x"); got != "org-b" {
		t.Fatalf("room path = %q, want the room's tenant org-b", got)
	}
}
