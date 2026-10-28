package roomtools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
)

// A host claimed only on some paths: a request under them goes to the
// service; any other path goes out from here as the command sent it (its
// own headers, the answer's cookies), and the service never sees it.
func TestEgressPartlyClaimedHost(t *testing.T) {
	var mu sync.Mutex
	var sys []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sys = append(sys, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("X-Signature-signature")+" sso="+r.Header.Get("Token"))
		mu.Unlock()
		if r.URL.Path == "/v1/messages" {
			http.SetCookie(w, &http.Cookie{Name: "own", Value: "1"})
		}
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	}))
	defer srv.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer func() { hereClient = old }()

	var seen []string
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		seen = append(seen, in.URL)
		mu.Unlock()
		if in.Report == nil {
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Remove: []string{"X-Signature-signature"}, Header: map[string][]string{"X-Signature-signature": {"SIGNED"}, "Token": {"SSO"}}}}
		}
		return http.StatusOK, component.EgressResponse{Accept: true}
	}}
	srvR, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"api.example.com": "connections"}, nil)
	sess.SetEgressPaths(map[string][]string{"api.example.com": {"/v1/meeting", "/v1/calendar/"}})
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	client := proxyClient(t, srvR, token, nil)

	get := func(path string) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com"+path, nil)
		req.Header.Set("X-Signature-signature", "OWN")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp.Header
	}
	if body, h := get("/v1/messages"); body != "ok /v1/messages" || !strings.Contains(h.Get("Set-Cookie"), "own=1") {
		t.Fatalf("pass-through = %q %v", body, h)
	}
	if body, _ := get("/v1/meeting/records"); body != "ok /v1/meeting/records" {
		t.Fatalf("claimed path = %q", body)
	}
	if body, _ := get("/v1/calendar/users/events"); body != "ok /v1/calendar/users/events" {
		t.Fatalf("claimed path = %q", body)
	}
	if body, _ := get("/v1/meetingsX"); body != "ok /v1/meetingsX" {
		t.Fatalf("near path = %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /v1/messages auth=OWN sso=",
		"GET /v1/meeting/records auth=SIGNED sso=SSO",
		"GET /v1/calendar/users/events auth=SIGNED sso=SSO",
		"GET /v1/meetingsX auth=OWN sso=",
	}
	if strings.Join(sys, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the system got:\n%s", strings.Join(sys, "\n"))
	}
	if len(seen) != 4 { // two claimed requests: an ask and a report each
		t.Fatalf("the service saw %v", seen)
	}
	for _, u := range seen {
		if !strings.Contains(u, "/v1/meeting/") && !strings.Contains(u, "/v1/calendar/") {
			t.Fatalf("the service saw an unclaimed path: %s", u)
		}
	}
}
