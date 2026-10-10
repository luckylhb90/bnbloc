package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAlertGateway serves the API token exchange, a failing smoke auth probe
// and the alert endpoint, recording what it received.
type fakeAlertGateway struct {
	*httptest.Server
	mu        sync.Mutex
	exchanged int
	bearers   []string
	alerts    []adminAlert
	alertCode int
}

func newFakeAlertGateway(t *testing.T) *fakeAlertGateway {
	g := &fakeAlertGateway{alertCode: http.StatusAccepted}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/api-token":
			var body struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Token != "bnbloc_at_good" {
				http.Error(w, "invalid", http.StatusUnauthorized)
				return
			}
			g.exchanged++
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "sess-1", "subject": "svc:smoke", "tenant": "home", "role": "super_admin"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/users/"):
			g.bearers = append(g.bearers, r.Header.Get("Authorization"))
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/platform/alerts":
			g.bearers = append(g.bearers, r.Header.Get("Authorization"))
			var a adminAlert
			_ = json.NewDecoder(r.Body).Decode(&a)
			g.alerts = append(g.alerts, a)
			if g.alertCode != http.StatusAccepted {
				http.Error(w, "no notifier component reaches the recipients", g.alertCode)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(adminAlertResult{ID: "ntf_1", Recipients: []string{"root"}, Notifiers: []string{"n1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// BNBLOC_API_TOKEN is exchanged once per process for a session; a failed
// smoke run with --alert-on-failure alerts with the failed steps and the
// gateway, and still fails.
func TestSmokeAlertOnFailureWithAPIToken(t *testing.T) {
	isolateHome(t)
	g := newFakeAlertGateway(t)
	t.Setenv("BNBLOC_TOKEN", "")
	t.Setenv(apiTokenEnv, "bnbloc_at_good")
	c := newClient(resolveConfig(g.URL, ""))
	run := &smokeRun{out: &bytes.Buffer{}}
	err := smokeChecks(context.Background(), c, smokeTestOptions(), run)
	if err == nil {
		t.Fatal("smoke passed against a failing gateway")
	}
	var out, errOut bytes.Buffer
	alertSmokeFailure(c, run, err, "codex", []string{"root"}, &out, &errOut)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exchanged != 1 {
		t.Fatalf("api token exchanged %d times, want once per process", g.exchanged)
	}
	for _, b := range g.bearers {
		if b != "Bearer sess-1" {
			t.Fatalf("request bearer = %q, want the exchanged session", b)
		}
	}
	if len(g.alerts) != 1 {
		t.Fatalf("alerts = %d (stderr %q)", len(g.alerts), errOut.String())
	}
	a := g.alerts[0]
	if a.Title != "bnbloc smoke failed on "+g.URL || a.URL != g.URL || !strings.Contains(a.Text, "FAIL auth:") ||
		!strings.Contains(a.Text, "agent codex") || strings.Join(a.Recipients, ",") != "root" {
		t.Fatalf("alert = %+v", a)
	}
	if !strings.Contains(out.String(), "ALERT ntf_1 queued for root via n1") {
		t.Fatalf("stdout = %q", out.String())
	}
}

// An alert that cannot be sent is reported, never swallowed.
func TestSmokeAlertFailureIsReported(t *testing.T) {
	isolateHome(t)
	g := newFakeAlertGateway(t)
	g.alertCode = http.StatusConflict
	c := newClient(config{Gateway: g.URL, Token: testSession("root", nil)})
	run := &smokeRun{out: &bytes.Buffer{}, failures: []string{"FAIL chat echo: timeout"}}
	var out, errOut bytes.Buffer
	alertSmokeFailure(c, run, errSmokeFailed, "", nil, &out, &errOut)
	if !strings.Contains(errOut.String(), "smoke alert NOT sent") || !strings.Contains(errOut.String(), "409") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// A refused API token names the variable and the fix.
func TestAPITokenRefused(t *testing.T) {
	isolateHome(t)
	g := newFakeAlertGateway(t)
	c := newClient(config{Gateway: g.URL, APIToken: "bnbloc_at_bad"})
	if _, err := c.bearer(); err == nil || !strings.Contains(err.Error(), apiTokenEnv) {
		t.Fatalf("bearer err = %v", err)
	}
}

func TestSmokeAlertTextIsBounded(t *testing.T) {
	a := smokeAlert("https://gw", []string{strings.Repeat("x", 800)}, errSmokeFailed, "")
	if n := len([]rune(a.Text)); n != 500 {
		t.Fatalf("text runes = %d", n)
	}
	if a := smokeAlert("gw-without-scheme", nil, errSmokeFailed, ""); a.URL != "" || a.Text != errSmokeFailed.Error() {
		t.Fatalf("alert = %+v", a)
	}
}
