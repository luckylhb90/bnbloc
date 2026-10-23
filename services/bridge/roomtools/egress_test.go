package roomtools

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
				Card:   &component.Card{Title: "Connect the wiki", Actions: []component.CardAction{{Kind: component.CardOpen, Label: "Connect", Page: "connections"}}}}
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
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"})
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

	resp, err = client.Get(direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(raw) != "direct" || len(envelopes) != 2 {
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
	sess.SetEgressHosts(map[string]string{"wiki.example.com": "connections"})
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
