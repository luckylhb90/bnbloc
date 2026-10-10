package buildinfo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestGetPrefersLinkStamp(t *testing.T) {
	old, oldBuilt := Commit, BuiltAt
	t.Cleanup(func() { Commit, BuiltAt = old, oldBuilt })
	Commit, BuiltAt = "abc123def456", "2026-10-08T00:00:00Z"
	v := Get("bnbloc-room")
	if v.Service != "bnbloc-room" || v.Commit != "abc123def456" || v.BuiltAt != "2026-10-08T00:00:00Z" || v.Instance == "" {
		t.Fatalf("Get = %+v", v)
	}
}

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler("bnbloc-relay").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	var v wire.ServiceVersion
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Service != "bnbloc-relay" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	Handler("x").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/version", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d", rec.Code)
	}
}

func TestEvaluate(t *testing.T) {
	web := &wire.WebBundleVersion{Version: "0.1.14", Commit: "abc1234"}
	d := wire.DeploymentVersions{
		Services: []wire.ServiceVersion{
			{Service: "bnbloc-room", Commit: "abc1234def0", Instance: "room-1"},
			{Service: "bnbloc-gateway", Commit: "abc1234def0123", Instance: "gw-1"},
		},
		Web: web,
	}
	Evaluate(&d)
	if !d.Consistent || len(d.Problems) != 0 {
		t.Fatalf("short/full hashes of one commit must be consistent: %+v", d)
	}
	if d.Services[0].Service != "bnbloc-gateway" {
		t.Fatalf("services not sorted: %+v", d.Services)
	}

	d.Services = append(d.Services, wire.ServiceVersion{Service: "bnbloc-room", Commit: "ffff000", Instance: "room-2"})
	Evaluate(&d)
	if d.Consistent || len(d.Problems) != 1 || !strings.Contains(d.Problems[0], "commit skew") || !strings.Contains(d.Problems[0], "bnbloc-room@room-2") {
		t.Fatalf("skew not reported: %+v", d)
	}

	d = wire.DeploymentVersions{
		Services:    []wire.ServiceVersion{{Service: "bnbloc-room", Instance: "r"}},
		Unreachable: []wire.UnreachableService{{Service: "bnbloc-relay", Error: "connection refused"}},
	}
	Evaluate(&d)
	if d.Consistent || len(d.Problems) != 2 || len(d.Warnings) != 1 {
		t.Fatalf("missing web and unreachable are problems, an unknown commit a warning: %+v", d)
	}

	// Images built without the commit stamp are unverifiable, not skewed.
	d = wire.DeploymentVersions{
		Services: []wire.ServiceVersion{{Service: "bnbloc-room", Instance: "r"}, {Service: "bnbloc-gateway", Instance: "g"}},
		Web:      &wire.WebBundleVersion{Version: "0.1.15"},
	}
	Evaluate(&d)
	if !d.Consistent || len(d.Warnings) != 3 {
		t.Fatalf("unstamped builds must warn without failing: %+v", d)
	}
}
