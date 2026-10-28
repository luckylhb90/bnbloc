package roomtools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// proxyClient is a command's HTTP client: the session's proxy URL and a
// trust store of the local CA and the extra roots.
func proxyClient(t *testing.T, srv *Server, token string, extra *x509.Certificate) *http.Client {
	t.Helper()
	env := srv.EgressEnv(token, "")
	pu, err := url.Parse(env["HTTPS_PROXY"])
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := srv.EgressCA()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	if extra != nil {
		pool.AddCert(extra)
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

// A request to a host a service claims goes to the service's turn proxy for
// the bound job, as an envelope, and its answer is the command's (without a
// cookie); the card it carries is posted once for the requester. Between
// turns the claimed host answers 503 and nothing is sent. Any other host is
// tunnelled untouched; a wrong bearer gets 407.
func TestEgressProxyRoutesClaimedHostsThroughTheService(t *testing.T) {
	var posted []wire.ComponentCard
	var envelopes []component.EgressRequest
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		switch {
		case strings.HasPrefix(path, "/v1/turn-proxy/connections/egress/request?room=room-1&job=job-1"):
			var in component.EgressRequest
			_ = json.Unmarshal(body, &in)
			envelopes = append(envelopes, in)
			return http.StatusOK, component.EgressResponse{Status: http.StatusForbidden,
				Header: map[string][]string{"Content-Type": {"application/json"}, "Set-Cookie": {"s=1"}},
				Body:   []byte(`{"error":"not connected"}`),
				Card:   &component.Card{Title: "Connect the wiki", Actions: []component.CardAction{{Kind: component.CardOpen, Label: "Connect", Page: "connections", Query: cardQuery(len(envelopes))}}}}
		case strings.Contains(path, "/agent-activity/messages"):
			var n struct {
				Messages []struct {
					Payload map[string]json.RawMessage `json:"payload"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &n)
			raw := n.Messages[0].Payload["card"]
			var c wire.ComponentCard
			_ = json.Unmarshal(raw, &c)
			posted = append(posted, c)
			return http.StatusOK, map[string]any{}
		}
		return http.StatusNotFound, map[string]string{"error": "nope"}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"}, nil)
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "direct") }))
	defer direct.Close()
	client := proxyClient(t, srv, token, direct.Certificate())

	// Between turns: refused, nothing sent.
	resp, err := client.Get("https://wiki.example.com/rest/api/space")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || len(envelopes) != 0 {
		t.Fatalf("between turns = %d, sent %d", resp.StatusCode, len(envelopes))
	}

	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "c1", MessageSeq: 4})
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://wiki.example.com/rest/api/search?q=release", strings.NewReader(`{"x":1}`))
		req.Header.Set("Authorization", "Bearer agent-made-up")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || string(raw) != `{"error":"not connected"}` || resp.Header.Get("Set-Cookie") != "" {
			t.Fatalf("claimed = %d %v %s", resp.StatusCode, resp.Header, raw)
		}
	}
	if len(envelopes) != 2 || envelopes[0].Method != http.MethodPost || envelopes[0].URL != "https://wiki.example.com/rest/api/search?q=release" ||
		string(envelopes[0].Body) != `{"x":1}` || envelopes[0].Header["Authorization"][0] != "Bearer agent-made-up" || envelopes[0].Header["Proxy-Authorization"] != nil {
		t.Fatalf("envelopes = %+v", envelopes)
	}
	if len(posted) != 1 || posted[0].For != "maya" || posted[0].Component != "connections" || posted[0].Title != "Connect the wiki" || len(posted[0].Actions) != 1 {
		t.Fatalf("cards = %+v (one, for the requester)", posted)
	}
	// A card that opens something else (another held change) is posted too.
	if resp, err := client.Get("https://wiki.example.com/rest/api/space"); err == nil {
		_ = resp.Body.Close()
	}
	if len(posted) != 2 || posted[1].Actions[0].Query != "id=h3" {
		t.Fatalf("a card opening another change = %+v", posted)
	}

	resp, err = client.Get(direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(raw) != "direct" || len(envelopes) != 3 {
		t.Fatalf("tunnel = %s (sent %d)", raw, len(envelopes))
	}

	if _, err := proxyClient(t, srv, "wrong-token-wrong-token-wrong-token", nil).Get("https://wiki.example.com/"); err == nil || !strings.Contains(err.Error(), "Proxy Authentication Required") {
		t.Fatalf("wrong bearer = %v", err)
	}
	srv.Revoke(sess)
	if _, err := client.Get("https://wiki.example.com/"); err == nil {
		t.Fatal("a revoked session's bearer still proxies")
	}
}

// A sandboxed session's commands authenticate with its egress alias on the
// proxy-only listener; their tunnels reach only public addresses (the proxy
// never opens what the sandbox does not), while claimed hosts still go
// through the service. The alias goes with the session.
func TestEgressAliasForSandboxedCommands(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		if strings.Contains(path, "/egress/request") {
			return http.StatusOK, component.EgressResponse{Status: http.StatusOK, Body: []byte("via service")}
		}
		return http.StatusNotFound, map[string]string{}
	}}
	srv, err := Start(Config{Gateway: gw, ProxyAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if !srv.ProxyListening() {
		t.Fatal("no proxy listener")
	}
	_, sess, _ := srv.Open(Scope{RoomID: "room-1", Owner: "maya"})
	alias := strings.Repeat("a", 43)
	if err := srv.AliasEgress(alias, sess); err != nil {
		t.Fatal(err)
	}
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"}, nil)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "local") }))
	defer local.Close()
	env := srv.EgressEnv(alias, srv.proxyLn.Addr().String())
	pu, _ := url.Parse(env["HTTPS_PROXY"])
	caPEM, _ := srv.EgressCA()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	pool.AddCert(local.Certificate())
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get("https://wiki.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(raw) != "via service" {
		t.Fatalf("claimed through the alias = %s", raw)
	}
	if _, err := client.Get(local.URL); err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("a sandboxed tunnel to loopback = %v, want refused", err)
	}
	srv.Revoke(sess)
	client.CloseIdleConnections()
	if _, err := client.Get("https://wiki.example.com/"); err == nil {
		t.Fatal("the alias outlives its session")
	}
}

// A service that answers Send has the bridge send the request itself, from
// this computer, with the credentials it gives in place of the agent's;
// the system's answer is reported back; a refused one is sent once more
// with fresh credentials; Accept gives the command the system's answer.
func TestEgressSendFromThisComputer(t *testing.T) {
	var got []http.Header
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
		_, _ = io.WriteString(w, "page for "+r.URL.Query().Get("k"))
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}}
	defer func() { hereClient = old }()
	var reports []component.EgressReport
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		switch {
		case in.Report == nil:
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Remove: []string{"Authorization"}, Header: map[string][]string{"Authorization": {"Bearer stale"}}, Query: map[string][]string{"k": {"v"}}}}
		case in.Report.Attempt == 1:
			reports = append(reports, *in.Report)
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Remove: []string{"Authorization"}, Header: map[string][]string{"Authorization": {"Bearer fresh"}}, Query: map[string][]string{"k": {"v"}}}}
		default:
			reports = append(reports, *in.Report)
			return http.StatusOK, component.EgressResponse{Accept: true}
		}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"}, nil)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	req, _ := http.NewRequest(http.MethodGet, "https://wiki.example.com/page", nil)
	req.Header.Set("Authorization", "Bearer agent-made-up")
	resp, err := proxyClient(t, srv, token, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(raw) != "page for v" || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("answer = %d %q %v", resp.StatusCode, raw, resp.Header)
	}
	if len(got) != 2 || got[0].Get("Authorization") != "Bearer stale" || got[1].Get("Authorization") != "Bearer fresh" {
		t.Fatalf("the system got %v", got)
	}
	if len(reports) != 2 || reports[0].Status != 401 || reports[1].Status != 200 || string(reports[1].Body) != "page for v" {
		t.Fatalf("reports = %+v", reports)
	}
}

// cardQuery is the fake service's card query for its n-th request: the
// first two open the same thing, the third another.
func cardQuery(n int) string {
	if n >= 3 {
		return "id=h3"
	}
	return "system=wiki"
}

// A sandboxed tunnel reaches public addresses, and private ones only for a
// host the member opened; never loopback, link-local or unspecified.
func TestSandboxReaches(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		private bool
		want    bool
	}{
		{"93.184.216.34", false, true}, {"10.1.2.3", false, false}, {"10.1.2.3", true, true}, {"100.64.0.1", false, false},
		{"100.64.0.1", true, true}, {"127.0.0.1", true, false}, {"169.254.169.254", true, false}, {"0.0.0.0", true, false},
		{"::ffff:10.0.0.1", true, true}, {"fe80::1", true, false},
	} {
		if got := sandboxReaches(netip.MustParseAddr(tc.addr), tc.private); got != tc.want {
			t.Errorf("%s (private %v) = %v", tc.addr, tc.private, got)
		}
	}
}

// A send that replaces the query string and the body goes out exactly so
// (a request the service signed over its exact query and body).
func TestEgressSendReplacesQueryAndBody(t *testing.T) {
	var gotQuery, gotBody string
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotQuery, gotBody = r.URL.RawQuery, string(raw)
		_, _ = io.WriteString(w, "ok")
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}}
	defer func() { hereClient = old }()
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		if in.Report == nil {
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{RawQuery: "b=2&a=1&signature=abc", ReplaceBody: true, Body: []byte("x=1")}}
		}
		return http.StatusOK, component.EgressResponse{Accept: true}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"}, nil)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	req, _ := http.NewRequest(http.MethodPost, "https://wiki.example.com/order?a=1&signature=fake", strings.NewReader("x=1&signature=fake"))
	resp, err := proxyClient(t, srv, token, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotQuery != "b=2&a=1&signature=abc" || gotBody != "x=1" {
		t.Fatalf("sent query %q body %q", gotQuery, gotBody)
	}
}

// A send outside a turn runs the same exchange through the send proxy: the
// service's credentials go on the request sent from here, and the report
// goes back to it; no turn is needed.
func TestSendFromHereOutsideATurn(t *testing.T) {
	var got http.Header
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"50113","msg":"Invalid sign"}`)
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}}
	defer func() { hereClient = old }()
	var report *component.EgressReport
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		if path != component.SendProxyPath+"s-1" {
			return http.StatusNotFound, map[string]string{"error": "no"}
		}
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		if in.Report == nil {
			return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Header: map[string][]string{"Ok-Access-Key": {"K"}}}}
		}
		report = in.Report
		return http.StatusOK, component.EgressResponse{Accept: true}
	}}
	if err := SendFromHere(context.Background(), gw, "s-1", "connections", http.MethodGet, "https://www.okx.com/api/v5/account/balance"); err != nil {
		t.Fatal(err)
	}
	if got.Get("Ok-Access-Key") != "K" || report == nil || report.Status != 401 || !strings.Contains(string(report.Body), "50113") {
		t.Fatalf("sent %v, reported %+v", got, report)
	}
	if err := SendFromHere(context.Background(), gw, "nope", "connections", http.MethodGet, "https://www.okx.com/x"); err == nil {
		t.Fatal("a refused send reported no error")
	}
}
