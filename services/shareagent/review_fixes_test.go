package shareagenthttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func runsUsed(t *testing.T, gs *store.MemoryGrant) int {
	t.Helper()
	g, err := gs.GetGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "alice", "codex"))
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}
	return g.RunsUsed
}

// BE-8: the grant's RunQuota is enforced when a run is reserved. It used to be
// stored and displayed but never enforced, so a borrow ran past its quota.
func TestReserveBorrowEnforcesGrantRunQuota(t *testing.T) {
	svc, gs := newService(t, "")
	quota := 1
	future := int64(1_000_000_000_000)
	if err := gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "alice", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "*",
		RunQuota: &quota, MaxConcurrent: 2, ExpiresAtMs: &future,
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := borrowRun(t, svc, tenant.Default, "alice", "codex", "bob", "", nil); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("first borrow = %d %v, want 200 recorded", code, body)
	}
	code, body := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "")
	if code != http.StatusForbidden || body["denyReason"] != "quota_exhausted" {
		t.Fatalf("over-quota borrow = %d %v, want 403 quota_exhausted", code, body)
	}
	if n := runsUsed(t, gs); n != 1 {
		t.Fatalf("runsUsed = %d, want 1 (the denied run must not consume)", n)
	}
}

// BE-15: the room-authorization grant id fits grant_id VARCHAR(64) for any id
// lengths, stays distinct across parts and tenants, and round-trips through the
// HTTP authorize + read path with realistic long ids.
func TestRoomAuthGrantIDFitsColumnAndRoundTrips(t *testing.T) {
	long := func(c string) string { return strings.Repeat(c, 255) }
	id := roomAuthGrantID(dctx(), long("r"), long("u")+"@example.com", "nimbus:"+long("p"))
	if len(id) > 64 {
		t.Fatalf("grant id length = %d, exceeds VARCHAR(64)", len(id))
	}
	if roomAuthGrantID(dctx(), "a", "b", "c") == roomAuthGrantID(dctx(), "a", "bc", "") {
		t.Fatal("grant ids collide across part boundaries")
	}
	if roomAuthGrantID(tctx("acme"), "r", "o", "p") == roomAuthGrantID(tctx("globex"), "r", "o", "p") {
		t.Fatal("grant ids collide across tenants (grant_id is the table's sole primary key)")
	}

	svc, _ := newService(t, "")
	room := "room-" + strings.Repeat("x", 120)
	owner := "owner-" + strings.Repeat("y", 100) + "@example.com"
	members := svc.Membership.(interface{ Add(string, string) })
	members.Add(room, owner)
	members.Add(room, "reader")
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	target := "shared-agent:nimbus:" + strings.Repeat("z", 60)
	base := srv.URL + "/v1/rooms/" + room + "/shared-agents/" + target + "/authorization"
	put, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(`{}`))
	put.Header.Set("X-Bnbloc-User", owner)
	resp := mustDo(t, put)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize with long ids = %d, want 200", resp.StatusCode)
	}
	if !readAuthorized(t, base+"?owner="+owner) {
		t.Fatal("long-id authorization must read back as authorized")
	}
}

// BE-21: an authorization that names no models stores an empty range; the
// service never invents a hardcoded default model name.
func TestAuthorizeWithoutModelsHasNoHardcodedDefault(t *testing.T) {
	svc, gs := newService(t, "")
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	put, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization", strings.NewReader(`{}`))
	put.Header.Set("X-Bnbloc-User", "alice")
	mustDo(t, put).Body.Close()
	g, err := gs.GetGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "alice", "codex"))
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}
	if len(g.AllowedModels) != 0 {
		t.Fatalf("allowed models = %+v, want empty (no invented default)", g.AllowedModels)
	}
}

