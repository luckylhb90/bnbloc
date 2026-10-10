package shareagenthttp

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// auditFixture: tenants "default" (admin root, member ann) and "acme", with
// audit rows in both, one platform (tenantless) row, and borrow decisions.
func auditFixture(t *testing.T) (*Service, http.Handler) {
	t.Helper()
	svc, grants := newService(t, "")
	tenants := store.NewMemoryTenant()
	for _, m := range []store.TenantMember{
		{TenantID: tenant.Default, UserID: "root", Role: store.OrgRoleAdmin, JoinedAtMs: 1},
		{TenantID: tenant.Default, UserID: "ann", Role: store.OrgRoleMember, JoinedAtMs: 1},
	} {
		_ = tenants.PutMember(dctx(), m)
	}
	svc.Tenants = tenants
	ext := store.NewMemoryExtensionAudit()
	svc.ExtAudit = ext
	svc.AuditLog = []store.AuditLogReader{ext, grants}
	for _, rec := range []store.ExtAuditRecord{
		{Kind: "plugin", Name: "p", Action: "install", Actor: "boss", Outcome: "allow", AtMs: 50},
		{Kind: "member", Name: "x", Action: "add", Actor: "acme-admin", TenantID: "acme", Outcome: "ok", AtMs: 60},
		{Kind: "notifier", Name: "n", Action: "deliver", TenantID: tenant.Default, Outcome: "delivered", AtMs: 40},
	} {
		_ = ext.AppendExtAudit(context.Background(), rec)
	}
	_ = grants.AppendAudit(dctx(), store.AuditRecord{RoomID: "room-1", ActorUserID: "bob", OwnerUserID: "alice", Action: "turn", Decision: "deny", DenyReason: "no_grant", AtMs: 45})
	_ = grants.AppendAudit(tenant.NewContext(context.Background(), "acme"), store.AuditRecord{RoomID: "r", ActorUserID: "z", OwnerUserID: "y", Action: "turn", Decision: "allow", AtMs: 70})
	return svc, svc.Handler()
}

// tenantScopedReq is a request whose ctx tenant is bound as the gateway's
// verified tenant would be (svcauth middleware ran first).
func tenantScopedReq(path, caller, tid string) *http.Request {
	r := tenantReq(http.MethodGet, path, caller, "")
	r.Header.Set(svcauth.HeaderTenant, tid)
	return r.WithContext(tenant.NewContext(r.Context(), tid))
}

func getAuditLog(t *testing.T, h http.Handler, r *http.Request) wire.AuditLogReply {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", r.URL, w.Code, w.Body)
	}
	var out wire.AuditLogReply
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// An org admin sees exactly their tenant: never another tenant's rows, never
// tenantless platform rows, and a ?tenant= parameter cannot widen the view.
func TestTenantAuditLogIsolation(t *testing.T) {
	svc, h := auditFixture(t)
	// The admin's own change is recorded and visible.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, tenantReq(http.MethodPut, "/v1/tenant/members/ann/role", "root", `{"role":"viewer"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("set role = %d %s", w.Code, w.Body)
	}
	_ = svc
	for _, tid := range []string{tenant.Default, "acme"} {
		out := getAuditLog(t, h, tenantScopedReq("/v1/tenant/audit-log?tenant=acme&tenant=", "root", tid))
		if len(out.Entries) == 0 {
			t.Fatalf("tenant %s: no entries", tid)
		}
		for _, e := range out.Entries {
			if e.TenantID != tid {
				t.Fatalf("tenant %s view leaked %+v", tid, e)
			}
		}
	}
	out := getAuditLog(t, h, tenantScopedReq("/v1/tenant/audit-log", "root", tenant.Default))
	kinds := []string{}
	for _, e := range out.Entries {
		kinds = append(kinds, e.Kind+"/"+e.Action)
	}
	if strings.Join(kinds, ",") != "member/role,borrow/turn,notifier/deliver" {
		t.Fatalf("default tenant entries = %v", kinds)
	}
	if e := out.Entries[0]; e.Actor != "root" || e.Name != "ann" || !strings.Contains(e.Detail, "role=viewer") {
		t.Fatalf("role change row = %+v", e)
	}
}

