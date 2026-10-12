package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// tokenGateway is a gateway whose role resolution makes root and svc:smoke
// platform super-admins and everyone else a member.
func tokenGateway(t *testing.T) (*Service, *httptest.Server) {
	t.Helper()
	gw := newGateway(t, "")
	gw.ExtAudit = store.NewMemoryExtensionAudit()
	gw.TenantResolver = func(id auth.Identity, def string) (string, string, error) {
		if id.Subject == "root" || id.Subject == "svc:smoke" {
			return "home", string(rbac.RoleSuperAdmin), nil
		}
		return "home", string(rbac.RoleMember), nil
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return gw, srv
}

func sessionFor(gw *Service, subject, role string) string {
	return gw.Signer.Issue(auth.Claims{Subject: subject, Tenant: "home", Role: role, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

func call(t *testing.T, method, u, bearer string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, u, rd)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestAPITokenLifecycle(t *testing.T) {
	gw, srv := tokenGateway(t)
	root := sessionFor(gw, "root", string(rbac.RoleSuperAdmin))
	member := sessionFor(gw, "alice", string(rbac.RoleMember))

	// Members cannot issue or list.
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/api-tokens", member, map[string]any{"label": "x"}); code != http.StatusForbidden {
		t.Fatalf("member create = %d", code)
	}
	// A super-admin cannot mint a token that acts as a colleague.
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/api-tokens", root, map[string]any{"label": "x", "subject": "alice"}); code != http.StatusBadRequest {
		t.Fatalf("impersonation create = %d", code)
	}
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/api-tokens", root, map[string]any{"label": "x", "ttlDays": 400}); code != http.StatusBadRequest {
		t.Fatalf("ttl 400 create = %d", code)
	}
	code, raw := call(t, http.MethodPost, srv.URL+"/v1/platform/api-tokens", root, map[string]any{"label": "nightly smoke", "subject": "svc:smoke"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created struct {
		ID, Token, Subject string
		Active             bool
		ExpiresAtMs        int64
	}
	_ = json.Unmarshal(raw, &created)
	if !strings.HasPrefix(created.Token, apiTokenFormat.Prefix+created.ID+"_") || created.Subject != "svc:smoke" || !created.Active {
		t.Fatalf("created = %+v", created)
	}
	// Listing never returns the secret.
	code, raw = call(t, http.MethodGet, srv.URL+"/v1/platform/api-tokens", root, nil)
	if code != http.StatusOK || strings.Contains(string(raw), created.Token) || !strings.Contains(string(raw), created.ID) {
		t.Fatalf("list = %d %s", code, raw)
	}

	// Exchange: a 1h session of the token's subject with its resolved role.
	code, raw = call(t, http.MethodPost, srv.URL+"/v1/auth/api-token", "", map[string]string{"token": created.Token})
	if code != http.StatusOK {
		t.Fatalf("exchange = %d %s", code, raw)
	}
	var grant apiTokenGrant
	_ = json.Unmarshal(raw, &grant)
	claims, err := gw.Signer.Verify(grant.Token)
	if err != nil || claims.Subject != "svc:smoke" || claims.Role != string(rbac.RoleSuperAdmin) || grant.Tenant != "home" {
		t.Fatalf("session claims = %+v, %v (grant %+v)", claims, err, grant)
	}
	// The exchanged session works on super-admin routes.
	if code, _ := call(t, http.MethodGet, srv.URL+"/v1/platform/api-tokens", grant.Token, nil); code != http.StatusOK {
		t.Fatalf("exchanged session list = %d", code)
	}
	// A tampered secret is refused.
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/auth/api-token", "", map[string]string{"token": created.Token + "x"}); code != http.StatusUnauthorized {
		t.Fatalf("tampered exchange = %d", code)
	}
	// Revoke: further exchanges fail; a second revoke is 404.
	if code, _ := call(t, http.MethodDelete, srv.URL+"/v1/platform/api-tokens/"+created.ID, root, nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if code, _ := call(t, http.MethodDelete, srv.URL+"/v1/platform/api-tokens/"+created.ID, root, nil); code != http.StatusNotFound {
		t.Fatalf("second revoke = %d", code)
	}
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/auth/api-token", "", map[string]string{"token": created.Token}); code != http.StatusUnauthorized {
		t.Fatalf("revoked exchange = %d", code)
	}
	// Issue, exchange and revoke are audited.
	recs, _ := gw.ExtAudit.ExtAuditRecords(context.Background(), apiTokenAuditKind, 100, 0)
	actions := map[string]int{}
	for _, r := range recs {
		actions[r.Action+"/"+r.Outcome]++
		if r.TenantID == "" {
			t.Errorf("audit row %s/%s has no tenant: its admins never see it", r.Action, r.Outcome)
		}
	}
	if actions["issue/allow"] != 1 || actions["exchange/allow"] != 1 || actions["revoke/allow"] != 1 || actions["exchange/deny"] < 2 {
		t.Fatalf("audit = %v", actions)
	}
}

// installTestNotifier installs (unsigned, dev) a notifier record into the
// gateway's platform settings, the way realtime's directory reads it.
func installTestNotifier(t *testing.T, settings store.PlatformSettingsStore, name string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"name": name, "contract": pkgmeta.NotifierContractV1})
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	body, _ := json.Marshal(map[string]any{
		"kind": pkgmeta.KindNotifierPackageV1, "name": name, "version": "0.1.0", "publisher": "test",
		"runtime": "endpoint", "contract": pkgmeta.NotifierContractV1,
		"endpoint": map[string]any{"baseUrl": srv.URL}, "permissions": map[string]any{"network": []string{u.Hostname()}},
	})
	pipe := &extpipe.Pipeline{Settings: settings, Audit: store.NewMemoryExtensionAudit(), AllowUnsigned: true,
		Installers: map[extreg.Kind]extpipe.KindInstaller{extreg.KindNotifier: notify.NewDirectory(settings).Installer()}}
	if _, err := pipe.Install(context.Background(), extpipe.InstallRequest{Body: body, Actor: "root"}); err != nil {
		t.Fatalf("install notifier: %v", err)
	}
}

