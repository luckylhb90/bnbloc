package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// serviceGateway answers the turn services' routes: one service "conn"
// with egress and room facts, recording the jobs it was asked for.
func serviceGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/my/turn-services":
			_ = json.NewEncoder(w).Encode(wire.TurnServiceList{Services: []wire.TurnService{
				{Name: "conn", Capabilities: []string{component.ServiceTurnProxy, component.ServiceEgress, component.ServiceRoomFacts}}}})
		case r.URL.Path == "/v1/turn-proxy/conn/egress/hosts":
			mu.Lock()
			asked = append(asked, "hosts:"+r.URL.Query().Get("job"))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(component.EgressHosts{Hosts: []string{"wiki.example.com", "BAD.example.com"}, Env: map[string]string{"WIKI_TOKEN": "connected", "HTTPS_PROXY": "x"}})
		case r.URL.Path == "/v1/turn-proxy/conn/facts":
			mu.Lock()
			asked = append(asked, "facts:"+r.URL.Query().Get("job"))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(component.Facts{Line: "systems you can reach as maya: wiki (INDEX.md)", Files: map[string]string{"INDEX.md": "# Systems", "wiki.md": "curl https://wiki.example.com/"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), asked...) }
}

// A session of an organization with an egress service gets the proxy and
// CA environment and the service's placeholders (never over the proxy's
// own), its hosts asked for the session's job and again at every turn; a
// sandboxed session gets none of it.
func TestEgressServiceGivesTheSessionItsProxy(t *testing.T) {
	gw, asked := serviceGateway(t)
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: t.TempDir()})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := b.sessionContributors()[0]
	info := SessionInfo{RoomID: "room-1", Owner: "maya", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "maya"}
	part, err := c.ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(part.Env["HTTPS_PROXY"], "http://bnbloc:") || part.Env["WIKI_TOKEN"] != "connected" || part.Env["NODE_EXTRA_CA_CERTS"] == "" || part.Env["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatalf("env = %v", part.Env)
	}
	var g roomToolsGrant
	if json.Unmarshal(part.State, &g) != nil || !g.Egress {
		t.Fatalf("state = %s (egress kept for a restore)", part.State)
	}
	part.Turn(TurnInfo{JobID: "job-2", RequesterUserID: "maya"})
	if got := strings.Join(asked(), ","); got != "hosts:job-1,hosts:job-2" {
		t.Fatalf("asked = %s", got)
	}
	info.Sandboxed = true
	if sp, err := c.ContributeSession(context.Background(), info); err != nil || sp.Env != nil {
		t.Fatalf("sandboxed env = %v, %v", sp.Env, err)
	}
}

// A roomFacts service's line joins the guidance's list of records and its
// files are written under .room/<service>/; the job is asked once.
func TestServiceFactsLineAndFiles(t *testing.T) {
	gw, asked := serviceGateway(t)
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya"})
	facts := b.jobFacts(context.Background(), "room-1", "job-1")
	_ = b.jobFacts(context.Background(), "room-1", "job-1")
	if got := strings.Join(asked(), ","); got != "facts:job-1" {
		t.Fatalf("asked = %s", got)
	}
	g := roomlog.SessionGuidance(roomlog.Guidance{HasRoomFiles: true, Services: serviceFactLines(facts)})
	if !strings.Contains(g, "- conn/: systems you can reach as maya: wiki (INDEX.md)") {
		t.Fatalf("guidance = %s", g)
	}
	files := serviceFactFiles(facts)
	if len(files) != 2 || files[0].Name != ".room/conn/INDEX.md" || files[1].Name != ".room/conn/wiki.md" {
		t.Fatalf("files = %+v", files)
	}
	if _, ok := roomlog.ServiceDir("deliverables"); ok {
		t.Fatal("a service named like a record gets its directory")
	}
	unreadable := roomlog.SessionGuidance(roomlog.Guidance{HasRoomFiles: true, Services: []roomlog.ServiceLine{{Service: "conn", Unreadable: true}}})
	if !strings.Contains(unreadable, "- conn/: could not be read this time.") {
		t.Fatalf("unreadable = %s", unreadable)
	}
}

// A sandboxed session's CA files are in its workspace when the session
// starts and at every turn: a fresh session resets the workspace's
// bnbloc-owned paths (.room/ included) after the space was prepared, so
// the files the space's commands are pointed at are written again.
func TestSandboxedSessionKeepsItsCAFiles(t *testing.T) {
	gw, _ := serviceGateway(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: t.TempDir(),
		Sandbox: SandboxConfig{HostProxy: fmt.Sprintf("host.vm.internal:%d", port)}})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	workdir := t.TempDir()
	ca := filepath.Join(workdir, ".room", "egress", "ca.pem")
	// The space's commands get what a host session's do: the services'
	// placeholders for the turn's job, never over the proxy's own.
	if env := b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-1"); env["HTTPS_PROXY"] == "" || env["HTTPS_PROXY"] == "x" || env["NODE_EXTRA_CA_CERTS"] != ca || env["WIKI_TOKEN"] != "connected" {
		t.Fatalf("space env = %v", env)
	}
	resetProtectedPaths(workdir)
	if _, err := os.Stat(ca); err == nil {
		t.Fatal("the reset kept .room/ (the test needs it gone)")
	}
	part, err := b.sessionContributors()[0].ContributeSession(context.Background(), SessionInfo{RoomID: "room-1", Owner: "maya", JobID: "job-1", RequesterUserID: "maya", Sandboxed: true, Workdir: workdir})
	if err != nil || part.Env != nil {
		t.Fatalf("contribution = %v %v (a sandboxed agent's own process gets nothing)", part.Env, err)
	}
	if _, err := os.Stat(ca); err != nil {
		t.Fatalf("the CA is not back at the session's start: %v", err)
	}
	_ = os.RemoveAll(filepath.Join(workdir, ".room"))
	part.Turn(TurnInfo{JobID: "job-2", RequesterUserID: "maya"})
	if _, err := os.Stat(ca); err != nil {
		t.Fatalf("the CA is not back at the turn: %v", err)
	}
}
