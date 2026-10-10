package realtimehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func scrapeMetrics(m *obs.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// waitMetric polls the registry until want appears (SSE open/close runs on
// the server goroutine).
func waitMetric(t *testing.T, m *obs.Metrics, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		body := scrapeMetrics(m)
		if strings.Contains(body, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Open room streams are counted by kind (presence-counting member vs
// ?presence=0 bridge) while open, and published events by kind (unknown
// kinds as "other").
func TestSSEConnectionAndPublishMetrics(t *testing.T) {
	m := obs.NewMetrics("bnbloc-realtime")
	realtime.RegisterMetrics(m)
	hub := realtime.NewHub(64, 16)
	hub.Metrics = m
	mem := membership.NewStatic()
	mem.Add("room-1", "bob")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem}).Handler())
	defer srv.Close()

	open := func(query string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events"+query, nil)
		req.Header.Set("X-Bnbloc-User", "bob")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("subscribe %s: %v %v", query, resp, err)
		}
		return resp
	}
	member := open("")
	bridge := open("?presence=0")
	waitMetric(t, m, `bnbloc_realtime_sse_connections{service="bnbloc-realtime",kind="member"} 1`)
	waitMetric(t, m, `bnbloc_realtime_sse_connections{service="bnbloc-realtime",kind="bridge"} 1`)

	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.KindChatMessage})
	hub.Publish(wire.Event{RoomID: "room-1", Kind: "made_up_kind"})
	waitMetric(t, m, `bnbloc_realtime_events_published_total{service="bnbloc-realtime",kind="chat_message"} 1`)
	waitMetric(t, m, `bnbloc_realtime_events_published_total{service="bnbloc-realtime",kind="other"} 1`)

	member.Body.Close()
	bridge.Body.Close()
	waitMetric(t, m, `bnbloc_realtime_sse_connections{service="bnbloc-realtime",kind="member"} 0`)
	waitMetric(t, m, `bnbloc_realtime_sse_connections{service="bnbloc-realtime",kind="bridge"} 0`)
}
