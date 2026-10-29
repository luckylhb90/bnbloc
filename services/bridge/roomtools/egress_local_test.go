package roomtools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// localRig is a model host, a provider on this computer and a bridge
// session that claims the model host's name through it.
type localRig struct {
	t      *testing.T
	srv    *Server
	token  string
	sess   *Session
	client *http.Client

	mu       sync.Mutex
	asked    []string // "<method> <path> drive=<d>" the provider saw
	bodies   []int    // body sizes the provider was sent
	reports  []component.EgressUsage
	cards    []wire.ComponentCard
	sysSaw   []http.Header
	sysURLs  []string
	idle     time.Duration
	answer   func(in component.EgressRequest) component.EgressResponse
	system   http.HandlerFunc
	hostsFor func(turn component.ProviderTurn) component.EgressHosts
}

const modelHost = "api.model.example.com"
const realKey = "sk-real-credential-0123456789"

func newLocalRig(t *testing.T) *localRig {
	t.Helper()
	r := &localRig{t: t}
	r.answer = func(component.EgressRequest) component.EgressResponse {
		return component.EgressResponse{Send: &component.EgressSend{
			Remove: []string{"Authorization", "X-Api-Key"}, RemoveQuery: []string{"key"},
			Header: map[string][]string{"Authorization": {"Bearer " + realKey}}, Relay: true, RelayIdleSeconds: 600}}
	}
	r.hostsFor = func(component.ProviderTurn) component.EgressHosts {
		return component.EgressHosts{Hosts: []string{modelHost}, Env: map[string]string{"MODEL_API_KEY": "placeholder-key-000"}}
	}
	r.system = func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, "ok") }

	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.sysSaw = append(r.sysSaw, req.Header.Clone())
		r.sysURLs = append(r.sysURLs, req.URL.String())
		r.mu.Unlock()
		r.system(w, req)
	}))
	t.Cleanup(sys.Close)
	old := relayClient
	relayClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
			idle, _ := ctx.Value(relayIdleKey{}).(time.Duration)
			r.mu.Lock()
			r.idle = idle
			r.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return idleConn{Conn: c, idle: idle}, nil
		}}, CheckRedirect: old.CheckRedirect}
	t.Cleanup(func() { relayClient = old })

	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var turn component.ProviderTurn
		_ = component.DecodeHeader(req.Header.Get(component.TurnHeader), &turn)
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.asked = append(r.asked, req.Method+" "+req.URL.Path+" drive="+turn.Drive)
		r.bodies = append(r.bodies, len(body))
		r.mu.Unlock()
		if req.Header.Get("Authorization") != "Bearer provider-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case component.EgressProviderHostsPath:
			_ = json.NewEncoder(w).Encode(r.hostsFor(turn))
		case component.EgressProviderRequestPath:
			var in component.EgressRequest
			_ = json.Unmarshal(body, &in)
			r.mu.Lock()
			ans := r.answer
			r.mu.Unlock()
			_ = json.NewEncoder(w).Encode(ans(in))
		case component.EgressProviderReportPath:
			var u component.EgressUsage
			_ = json.Unmarshal(body, &u)
			r.mu.Lock()
			r.reports = append(r.reports, u)
			r.mu.Unlock()
			_, _ = io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(prov.Close)
	dir := t.TempDir()
	r.writeDescriptor(dir, "acme", component.EgressProviderContract, prov.URL, "provider-token")
	providers, dropped := ReadLocalProviders(dir)
	if len(providers) != 1 || len(dropped) != 0 {
		t.Fatalf("providers = %v dropped = %v", providers, dropped)
	}

	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		if strings.Contains(path, "/agent-activity/messages") {
			var n struct {
				Messages []struct {
					Payload map[string]json.RawMessage `json:"payload"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &n)
			var c wire.ComponentCard
			_ = json.Unmarshal(n.Messages[0].Payload["card"], &c)
			r.mu.Lock()
			r.cards = append(r.cards, c)
			r.mu.Unlock()
			return http.StatusOK, map[string]any{}
		}
		return http.StatusNotFound, map[string]string{"error": "the gateway is never asked for a local provider"}
	}}
	r.srv, r.token, r.sess = startServer(t, gw)
	r.sess.SetLocalProviders(providers)
	r.client = proxyClient(t, r.srv, r.token, nil)
	return r
}

func (r *localRig) writeDescriptor(dir, name, contract, url, token string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		r.t.Fatal(err)
	}
	raw, _ := json.Marshal(component.EgressProviderFile{Contract: contract, URL: url, Token: token})
	if err := os.WriteFile(filepath.Join(dir, name, component.EgressProviderFileName), raw, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// turn starts a turn the way the bridge does: the provider is asked for its
// hosts (an own run only) and the claim is set.
func (r *localRig) turn(job Job) {
	r.t.Helper()
	r.sess.Bind(job)
	hosts := map[string]string{}
	if job.OwnRun {
		h, err := r.sess.local["local:acme"].Hosts(context.Background(), ProviderTurnOf(r.sess.Scope(), job))
		if err != nil {
			r.t.Fatal(err)
		}
		for _, host := range h.Hosts {
			hosts[host] = "local:acme"
		}
	}
	r.sess.SetEgressHosts(hosts, nil)
}

func (r *localRig) post(body string, header map[string]string) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, "https://"+modelHost+"/v1/chat/completions?key=placeholder-q&alt=sse", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer placeholder-key-000")
	req.Header.Set("X-Api-Key", "placeholder-key-000")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return r.client.Do(req)
}

var ownJob = Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "ch", OwnRun: true}

// The member's own run claims the model host through the provider on this
// computer: the agent's placeholder (header and query) is removed, the
// credential the provider chose is set for this one request, and the
// provider never sees a body or the gateway a question.
func TestLocalProviderInjectsForAnOwnRun(t *testing.T) {
	r := newLocalRig(t)
	r.turn(ownJob)
	resp, err := r.post(`{"prompt":"hi"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("answer = %d %q", resp.StatusCode, b)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := r.sysSaw[0].Get("Authorization"); got != "Bearer "+realKey {
		t.Fatalf("the model host saw Authorization %q", got)
	}
	if r.sysSaw[0].Get("X-Api-Key") != "" || strings.Contains(r.sysURLs[0], "placeholder") || !strings.Contains(r.sysURLs[0], "alt=sse") {
		t.Fatalf("the agent's own credentials reached the model host: %v %v", r.sysSaw[0], r.sysURLs[0])
	}
	if r.idle != 10*time.Minute {
		t.Fatalf("the relayed send's idle window = %v, want the 10 minutes the provider named", r.idle)
	}
	for _, a := range r.asked {
		if !strings.HasSuffix(a, "drive=own") {
			t.Fatalf("the provider was told %q", a)
		}
	}
	for _, n := range r.bodies {
		if n > 0 && !strings.Contains(strings.Join(r.asked, ""), "POST /egress/") {
			t.Fatal("unexpected body")
		}
	}
	if len(r.cards) != 0 {
		t.Fatalf("cards = %v", r.cards)
	}
}

// A request body never goes to the provider: only the request line and the
// headers (it decides, it does not read).
func TestLocalProviderNeverSeesTheBody(t *testing.T) {
	r := newLocalRig(t)
	r.turn(ownJob)
	var seen component.EgressRequest
	r.mu.Lock()
	r.answer = func(in component.EgressRequest) component.EgressResponse {
		seen = in
		return component.EgressResponse{Status: 403, Body: []byte(`{"error":"no"}`)}
	}
	r.mu.Unlock()
	resp, err := r.post(`{"secret prompt":"do not leak"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(seen.Body) != 0 {
		t.Fatalf("the provider got a body: %q", seen.Body)
	}
}

// A borrowed or platform run claims nothing from a provider on this
// computer, is never asked, and its request to the host goes out
// untouched (the placeholder is not the member's credential): the claim is
// refused even when a stale one is left in the session.
func TestLocalProviderIsNeverAskedForAnotherRun(t *testing.T) {
	r := newLocalRig(t)
	for _, job := range []Job{{JobID: "job-b", RequesterUserID: "jack", Drive: component.DriveBorrowed}, {JobID: "job-p", Drive: component.DrivePlatform}} {
		r.turn(job)
		r.mu.Lock()
		before := len(r.asked)
		r.mu.Unlock()
		if got := r.sess.EgressHosts(); len(got) != 0 {
			t.Fatalf("a %s run claimed %v", job.Drive, got)
		}
		// A claim left over from an own turn, with a run that is not own now:
		// the proxy refuses it rather than ask.
		r.sess.SetEgressHosts(map[string]string{modelHost: "local:acme"}, nil)
		resp, err := r.post(`{}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "does not serve this run") {
			t.Fatalf("%s run: %d %s", job.Drive, resp.StatusCode, b)
		}
		r.mu.Lock()
		if len(r.asked) != before || len(r.sysSaw) != 0 {
			t.Fatalf("%s run reached the provider or the host: %v", job.Drive, r.asked)
		}
		r.mu.Unlock()
		r.sess.Unbind(job.JobID)
	}
}

// Between turns a claimed name answers 503 and nothing is sent.
func TestLocalProviderClaimEndsWithTheTurn(t *testing.T) {
	r := newLocalRig(t)
	r.turn(ownJob)
	r.sess.Unbind(ownJob.JobID)
	resp, err := r.post(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp.StatusCode != http.StatusServiceUnavailable || len(r.sysSaw) != 0 {
		t.Fatalf("status %d, the host saw %d requests", resp.StatusCode, len(r.sysSaw))
	}
}

// A completion reaches the agent as the model produces it (not after 64 KiB),
// and the provider hears of it once, when it ended, with the byte count.
func TestRelayStreamsAndReportsOnce(t *testing.T) {
	r := newLocalRig(t)
	release := make(chan struct{})
	r.system = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	}
	r.turn(ownJob)
	resp, err := r.post(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len("data: one\n\n"))
	got := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, first); got <- err }()
	select {
	case err := <-got:
		if err != nil || string(first) != "data: one\n\n" {
			t.Fatalf("first chunk = %q %v", first, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not reach the agent while the model was still answering")
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(rest) != "data: two\n\n" {
		t.Fatalf("rest = %q", rest)
	}
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reports) == 1 })
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.reports[0]
	if u.Host != modelHost || u.Status != 200 || u.Bytes != int64(len("data: one\n\n")+len("data: two\n\n")) || u.Error != "" {
		t.Fatalf("usage = %+v", u)
	}
}

