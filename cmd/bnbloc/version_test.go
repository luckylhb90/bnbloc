package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func consistentVersions(commit string) *wire.DeploymentVersions {
	return &wire.DeploymentVersions{
		Services:   []wire.ServiceVersion{{Service: "bnbloc-gateway", Instance: "gw-1", Commit: commit}, {Service: "bnbloc-room", Instance: "room-1", Commit: commit}},
		Web:        &wire.WebBundleVersion{Version: "0.1.14", Commit: commit},
		Consistent: true,
	}
}

func TestSmokeVersionsPassAndSkew(t *testing.T) {
	g := newFakeSmokeGateway(t)
	g.versions = consistentVersions("abc1234")
	out, err := runFakeSmoke(t, g, smokeTestOptions())
	if err != nil || !strings.Contains(out, "PASS versions (") || !strings.Contains(out, "abc1234 on 2 instance(s)") {
		t.Fatalf("consistent deployment: err=%v\n%s", err, out)
	}

	g = newFakeSmokeGateway(t)
	g.versions = consistentVersions("abc1234")
	g.versions.Services = append(g.versions.Services, wire.ServiceVersion{Service: "bnbloc-room", Instance: "room-2", Commit: "0ld0ld0"})
	out, err = runFakeSmoke(t, g, smokeTestOptions())
	if err == nil || !strings.Contains(out, "FAIL versions: commit skew") || !strings.Contains(out, "PASS history (") {
		t.Fatalf("skew must fail the run but not stop the message-path checks: err=%v\n%s", err, out)
	}
}

func TestFetchDeploymentVersionsMergesSamples(t *testing.T) {
	g := newFakeSmokeGateway(t)
	g.versions = consistentVersions("abc1234")
	isolateHome(t)
	c := newClient(config{Gateway: g.URL, Token: testSession("root", nil)})
	d, err := fetchDeploymentVersions(context.Background(), c, 3)
	if err != nil || !d.Consistent || len(d.Services) != 2 {
		t.Fatalf("merged = %+v, err %v", d, err)
	}
	var buf bytes.Buffer
	printDeploymentVersions(&buf, g.URL, d)
	if !strings.Contains(buf.String(), "consistent") || !strings.Contains(buf.String(), "room-1") {
		t.Fatalf("table:\n%s", buf.String())
	}

	g.versions = nil
	if _, err := fetchDeploymentVersions(context.Background(), c, 1); err != errNotSuperAdmin {
		t.Fatalf("403 must map to errNotSuperAdmin, got %v", err)
	}
}