// The owner usage view lists a freshly authorized provider from the stored
// grants, without depending on a client-supplied provider hint or on parsing
// the (now opaque) grant id.
func TestOwnerUsageListsAuthorizationsWithoutHints(t *testing.T) {
	svc, gs := newService(t, "")
	seedAuthorizedBorrow(t, gs, tenant.Default)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/shared-agents-usage", nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	var out struct {
		Authorizations []authorizationView `json:"authorizations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Authorizations) != 1 || out.Authorizations[0].Provider != "codex" {
		t.Fatalf("authorizations = %+v, want one codex entry", out.Authorizations)
	}
	// Another member sees none of alice's authorizations as their own.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/shared-agents-usage", nil)
	req2.Header.Set("X-Bnbloc-User", "bob")
	resp2 := mustDo(t, req2)
	defer resp2.Body.Close()
	out.Authorizations = nil
	_ = json.NewDecoder(resp2.Body).Decode(&out)
	if len(out.Authorizations) != 0 {
		t.Fatalf("bob's view = %+v, want none", out.Authorizations)
	}
}

// tenantReq builds a tenant-scoped admin request against the Handler.
func tenantReq(method, path, caller, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Bnbloc-User", caller)
	return r
}

// BE-23: add-member upserts, so re-adding the only org_admin with a lower role
// is a demotion and must be refused like the role-change path.
func TestAddMemberCannotDemoteLastAdmin(t *testing.T) {
	svc, _ := newService(t, "")
	tenants := store.NewMemoryTenant()
	_ = tenants.PutMember(dctx(), store.TenantMember{TenantID: tenant.Default, UserID: "root", Role: store.OrgRoleAdmin, JoinedAtMs: 1})
	svc.Tenants = tenants
	h := svc.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, tenantReq(http.MethodPost, "/v1/tenant/members", "other", `{"userId":"root","role":"member"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("demote last admin via add-member = %d, want 409", w.Code)
	}
	if m, _, _ := tenants.GetMember(dctx(), tenant.Default, "root"); m.Role != store.OrgRoleAdmin {
		t.Fatalf("root role = %q, want org_admin preserved", m.Role)
	}
	// With a second admin present the same demotion is allowed.
	_ = tenants.PutMember(dctx(), store.TenantMember{TenantID: tenant.Default, UserID: "admin2", Role: store.OrgRoleAdmin, JoinedAtMs: 2})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, tenantReq(http.MethodPost, "/v1/tenant/members", "other", `{"userId":"root","role":"member"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("demote with another admin = %d, want 200", w.Code)
	}
}

// BE-23: setting one tenant's quota is persisted per tenant and never touches
// shared process state. The old handler wrote a Service-wide field (a data race
// under concurrent admins, and a cross-tenant leak); run with -race.
func TestSetQuotaIsPerTenantAndRaceFree(t *testing.T) {
	svc, _ := newService(t, "")
	tenants := store.NewMemoryTenant()
	for _, id := range []string{"acme", "globex"} {
		_ = tenants.PutTenant(tctx(id), store.Tenant{TenantID: id, Name: id, Status: store.TenantActive})
	}
	svc.Tenants = tenants
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, id := range []string{"acme", "globex"} {
			wg.Add(1)
			go func(id string, n int) {
				defer wg.Done()
				body, _ := json.Marshal(quotaBody{RunQuota: n})
				r := httptest.NewRequest(http.MethodPut, "/v1/tenant/quota", bytes.NewReader(body)).WithContext(tctx(id))
				r.Header.Set("X-Bnbloc-User", "admin")
				svc.handleTenantSetQuota(httptest.NewRecorder(), r)
				_, _, _ = svc.tenantCeilings(tctx(id))
			}(id, i+1)
		}
	}
	wg.Wait()
	body, _ := json.Marshal(quotaBody{RunQuota: 7})
	r := httptest.NewRequest(http.MethodPut, "/v1/tenant/quota", bytes.NewReader(body)).WithContext(tctx("acme"))
	svc.handleTenantSetQuota(httptest.NewRecorder(), r)
	body, _ = json.Marshal(quotaBody{RunQuota: 0})
	r = httptest.NewRequest(http.MethodPut, "/v1/tenant/quota", bytes.NewReader(body)).WithContext(tctx("globex"))
	svc.handleTenantSetQuota(httptest.NewRecorder(), r)
	if q, _, _ := svc.tenantCeilings(tctx("acme")); q != 7 {
		t.Fatalf("acme run quota = %d, want 7", q)
	}
	if q, _, _ := svc.tenantCeilings(tctx("globex")); q != 0 {
		t.Fatalf("globex run quota = %d, want 0 (acme's quota leaked)", q)
	}
}

// BE-17: shareagent JSON bodies are size-capped (413), not streamed unbounded.
func TestShareagentRejectsOversizedBody(t *testing.T) {
	svc, _ := newService(t, "")
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	huge := `{"allowedModels":["` + strings.Repeat("m", httpx.DefaultBodyLimit) + `"]}`
	put, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization", strings.NewReader(huge))
	put.Header.Set("X-Bnbloc-User", "alice")
	resp := mustDo(t, put)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", resp.StatusCode)
	}
}