// An agent that drops the answer midway is still counted.
func TestRelayReportsADroppedAnswer(t *testing.T) {
	r := newLocalRig(t)
	done := make(chan struct{})
	r.system = func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-req.Context().Done():
		case <-done:
		}
	}
	defer close(done)
	r.turn(ownJob)
	resp, err := r.post(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_, _ = io.ReadFull(resp.Body, buf)
	resp.Body.Close() // the agent goes away
	r.client.CloseIdleConnections()
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reports) == 1 })
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reports[0].Error == "" {
		t.Fatalf("a dropped answer was reported as complete: %+v", r.reports[0])
	}
}

// A provider that echoes the credential in an error, or in a header, does
// not hand it to the agent.
func TestRelayScrubsTheCredentialFromAnswers(t *testing.T) {
	r := newLocalRig(t)
	r.system = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Echo", "you sent "+realKey)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid key `+realKey+`","auth":"`+req.Header.Get("Authorization")+`"}`)
	}
	r.turn(ownJob)
	resp, err := r.post(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || strings.Contains(string(b), realKey) || strings.Contains(resp.Header.Get("X-Echo"), realKey) {
		t.Fatalf("the credential came back: %d %s %v", resp.StatusCode, b, resp.Header)
	}
	if !strings.Contains(string(b), "[removed]") {
		t.Fatalf("body = %s", b)
	}
}

// An upgrade (Codex opens a WebSocket first) to a claimed host is answered 426
// here and never sent, so the client falls back at once and nothing carries
// the credential.
func TestUpgradeToAClaimedHostIsRefused(t *testing.T) {
	r := newLocalRig(t)
	r.turn(ownJob)
	resp, err := r.post(`{}`, map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp.StatusCode != http.StatusUpgradeRequired || len(r.sysSaw) != 0 {
		t.Fatalf("status %d, the host saw %d requests", resp.StatusCode, len(r.sysSaw))
	}
}

// A provider that has no key answers the request itself, with a card for the
// requester (once per job): the request is never sent without a credential.
func TestLocalProviderRefusalCarriesACard(t *testing.T) {
	r := newLocalRig(t)
	r.answer = func(component.EgressRequest) component.EgressResponse {
		return component.EgressResponse{Status: 401, Body: []byte(`{"error":"add your key"}`),
			Card: &component.Card{Title: "Add your model key", Actions: []component.CardAction{{Kind: component.CardOpen, Label: "Open", Page: "keys"}}}}
	}
	r.turn(ownJob)
	for i := 0; i < 2; i++ {
		resp, err := r.post(`{}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 401 || !strings.Contains(string(b), "add your key") {
			t.Fatalf("answer = %d %s", resp.StatusCode, b)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sysSaw) != 0 {
		t.Fatal("a request without a credential reached the model host")
	}
	if len(r.cards) != 1 || r.cards[0].Component != "acme" || r.cards[0].For != "maya" {
		t.Fatalf("cards = %+v", r.cards)
	}
}

