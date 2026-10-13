package bridge

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// borrowGateway is an activityGateway (real activity dedup) that also grants
// every borrow reservation (capturing its body) and captures every settle
// (record) body, noting how many replies were stored when it arrived.
type borrowGateway struct {
	*activityGateway
	srv *httptest.Server

	mu            sync.Mutex
	reserves      []map[string]any
	records       []map[string]any
	storedAtCalls []int
}

func newBorrowGateway(t *testing.T, room string) *borrowGateway {
	t.Helper()
	g := &borrowGateway{activityGateway: newActivityGateway(t, room)}
	inner := g.activityGateway.srv.Config.Handler
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/authorization/record"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, fresh := g.stored()
			g.mu.Lock()
			g.records = append(g.records, body)
			g.storedAtCalls = append(g.storedAtCalls, len(fresh))
			g.mu.Unlock()
			_, _ = w.Write([]byte(`{"recorded":true}`))
		case strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, fresh := g.stored()
			g.mu.Lock()
			g.reserves = append(g.reserves, body)
			stored := len(fresh)
			g.mu.Unlock()
			if stored != 0 {
				t.Errorf("reservation arrived after %d replies; a borrow must reserve before driving", stored)
			}
			_, _ = w.Write([]byte(`{"reserved":true,"reservationId":"rsv_test"}`))
		case r.URL.Path == "/v1/my/tenant-policy":
			_, _ = w.Write([]byte(`{"tenantId":"default","requireSandboxForBorrow":false,"anonymousLinksDisabled":false}`))
		case strings.HasPrefix(r.URL.Path, "/v1/rooms/"+room+"/"):
			inner.ServeHTTP(w, r)
		default: // job status / lease reports
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *borrowGateway) waitRecord(t *testing.T) (map[string]any, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		if len(g.records) > 0 {
			defer g.mu.Unlock()
			if len(g.records) != 1 {
				t.Fatalf("records = %d; want exactly one per borrowed turn", len(g.records))
			}
			return g.records[0], g.storedAtCalls[0]
		}
		g.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("borrow was never recorded")
	return nil, 0
}

func runBorrowedJob(t *testing.T, msgs []corepkg.Message, model string, failPosts int64) *borrowGateway {
	t.Helper()
	g := newBorrowGateway(t, "room-1")
	g.failPosts.Store(failPosts)
	prov := &jobProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "c", Provider: "codex"}}}, messages: msgs}
	prov.pollsBusy = 3
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.runBridgeJob(context.Background(), bridgeJob{
		JobID: "job-1", RoomID: "room-1", OwnerUserID: "alice", RequesterUserID: "bob",
		Core: "core-a", Provider: "codex", MessageText: "go", ChannelID: "ch", Model: model,
	})
	return g
}

func reply(version int64, text string, metrics map[string]any) corepkg.Message {
	return corepkg.Message{Version: version, Seq: version, Role: "assistant", Text: text, UserVisible: true,
		Completed: true, MessageID: text, Metrics: metrics}
}

// G2 / R8: the borrow is reserved before the drive and settled after it; the
// settle carries the usage the turn's replies reported — summed across
// replies, counted once even when a reply post had to be retried.
func TestBorrowRecordCarriesTurnUsage(t *testing.T) {
	g := runBorrowedJob(t, []corepkg.Message{
		reply(1, "part one", map[string]any{"model": "m-1", "reported": true, "inputTokens": float64(100), "outputTokens": float64(20), "costUsd": 0.01}),
		reply(2, "part two", map[string]any{"model": "m-2", "reported": true, "inputTokens": float64(50), "outputTokens": float64(5), "costUsd": 0.02}),
	}, "requested-model", 1)
	rec, storedBefore := g.waitRecord(t)
	if storedBefore != 2 {
		t.Fatalf("record arrived with %d replies stored; want it after the turn's 2 replies", storedBefore)
	}
	if rec["reservationId"] != "rsv_test" {
		t.Fatalf("record = %v; want it to settle the reservation", rec)
	}
	g.mu.Lock()
	reserves := g.reserves
	g.mu.Unlock()
	if len(reserves) != 1 || reserves[0]["borrower"] != "bob" || reserves[0]["channelId"] != "ch" || reserves[0]["model"] != "requested-model" {
		t.Fatalf("reservations = %v; want one for borrower bob on channel ch with the requested model", reserves)
	}
	if rec["inputTokens"] != float64(150) || rec["outputTokens"] != float64(25) {
		t.Fatalf("tokens = %v/%v; want 150/25 (summed, retried post counted once)", rec["inputTokens"], rec["outputTokens"])
	}
	if c, _ := rec["cost"].(float64); math.Abs(c-0.03) > 1e-9 {
		t.Fatalf("cost = %v; want 0.03", rec["cost"])
	}
	if rec["model"] != "m-2" {
		t.Fatalf("model = %v; want the reported model m-2", rec["model"])
	}
}

// G2: nothing is fabricated — a turn whose replies report no usage records
// the run with no token/cost fields; the requested model stands in only for
// the model label. A reported=false reply contributes its model but no usage.
func TestBorrowRecordOmitsUnreportedUsage(t *testing.T) {
	g := runBorrowedJob(t, []corepkg.Message{reply(1, "plain", nil)}, "requested-model", 0)
	rec, _ := g.waitRecord(t)
	for _, k := range []string{"inputTokens", "outputTokens", "cost"} {
		if _, has := rec[k]; has {
			t.Fatalf("record carries %s = %v for a turn that reported no usage", k, rec[k])
		}
	}
	if rec["model"] != "requested-model" {
		t.Fatalf("model = %v; want the job's requested model", rec["model"])
	}

	g = runBorrowedJob(t, []corepkg.Message{reply(1, "unreported", map[string]any{"model": "m-x", "reported": false, "inputTokens": float64(0)})}, "", 0)
	rec, _ = g.waitRecord(t)
	if _, has := rec["inputTokens"]; has {
		t.Fatalf("reported=false reply recorded tokens: %v", rec)
	}
	if rec["model"] != "m-x" {
		t.Fatalf("model = %v; want m-x", rec["model"])
	}
}

func TestTurnUsageRecordFields(t *testing.T) {
	var u turnUsage
	u.add(map[string]any{"reported": true, "inputTokens": float64(10), "outputTokens": float64(3)})
	f := u.recordFields("")
	if f["inputTokens"] != int64(10) || f["outputTokens"] != int64(3) {
		t.Fatalf("fields = %v", f)
	}
	if _, has := f["cost"]; has {
		t.Fatal("cost present although no reply reported one")
	}
	if _, has := f["model"]; has {
		t.Fatal("model present although none was reported or requested")
	}
	var nilUsage *turnUsage
	nilUsage.add(map[string]any{"inputTokens": float64(1)}) // untracked: no panic
}
