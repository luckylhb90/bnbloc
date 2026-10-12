package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// signalRecorder sits in front of the test realtime: it records (and, when
// failing, refuses) the components_changed signals and proxies the rest.
type signalRecorder struct {
	mu      sync.Mutex
	signals []wire.ComponentsChanged
	fail    bool
}

func (rec *signalRecorder) wrap(t *testing.T, svc *Service) {
	t.Helper()
	upstream, err := url.Parse(svc.Backends.Realtime)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/components/changed" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(svcauth.HeaderUser) != svcauth.ServiceSubject("gateway") || r.Header.Get(svcauth.HeaderSig) == "" {
			t.Errorf("unsigned signal: %v", r.Header)
		}
		var c wire.ComponentsChanged
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Errorf("signal body %s: %v", raw, err)
		}
		rec.mu.Lock()
		rec.signals = append(rec.signals, c)
		fail := rec.fail
		rec.mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	svc.Backends.Realtime = srv.URL
}

// take returns the signals so far as "tenant/member" strings and resets.
func (rec *signalRecorder) take() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, c := range rec.signals {
		if c.AtMs == 0 {
			out = append(out, "no-time")
		}
		out = append(out, c.TenantID+"/"+c.Member)
	}
	rec.signals = nil
	return out
}

func adminCall(t *testing.T, srv *httptest.Server, token, method, path, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Every change of a member's executable catalog signals realtime, which
// pushes the components_changed hint to the desktops: platform changes for
// everyone, an opt-in for the one member in every organization (the row is
// the member's, and their desktop streams in its own session tenant, not
// the web's acting tenant).
func TestComponentChanges_signalRealtime(t *testing.T) {
	t.Setenv("BNBLOC_INTERNAL_KEY", "signal-test-internal-key")
	svc, srv, adminToken, memberToken := rolloutTestGateway(t)
	rec := &signalRecorder{}
	rec.wrap(t, svc)

	installRolloutTestCore(t, srv, adminToken, "signal-core")
	if got := rec.take(); strings.Join(got, ",") != "/" {
		t.Fatalf("install signals = %v", got)
	}
	if status := setRollout(t, srv, adminToken, "signal-core", "optin"); status != http.StatusNoContent {
		t.Fatalf("rollout = %d", status)
	}
	if got := rec.take(); strings.Join(got, ",") != "/" {
		t.Fatalf("rollout signals = %v", got)
	}
	if status := memberInstallStatus(t, srv, memberToken, "signal-core", http.MethodPost); status != http.StatusNoContent {
		t.Fatalf("member install = %d", status)
	}
	if status := memberInstallStatus(t, srv, memberToken, "signal-core", http.MethodDelete); status != http.StatusNoContent {
		t.Fatalf("member uninstall = %d", status)
	}
	if got := rec.take(); strings.Join(got, ",") != "/member,/member" {
		t.Fatalf("opt-in signals = %v", got)
	}
	if status := adminCall(t, srv, adminToken, http.MethodPut, "/v1/platform/components/signal-core/tenants", `{"enabledTenants":["home"]}`); status != http.StatusNoContent {
		t.Fatalf("tenants = %d", status)
	}
	if got := rec.take(); strings.Join(got, ",") != "/" {
		t.Fatalf("tenants signals = %v", got)
	}
	// A refused change signals nothing.
	if status := setRollout(t, srv, adminToken, "signal-core", "sometimes"); status != http.StatusBadRequest {
		t.Fatalf("bad rollout = %d", status)
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("refused change signalled %v", got)
	}

	// realtime refusing the signal never fails the change.
	rec.fail = true
	if status := setRollout(t, srv, adminToken, "signal-core", "auto"); status != http.StatusNoContent {
		t.Fatalf("rollout with realtime failing = %d", status)
	}
	if status := adminCall(t, srv, adminToken, http.MethodDelete, "/v1/platform/components/signal-core", ""); status != http.StatusNoContent {
		t.Fatalf("uninstall with realtime failing = %d", status)
	}
	if got := rec.take(); strings.Join(got, ",") != "/,/" {
		t.Fatalf("signals while failing = %v", got)
	}
}