// A provider that is gone: 502 with the reason, never an unauthenticated
// success.
func TestLocalProviderDownFailsClosed(t *testing.T) {
	r := newLocalRig(t)
	r.turn(ownJob)
	p := r.sess.local["local:acme"]
	p.url.Host = "127.0.0.1:1" // nothing listens
	resp, err := r.post(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "provider on this computer could not be reached") || len(r.sysSaw) != 0 {
		t.Fatalf("answer = %d %s, the host saw %d", resp.StatusCode, b, len(r.sysSaw))
	}
}

// The descriptor is untrusted: only a plain file of the contract with a
// loopback http address and a port is taken.
func TestLocalProviderDescriptorsAreChecked(t *testing.T) {
	dir := t.TempDir()
	r := &localRig{t: t}
	r.writeDescriptor(dir, "good", component.EgressProviderContract, "http://127.0.0.1:4821", "")
	r.writeDescriptor(dir, "v6", component.EgressProviderContract, "http://[::1]:4821/base/", "")
	r.writeDescriptor(dir, "localhost", component.EgressProviderContract, "http://localhost:4821", "")
	r.writeDescriptor(dir, "remote", component.EgressProviderContract, "http://10.0.0.5:4821", "")
	r.writeDescriptor(dir, "dns", component.EgressProviderContract, "http://example.com:80", "")
	r.writeDescriptor(dir, "tls", component.EgressProviderContract, "https://127.0.0.1:4821", "")
	r.writeDescriptor(dir, "noport", component.EgressProviderContract, "http://127.0.0.1", "")
	r.writeDescriptor(dir, "userinfo", component.EgressProviderContract, "http://u:p@127.0.0.1:1", "")
	r.writeDescriptor(dir, "oldcontract", "bnbloc.egress.provider/v0", "http://127.0.0.1:4821", "")
	r.writeDescriptor(dir, "Bad_Name", component.EgressProviderContract, "http://127.0.0.1:4821", "")
	got, dropped := ReadLocalProviders(dir)
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "good,localhost,v6" {
		t.Fatalf("taken = %v (dropped %v)", names, dropped)
	}
	if len(dropped) != 7 {
		t.Fatalf("dropped = %v", dropped)
	}
	if p, _ := ReadLocalProviders(filepath.Join(dir, "missing")); p != nil {
		t.Fatal("a missing folder gave providers")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
