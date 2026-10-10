package activity

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// An accepted heartbeat is counted, and the sampler publishes how many
// bridges are online in the shared store.
func TestBridgeHeartbeatMetrics(t *testing.T) {
	m := obs.NewMetrics("bnbloc-activity")
	RegisterMetrics(m)
	svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: membership.NewStatic(), BridgeJobs: store.NewMemoryBridgeJobs(), Metrics: m}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	scrape := func() string {
		rec := httptest.NewRecorder()
		m.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}
	if body := scrape(); !strings.Contains(body, `bnbloc_bridges_online{service="bnbloc-activity"} 0`) ||
		!strings.Contains(body, `bnbloc_bridge_heartbeats_total{service="bnbloc-activity"} 0`) {
		t.Fatalf("series not registered at zero:\n%s", body)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/bridge/heartbeat", bytes.NewReader([]byte(`{"bridgeId":"b1"}`)))
	req.Header.Set("X-Bnbloc-User", "maya")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %v %v", resp, err)
	}
	resp.Body.Close()
	svc.SampleBridgeMetrics(context.Background())
	body := scrape()
	for _, want := range []string{
		`bnbloc_bridge_heartbeats_total{service="bnbloc-activity"} 1`,
		`bnbloc_bridges_online{service="bnbloc-activity"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}
