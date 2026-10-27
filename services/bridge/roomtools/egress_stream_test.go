package roomtools

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
)

// A send from this computer streams (docs/prd/2026-10-27-streamed-member-sends.md):
// a 20 MB answer goes to the command after the service accepted its first
// 64 KiB; a 20 MB request body goes to a service that says it takes one
// without its body, and to the system once, as the command streams it. A
// service that does not say so gets 413 and the system nothing. The
// service's own answer to a report replaces the system's; a second send
// of a streamed body is refused here, whatever the service asks.
func TestEgressStreamsFromThisComputer(t *testing.T) {
	const big = 20 << 20
	var mu sync.Mutex
	var received []int
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic member" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		n, _ := io.Copy(io.Discard, r.Body)
		mu.Lock()
		received = append(received, int(n))
		mu.Unlock()
		if r.URL.Path == "/repo.git/big" {
			_, _ = w.Write(bytes.Repeat([]byte("p"), big))
			return
		}
		_, _ = io.WriteString(w, "got "+strconv.Itoa(int(n)))
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}}
	defer func() { hereClient = old }()

	var envelopes []component.EgressRequest
	answer := "accept" // what the service answers a report
	send := &component.EgressSend{Remove: []string{"Authorization"}, Header: map[string][]string{"Authorization": {"Basic member"}}}
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		envelopes = append(envelopes, in)
		mu.Unlock()
		switch {
		case in.Report == nil:
			return http.StatusOK, component.EgressResponse{Send: send}
		case answer == "refuse":
			return http.StatusOK, component.EgressResponse{Status: http.StatusForbidden, Body: []byte(`{"error":"connect again"}`)}
		case answer == "again":
			return http.StatusOK, component.EgressResponse{Send: send}
		}
		if len(in.Report.Body) != min(len(in.Report.Body), component.MaxEgressReportBody) {
			t.Errorf("report body = %d bytes", len(in.Report.Body))
		}
		return http.StatusOK, component.EgressResponse{Accept: true}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"git.example.com": "connections"}, map[string]bool{"connections": true})
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	client := proxyClient(t, srv, token, nil)
	client.Timeout = 0

	resp, err := client.Get("https://git.example.com/repo.git/big")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || n != big || len(envelopes) != 2 || len(envelopes[1].Report.Body) != component.MaxEgressReportBody {
		t.Fatalf("large answer = %d, %d bytes, envelopes %d", resp.StatusCode, n, len(envelopes))
	}

	push := func() (*http.Response, error) {
		// An io.Reader of unknown length: sent chunked, as git sends a pack.
		req, _ := http.NewRequest(http.MethodPost, "https://git.example.com/repo.git/git-receive-pack", io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("k"), big))))
		req.Header.Set("Authorization", "Basic agent-made-up")
		req.Header.Set("Expect", "100-continue")
		return client.Do(req)
	}
	envelopes = nil
	resp, err = push()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(raw) != "got "+strconv.Itoa(big) {
		t.Fatalf("large request = %d %q", resp.StatusCode, raw)
	}
	if len(envelopes) != 2 || !envelopes[0].Streamed || len(envelopes[0].Body) != 0 {
		t.Fatalf("the service got %+v (streamed, no body)", envelopes[0])
	}

	// The service's own answer to the report replaces the system's.
	answer = "refuse"
	resp, err = push()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || string(raw) != `{"error":"connect again"}` {
		t.Fatalf("refused after the report = %d %q", resp.StatusCode, raw)
	}

	// A second send of a streamed body: refused here.
	answer = "again"
	mu.Lock()
	sentBefore := len(received)
	mu.Unlock()
	resp, err = push()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	mu.Lock()
	sent := len(received) - sentBefore
	mu.Unlock()
	if resp.StatusCode != http.StatusBadGateway || !bytes.Contains(raw, []byte("streamed once")) || sent != 1 {
		t.Fatalf("a second send = %d %q (system got %d)", resp.StatusCode, raw, sent)
	}

	// A service that does not take a streamed body: 413, nothing sent.
	sess.SetEgressHosts(map[string]string{"git.example.com": "connections"}, nil)
	envelopes = nil
	mu.Lock()
	sentBefore = len(received)
	mu.Unlock()
	if resp, err := push(); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("not streaming = %d", resp.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(envelopes) != 0 || len(received) != sentBefore {
		t.Fatalf("not streaming: the service got %d, the system %d", len(envelopes), len(received)-sentBefore)
	}
}
