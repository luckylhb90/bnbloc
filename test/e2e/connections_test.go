package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
	activitysvc "github.com/luckylhb90/bnbloc/services/activity"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
	gatewaysvc "github.com/luckylhb90/bnbloc/services/gateway"
	realtimesvc "github.com/luckylhb90/bnbloc/services/realtime"
)

// TestE2EConnections runs the connections component (the built binary of
// github.com/luckylhb90/bnbloc-connections, BNBLOC_E2E_CONNECTIONS_BIN)
// beside an in-process gateway and drives it the way an agent does: an
// admin adds and enables a connector on its page, maya connects it with a
// key on hers, and curl, through the room tools server's egress proxy for
// maya's own job, reaches the system as maya (httpbin.org, which echoes the
// bearer it got): a read goes through with maya's key in place of the one
// the agent sent; a system not connected answers 403 and puts a card in
// the room for maya; a change is held (202) until maya allows it on its
// page; another host is tunnelled untouched; the room facts name the
// systems. Needs the network.
func TestE2EConnections(t *testing.T) {
	bin := os.Getenv("BNBLOC_E2E_CONNECTIONS_BIN")
	if bin == "" {
		t.Skip("set BNBLOC_E2E_CONNECTIONS_BIN to a bnbloc-connections binary")
	}
	// The control plane.
	rooms := store.NewMemoryRoom()
	members := staticMembers{rooms}
	hub := realtime.NewHub(256, 64)
	realtimeSrv := httptest.NewServer((&realtimesvc.Service{Hub: hub, Membership: members}).Handler())
	defer realtimeSrv.Close()
	activity := &activitysvc.Service{Store: store.NewMemory(), Publisher: realtime.HubPublisher{Hub: hub}, Membership: members}
	var mu sync.Mutex
	var notices []string
	activitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), wire.NoticeComponentCard) {
			mu.Lock()
			notices = append(notices, string(body))
			mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		activity.Handler().ServeHTTP(w, r)
	}))
	defer activitySrv.Close()
	roomSrv := httptest.NewServer(newRoomHandler(rooms))
	defer roomSrv.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	jobs := store.NewMemoryBridgeJobs()
	gw := &gatewaysvc.Service{
		Membership: members, Verifier: devVerifier{}, Signer: auth.NewSigner([]byte("e2e-key")),
		Backends: gatewaysvc.Backends{Room: roomSrv.URL, Activity: activitySrv.URL, Realtime: realtimeSrv.URL},
		Rooms: func(subject, _ string) []string {
			if m, _ := rooms.Members(tctx(), "room-1"); len(m) > 0 {
				for _, x := range m {
					if x.UserID == subject {
						return []string{"room-1"}
					}
				}
			}
			return nil
		},
		RoomTenant: func(string) string { return tenant.Default },
		TenantResolver: func(id auth.Identity, _ string) (string, string, error) {
			switch id.Subject {
			case "root":
				return tenant.Default, "super_admin", nil
			case "alice":
				return tenant.Default, "org_admin", nil
			}
			return tenant.Default, "member", nil
		},
		PlatformSettings: store.NewMemoryPlatformSettings(), SecretBox: box, AllowExtUnsigned: true,
		ContentJobs: jobs, ComponentValues: store.NewMemoryComponentValues(),
	}
	gwSrv := httptest.NewServer(gw.Handler())
	defer gwSrv.Close()
	h := &harness{gateway: gwSrv, rooms: rooms}
	_ = rooms.CreateRoom(tctx(), store.Room{RoomID: "room-1", Name: "Release"})
	_ = rooms.AddMember(tctx(), store.Membership{RoomID: "room-1", UserID: "maya", Role: store.RoleOwner})
	_ = rooms.AddMember(tctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleMember})
	root, alice, maya := h.login(t, "root"), h.login(t, "alice"), h.login(t, "maya")

	// The connections component, installed like any service.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	stop := startConnections(t, bin, addr, gwSrv.URL, "not-yet")
	pkg, _ := json.Marshal(map[string]any{
		"kind": "bnbloc.service.package/v1", "name": "connections", "version": "0.1.0", "publisher": "e2e",
		"runtime": "endpoint", "contract": component.ServiceContractV1, "capabilities": component.ServiceCapabilities,
		"endpoint": map[string]string{"baseUrl": "http://" + addr}, "permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	raw, code := call(t, root, http.MethodPost, gwSrv.URL+"/v1/platform/components", "application/json",
		mustJSON(wire.ComponentInstallRequest{Package: string(pkg), Rollout: "auto"}))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if code != http.StatusOK || installed.Secret == "" {
		t.Fatalf("install = %d %s", code, raw)
	}
	stop()
	startConnections(t, bin, addr, gwSrv.URL, installed.Secret)

	// The admin adds and enables two connectors on the Connectors page.
	adminPage := openPage(t, alice, gwSrv.URL, "admin", "")
	for _, c := range []map[string]any{
		{"name": "httpbin", "displayName": "HTTPBin", "purpose": "An echo of the request", "hosts": []string{"httpbin.org"},
			"connect": map[string]any{"kind": "apiKey", "apiKey": map[string]string{"in": "bearer"}},
			"policy":  map[string]any{"read": []map[string]any{{"paths": []string{"/**"}}}, "write": []map[string]any{{"methods": []string{"POST"}, "paths": []string{"/post"}}}},
			"guide":   "curl https://httpbin.org/bearer"},
		{"name": "echo", "displayName": "Echo", "purpose": "Another echo", "hosts": []string{"postman-echo.com"},
			"connect": map[string]any{"kind": "apiKey", "apiKey": map[string]string{"in": "bearer"}},
			"policy":  map[string]any{"read": []map[string]any{{"paths": []string{"/**"}}}}},
	} {
		if code := postForm(t, adminPage+"save", url.Values{"json": {string(mustJSON(c))}}); code != http.StatusSeeOther {
			t.Fatalf("save connector = %d", code)
		}
		if code := postForm(t, adminPage+"toggle", url.Values{"name": {c["name"].(string)}, "enabled": {"1"}}); code != http.StatusSeeOther {
			t.Fatalf("enable connector = %d", code)
		}
	}
	// Maya connects HTTPBin with her key (Echo stays unconnected).
	mayaPage := openPage(t, maya, gwSrv.URL, "connections", "system=httpbin")
	if code := postForm(t, mayaPage+"connect", url.Values{"system": {"httpbin"}, "key": {"maya-e2e-key"}}); code != http.StatusSeeOther {
		t.Fatalf("connect = %d", code)
	}

	// Maya's own agent's turn: a job leased to her bridge.
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	_ = jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-maya", OwnerUserID: "maya", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()})
	if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "main", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "maya",
		Provider: "codex", Status: store.AgentJobRunning, LeaseBridgeID: "br-maya", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	gwc := gatewayClient{base: gwSrv.URL, token: maya}
	srv, err := roomtools.Start(roomtools.Config{Gateway: gwc, EgressDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	token, sess, _ := srv.Open(roomtools.Scope{RoomID: "room-1", Owner: "maya", TenantID: tenant.Default, Provider: "codex"})
	sess.Bind(roomtools.Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "main", MessageSeq: 1})
	var hosts component.EgressHosts
	raw, err = gwc.Do(context.Background(), http.MethodGet, component.TurnProxyPath+"connections/egress/hosts?room=room-1&job=job-1", nil)
	if err != nil || json.Unmarshal(raw, &hosts) != nil {
		t.Fatalf("hosts = %s %v", raw, err)
	}
	claimed := map[string]string{}
	for _, host := range hosts.Hosts {
		claimed[host] = "connections"
	}
	if claimed["httpbin.org"] == "" || claimed["postman-echo.com"] == "" {
		t.Fatalf("hosts = %+v", hosts)
	}
	sess.SetEgressHosts(claimed, nil)
	env := srv.EgressEnv(token, "")
	curl := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command("curl", append([]string{"-sS", "-m", "60", "-o", "-", "-w", "\n%{http_code}"}, args...)...)
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("curl %v: %v\n%s", args, err, out)
		}
		s := strings.TrimRight(string(out), "\n")
		i := strings.LastIndexByte(s, '\n')
		var status int
		_, _ = fmt.Sscanf(s[i+1:], "%d", &status)
		return status, s[:i+1]
	}

	// A read: maya's key, not the agent's.
	if status, body := curl("-H", "Authorization: Bearer agent-made-up", "https://httpbin.org/bearer"); status != 200 || !strings.Contains(body, `"maya-e2e-key"`) {
		t.Fatalf("read = %d %s", status, body)
	}
	// Not connected: 403 and a card for maya.
	if status, body := curl("https://postman-echo.com/get"); status != http.StatusForbidden || !strings.Contains(body, "not connected") {
		t.Fatalf("not connected = %d %s", status, body)
	}
	mu.Lock()
	cards := len(notices)
	mu.Unlock()
	if cards != 1 || !strings.Contains(notices[0], `"for":"maya"`) || !strings.Contains(notices[0], "Connect Echo") {
		t.Fatalf("cards = %v", notices)
	}
	// A change: held until maya allows it.
	status, body := curl("-X", "POST", "-H", "Content-Type: application/json", "-d", `{"title":"ship it"}`, "https://httpbin.org/post")
	var held struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &held)
	if status != http.StatusAccepted || held.Status != "held" || held.ID == "" {
		t.Fatalf("change = %d %s", status, body)
	}
	approve := openPage(t, maya, gwSrv.URL, "approve", "id="+held.ID)
	if code, page := getPage(t, approve+"?id="+held.ID); code != 200 || !strings.Contains(page, "ship it") {
		t.Fatalf("review = %d %s", code, page)
	}
	code, page := postFormBody(t, approve+"decide", url.Values{"id": {held.ID}, "decision": {"allow"}})
	if code != 200 || !strings.Contains(page, "Done") || !strings.Contains(page, "ship it") {
		t.Fatalf("allow = %d %s", code, page)
	}
	// Another host: tunnelled, never decrypted.
	if status, _ := curl("-o", "/dev/null", "https://example.com/"); status != 200 {
		t.Fatalf("tunnel = %d", status)
	}
	// The facts: the line and the files.
	var facts component.Facts
	raw, _ = gwc.Do(context.Background(), http.MethodGet, component.TurnProxyPath+"connections/facts?room=room-1&job=job-1", nil)
	_ = json.Unmarshal(raw, &facts)
	if !strings.Contains(facts.Line, "httpbin") || !strings.Contains(facts.Files["INDEX.md"], "| HTTPBin") || !strings.Contains(facts.Files["ACTIVITY.md"], "httpbin POST /post") {
		t.Fatalf("facts = %+v", facts)
	}
	// The admin switches HTTPBin to go out from the members' computers: the
	// same read now leaves through this bridge, still with maya's key.
	if code := postForm(t, adminPage+"sendfrom", url.Values{"name": {"httpbin"}, "sendFrom": {"member"}}); code != http.StatusSeeOther {
		t.Fatalf("send from members = %d", code)
	}
	if status, body := curl("-H", "Authorization: Bearer agent-made-up", "https://httpbin.org/bearer"); status != 200 || !strings.Contains(body, `"maya-e2e-key"`) {
		t.Fatalf("read from this computer = %d %s", status, body)
	}
	if sb := os.Getenv("BNBLOC_E2E_SANDBOX_URL"); sb != "" {
		t.Run("sandboxed", func(t *testing.T) {
			e2eSandboxed(t, gwc, claimed, core.Endpoint{BaseURL: sb, Token: os.Getenv("BNBLOC_E2E_SANDBOX_TOKEN")})
		})
	}
}

