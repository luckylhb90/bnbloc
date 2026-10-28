package roomtools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
)

// A session held on this computer (docs/prd/2026-10-28-member-held-sessions.md):
// the service asks for a sign-in's steps first (StepsOnly), the system sets
// its cookie on a step, the service reads the steps' report and answers the
// real Send naming the session, which carries the cookie. The command never
// sees a cookie; a send that names no session, or another service's, never
// carries it; EndSession drops it.
func TestEgressSessionHeldHere(t *testing.T) {
	var mu sync.Mutex
	var seen []string // cookies the system received on /api
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/signin":
			if r.URL.Query().Get("code") != "C" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "S1", Path: "/", HttpOnly: true, Secure: true})
			w.Header().Set("Location", "/app")
			w.WriteHeader(http.StatusSeeOther)
		case "/config":
			c, err := r.Cookie("sess")
			if err == nil && c.Value == "S1" {
				_, _ = io.WriteString(w, `csrf="T1"`)
				return
			}
			_, _ = io.WriteString(w, `csrf=""`)
		default:
			mu.Lock()
			seen = append(seen, r.Header.Get("Cookie"))
			mu.Unlock()
			if c, err := r.Cookie("sess"); err != nil || c.Value != "S1" || r.Header.Get("X-Csrf") != "T1" {
				_, _ = io.WriteString(w, `{"code":401}`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "S1", Path: "/"})
			_, _ = io.WriteString(w, `{"code":0}`)
		}
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer func() { hereClient = old }()

	const host = "ci.example.com"
	var mode string // "" signs in once; "plain" sends no session; "end" signs in afresh
	signedIn := false
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		send := &component.EgressSend{Remove: []string{"Cookie"}, Session: "member:maya"}
		switch {
		case mode == "plain" && in.Report == nil:
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Remove: []string{"Cookie"}, Header: map[string][]string{"X-Csrf": {"T1"}}}}
		case in.Report != nil && len(in.Report.Steps) > 0:
			// The steps' report: the CSRF read from the second step.
			if in.Report.Steps[0].Status != http.StatusSeeOther || !strings.Contains(string(in.Report.Steps[1].Body), `"T1"`) {
				return http.StatusOK, component.EgressResponse{Status: http.StatusBadGateway, Body: []byte("sign-in failed")}
			}
			signedIn = true
			send.Header = map[string][]string{"X-Csrf": {"T1"}}
			return http.StatusOK, component.EgressResponse{Send: send}
		case in.Report == nil && (!signedIn || mode == "end"):
			send.Steps = []component.EgressStep{{Method: http.MethodGet, URL: "https://" + host + "/signin?code=C"}, {Method: http.MethodGet, URL: "https://" + host + "/config"}}
			send.StepsOnly, send.EndSession = true, mode == "end"
			mode = ""
			return http.StatusOK, component.EgressResponse{Send: send}
		case in.Report == nil:
			send.Header = map[string][]string{"X-Csrf": {"T1"}}
			return http.StatusOK, component.EgressResponse{Send: send}
		}
		return http.StatusOK, component.EgressResponse{Accept: true}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{host: "connections"}, nil)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	client := proxyClient(t, srv, token, nil)

	get := func() (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/api/apps", nil)
		req.Header.Set("Cookie", "sess=forged")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp.Header
	}
	// Signed in on this computer; the command gets the answer, no cookie.
	if body, h := get(); body != `{"code":0}` || h.Get("Set-Cookie") != "" {
		t.Fatalf("first request = %q %v", body, h)
	}
	// The session is kept: the next request carries it with no sign-in.
	if body, _ := get(); body != `{"code":0}` {
		t.Fatalf("second request = %q", body)
	}
	// A send that names no session carries none (and never the command's).
	mode = "plain"
	if body, _ := get(); body != `{"code":401}` {
		t.Fatalf("a send without the session = %q", body)
	}
	// Another service's send never reads this one's session.
	if j := sessions.jar(sessionKey{"other", host, "member:maya"}); len(j.Cookies(mustURL(t, "https://"+host+"/api"))) != 0 {
		t.Fatal("another service sees the session")
	}
	// EndSession signs in afresh.
	mode = "end"
	if body, _ := get(); body != `{"code":0}` {
		t.Fatalf("after a fresh sign-in = %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range seen {
		if strings.Contains(c, "forged") {
			t.Fatalf("the command's cookie reached the system: %q", seen)
		}
	}
}

// A sign-in step is a GET or HEAD of the request's own https host.
func TestEgressStepsStayOnTheHost(t *testing.T) {
	in := component.EgressRequest{Method: http.MethodGet, URL: "https://ci.example.com/api"}
	jar := sessions.jar(sessionKey{"connections", "ci.example.com", "t"})
	for _, st := range []component.EgressStep{
		{Method: http.MethodGet, URL: "https://evil.example.com/x"},
		{Method: http.MethodPost, URL: "https://ci.example.com/x"},
		{Method: http.MethodGet, URL: "http://ci.example.com/x"},
	} {
		if _, err := sendSteps(context.Background(), in, &component.EgressSend{Steps: []component.EgressStep{st}}, jar); err == nil {
			t.Errorf("step %+v was sent", st)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