func TestPlatformAlert(t *testing.T) {
	gw, srv := tokenGateway(t)
	settings := store.NewMemoryPlatformSettings()
	outbox := store.NewMemoryNotifications()
	gw.SuperAdmins = []string{"root", "ops@example.com"}
	gw.Alerts = notify.NewEmitter(notify.Stores{Prefs: outbox, Outbox: outbox, Settings: settings, Installs: store.NewMemoryComponentInstall()}, "")
	root := sessionFor(gw, "root", string(rbac.RoleSuperAdmin))
	member := sessionFor(gw, "alice", string(rbac.RoleMember))

	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", member, map[string]string{"title": "x"}); code != http.StatusForbidden {
		t.Fatalf("member alert = %d", code)
	}
	// (No notifier installed answers 409 — covered in internal/notify; the
	// directory caches records for a few seconds, so install first here.)
	installTestNotifier(t, settings, "n1")
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, map[string]string{"title": ""}); code != http.StatusBadRequest {
		t.Fatalf("empty title alert = %d", code)
	}
	code, raw := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, wire.AdminAlertRequest{Title: "smoke failed", Text: "FAIL chat echo", URL: "https://gw.example.com"})
	if code != http.StatusAccepted {
		t.Fatalf("alert = %d %s", code, raw)
	}
	var res wire.AdminAlertResponse
	_ = json.Unmarshal(raw, &res)
	if strings.Join(res.Recipients, ",") != "root,ops@example.com" || strings.Join(res.Notifiers, ",") != "n1" {
		t.Fatalf("alert response = %+v", res)
	}
	it, ok := outbox.Item("home", res.ID, "n1")
	if !ok {
		t.Fatal("alert not queued in the caller's tenant")
	}
	var p wire.NotificationPayload
	_ = json.Unmarshal(it.EventJSON, &p)
	if p.Type != wire.NotificationAdminAlert || p.Title != "smoke failed" || p.Actor != "root" || len(p.Recipients) != 2 {
		t.Fatalf("payload = %+v", p)
	}
	// Explicit recipients replace the default.
	code, raw = call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, wire.AdminAlertRequest{Title: "t", Recipients: []string{"bob"}})
	_ = json.Unmarshal(raw, &res)
	if code != http.StatusAccepted || strings.Join(res.Recipients, ",") != "bob" {
		t.Fatalf("explicit recipients = %d %+v", code, res)
	}
}

// An alert nothing would carry is a 409 the CLI reports, not a silent 202;
// a gateway without the notification store answers 503.
func TestPlatformAlertWithoutNotifier(t *testing.T) {
	gw, srv := tokenGateway(t)
	root := sessionFor(gw, "root", string(rbac.RoleSuperAdmin))
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, map[string]string{"title": "x"}); code != http.StatusServiceUnavailable {
		t.Fatalf("no store alert = %d", code)
	}
	mem := store.NewMemoryNotifications()
	gw.SuperAdmins = []string{"root"}
	gw.Alerts = notify.NewEmitter(notify.Stores{Prefs: mem, Outbox: mem, Settings: store.NewMemoryPlatformSettings()}, "")
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, map[string]string{"title": "x"}); code != http.StatusConflict {
		t.Fatalf("no notifier alert = %d", code)
	}
	gw.SuperAdmins = nil
	if code, _ := call(t, http.MethodPost, srv.URL+"/v1/platform/alerts", root, map[string]string{"title": "x"}); code != http.StatusBadRequest {
		t.Fatalf("no recipients alert = %d", code)
	}
}