// e2eSandboxed: a guest's curl, in a machine created with the proxy's host
// port (hostProxy), reaches the connected system as maya through the
// proxy-only listener with an egress alias, and its tunnels reach no
// private address.
func e2eSandboxed(t *testing.T, gwc gatewayClient, claimed map[string]string, ep core.Endpoint) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	srv, err := roomtools.Start(roomtools.Config{Gateway: gwc, EgressDir: t.TempDir(), ProxyAddr: fmt.Sprintf("127.0.0.1:%d", port)})
	if err != nil || !srv.ProxyListening() {
		t.Fatalf("proxy listener: %v", err)
	}
	defer srv.Close()
	_, sess, _ := srv.Open(roomtools.Scope{RoomID: "room-1", Owner: "maya", TenantID: tenant.Default, Provider: "codex"})
	sess.Bind(roomtools.Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "main", MessageSeq: 1})
	sess.SetEgressHosts(claimed, nil)
	alias := strings.Repeat("e2ealias", 6)
	if err := srv.AliasEgress(alias, sess); err != nil {
		t.Fatal(err)
	}
	client := sandbox.NewClient(nil)
	ctx := context.Background()
	disc, err := client.Discover(ctx, ep)
	if err != nil || !sandbox.HasCapability(disc, sandbox.CapabilityHostProxy) {
		t.Skipf("the sandbox has no hostProxy (%v)", err)
	}
	caPEM, _ := srv.EgressCA()
	if _, err := client.PullImage(ctx, ep, "alpine:3.20"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	m, err := client.CreateMachine(ctx, ep, sandbox.CreateMachineRequest{
		Name: "e2e-connections-" + strconv.FormatInt(time.Now().UnixNano(), 36), Image: "alpine:3.20", MemoryMiB: 512,
		Egress:    &sandbox.Egress{Mode: sandbox.EgressPublic},
		HostPorts: []int{port},
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	defer client.DeleteMachine(ctx, ep, m.ID)
	run := func(script string) (int, string) {
		t.Helper()
		res, err := client.Run(ctx, ep, m.ID, sandbox.ExecRequest{Command: []string{"sh", "-c", script}, TimeoutMs: 180_000})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		o, _ := base64.StdEncoding.DecodeString(res.StdoutBase64)
		e, _ := base64.StdEncoding.DecodeString(res.StderrBase64)
		return res.ExitCode, string(o) + string(e)
	}
	if code, out := run("apk add -q --no-progress curl ca-certificates >/dev/null 2>&1; command -v curl"); code != 0 {
		t.Fatalf("curl in the guest: %d %s", code, out)
	}
	// The bundle the bridge copies into a sandboxed workspace: the guest's
	// roots and the local CA.
	if code, out := run("mkdir -p /ca && printf '%s' '" + string(caPEM) + "' > /ca/ca.pem && cat /etc/ssl/certs/ca-certificates.crt /ca/ca.pem > /ca/bundle.pem"); code != 0 {
		t.Fatalf("bundle: %d %s", code, out)
	}
	proxy := fmt.Sprintf("http://bnbloc:%s@%s:%d", alias, disc.HostAlias, port)
	env := "HTTPS_PROXY=" + proxy + " CURL_CA_BUNDLE=/ca/bundle.pem"
	if code, out := run(env + " curl -sS -m 60 -H 'Authorization: Bearer agent-made-up' https://httpbin.org/bearer"); code != 0 || !strings.Contains(out, `"maya-e2e-key"`) {
		t.Fatalf("guest read = %d %s", code, out)
	}
	if code, out := run(env + " curl -sS -m 20 -o /dev/null -w '%{http_code}' https://example.com/"); code != 0 || out != "200" {
		t.Fatalf("guest tunnel = %d %s", code, out)
	}
	if code, out := run(env + " curl -sS -m 20 -o /dev/null https://10.255.255.1/ 2>&1; echo exit=$?"); !strings.Contains(out, "502") && !strings.Contains(out, "exit=56") {
		t.Fatalf("a private address through the proxy = %d %s, want refused", code, out)
	}
	if code, out := run("curl -sS -m 5 -o /dev/null -w '%{http_code}' http://" + disc.HostAlias + ":1/ 2>&1; echo"); strings.HasPrefix(out, "200") {
		t.Fatalf("another host port is reachable: %d %s", code, out)
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func call(t *testing.T, token, method, u, contentType string, body []byte) ([]byte, int) {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode
}

// openPage opens a service page as the member and returns its address.
func openPage(t *testing.T, token, base, page, query string) string {
	t.Helper()
	raw, code := call(t, token, http.MethodPost, base+"/v1/my/component-pages/connections/"+page, "application/json", mustJSON(wire.ComponentPageOpen{Query: query}))
	var s wire.ComponentPageSession
	_ = json.Unmarshal(raw, &s)
	if code != http.StatusOK || s.URL == "" {
		t.Fatalf("open %s = %d %s", page, code, raw)
	}
	u, _ := url.Parse(s.URL)
	u.RawQuery = ""
	return u.String()
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func postFormBody(t *testing.T, u string, form url.Values) (int, string) {
	t.Helper()
	resp, err := noRedirect.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "err=1") {
		t.Logf("%s answered %s", u, loc)
	}
	return resp.StatusCode, string(raw)
}

func postForm(t *testing.T, u string, form url.Values) int {
	t.Helper()
	code, _ := postFormBody(t, u, form)
	return code
}

func getPage(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// startConnections runs the component and waits for its discover.
func startConnections(t *testing.T, bin, addr, gateway, secret string) (stop func()) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "BNBLOC_CONNECTIONS_LISTEN="+addr, "BNBLOC_CONNECTIONS_BNBLOC_URL="+gateway, "BNBLOC_CONNECTIONS_SECRET="+secret)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop = func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	t.Cleanup(stop)
	for i := 0; i < 100; i++ {
		if resp, err := http.Get("http://" + addr + component.ServiceDiscoverPath); err == nil {
			_ = resp.Body.Close()
			return stop
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("connections did not start")
	return stop
}

// gatewayClient is the bridge's gateway client: the member's session.
type gatewayClient struct{ base, token string }

func (g gatewayClient) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(mustJSON(body))
	}
	req, _ := http.NewRequestWithContext(ctx, method, g.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &roomtools.StatusError{Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}