// The platform view spans every tenant (and platform rows) newest first, or
// one tenant with ?tenant=; pages chain through the opaque cursor.
func TestPlatformAuditLogMergesAndPages(t *testing.T) {
	_, h := auditFixture(t)
	var got []string
	cursor := ""
	for i := 0; i < 10; i++ {
		path := "/v1/platform/audit-log?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		out := getAuditLog(t, h, tenantScopedReq(path, "boss", tenant.Default))
		for _, e := range out.Entries {
			got = append(got, e.Kind+"@"+e.TenantID)
		}
		if cursor = out.NextCursor; cursor == "" {
			break
		}
	}
	want := "borrow@acme,member@acme,plugin@,borrow@default,notifier@default"
	if strings.Join(got, ",") != want {
		t.Fatalf("platform pages = %v, want %s", got, want)
	}
	out := getAuditLog(t, h, tenantScopedReq("/v1/platform/audit-log?tenant=acme&kind=borrow", "boss", tenant.Default))
	if len(out.Entries) != 1 || out.Entries[0].TenantID != "acme" || out.Entries[0].Kind != "borrow" {
		t.Fatalf("platform tenant+kind filter = %+v", out.Entries)
	}
	out = getAuditLog(t, h, tenantScopedReq("/v1/platform/audit-log?outcome=deny&actor=bob", "boss", tenant.Default))
	if len(out.Entries) != 1 || out.Entries[0].Detail != "reason=no_grant" {
		t.Fatalf("outcome/actor filter = %+v", out.Entries)
	}
	out = getAuditLog(t, h, tenantScopedReq("/v1/platform/audit-log?from=45&to=60", "boss", tenant.Default))
	if len(out.Entries) != 2 {
		t.Fatalf("from/to filter = %+v", out.Entries)
	}
}

func TestAuditLogCSVAndBadInput(t *testing.T) {
	_, h := auditFixture(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, tenantScopedReq("/v1/tenant/audit-log?format=csv", "root", tenant.Default))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv = %d %s", w.Code, w.Header())
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][0] != "time_utc" {
		t.Fatalf("csv rows = %v (%v)", rows, err)
	}
	for _, bad := range []string{"?cursor=nope", "?limit=x", "?from=yesterday", "?from=60&to=50", "?format=xml"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, tenantScopedReq("/v1/tenant/audit-log"+bad, "root", tenant.Default))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, w.Code)
		}
	}
}

// Member removal, session revocation and tenant status changes are audited
// into the affected tenant.
func TestAdministrationIsAudited(t *testing.T) {
	svc, h := auditFixture(t)
	svc.RefreshTokens = store.NewMemoryRefreshToken()
	_ = svc.RefreshTokens.CreateRefreshFamily(dctx(), store.RefreshFamily{FamilyID: "ann-1", Subject: "ann", TenantID: tenant.Default, CurrentJTI: "j", ExpiresAtMs: 1 << 62})
	_ = svc.Tenants.PutTenant(dctx(), store.Tenant{TenantID: "acme", Name: "Acme", Status: store.TenantActive})
	for _, req := range []*http.Request{
		tenantReq(http.MethodDelete, "/v1/tenant/members/ann/sessions", "root", ""),
		tenantReq(http.MethodDelete, "/v1/tenant/members/ann", "root", ""),
		tenantReq(http.MethodPut, "/v1/platform/tenants/acme/status", "boss", `{"status":"suspended"}`),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", req.Method, req.URL, w.Code, w.Body)
		}
	}
	out := getAuditLog(t, h, tenantScopedReq("/v1/tenant/audit-log?kind=session,member", "root", tenant.Default))
	var got []string
	for _, e := range out.Entries {
		got = append(got, e.Kind+"/"+e.Action+"/"+e.Name)
	}
	if strings.Join(got, ",") != "member/remove/ann,session/revoke_member/ann" {
		t.Fatalf("default tenant admin rows = %v", got)
	}
	out = getAuditLog(t, h, tenantScopedReq("/v1/platform/audit-log?tenant=acme&kind=tenant", "boss", tenant.Default))
	if len(out.Entries) != 1 || out.Entries[0].Detail != "status=suspended" || out.Entries[0].Actor != "boss" {
		t.Fatalf("tenant status row = %+v", out.Entries)
	}
}
