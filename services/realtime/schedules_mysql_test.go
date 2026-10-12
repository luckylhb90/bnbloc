package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/testmysql"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// TestMySQLScheduledRunEndToEnd runs the scheduler on the durable path
// (gated on BNBLOC_TEST_MYSQL_DSN): a schedule created over HTTP is claimed
// from MySQL, posted through PostChat into room_messages with its slot
// clientMsgId, and two schedulers racing the same tick post it exactly once.
func TestMySQLScheduledRunEndToEnd(t *testing.T) {
	b, err := store.NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.DB.Close() })
	room := "sched-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	members := &schedMembers{m: map[string]bool{}}
	members.set(room, "alice", true)
	newSvc := func() *Service {
		return &Service{Hub: realtime.NewHub(64, 16), Membership: members, Chat: b.Chat, Schedules: b.Schedules}
	}
	svc := newSvc()
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	f := &schedFixture{t: t, srv: srv}
	code, out := f.do("alice", http.MethodPost, "/v1/rooms/"+room+"/schedules", dailyBody("durable ping", map[string]any{"kind": "room"}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, out)
	}
	var sc wire.RoomSchedule
	_ = json.Unmarshal(out, &sc)
	slot := time.UnixMilli(sc.NextRunAtMs)

	// Two scheduler instances tick at once (sharing the hub would hide a
	// double publish; each has its own, both persist to the same table).
	other := newSvc()
	var wg sync.WaitGroup
	for _, s := range []*Service{svc, other} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			s.RunDueSchedules(context.Background(), slot.Add(time.Second))
		}(s)
	}
	wg.Wait()
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	msgs, err := b.Chat.RoomFeed(ctx, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Author != "alice" {
		t.Fatalf("durable feed = %+v", msgs)
	}
	var p map[string]any
	_ = json.Unmarshal(msgs[0].Payload, &p)
	if p["clientMsgId"] != schedule.RunSlotClientMsgID(sc.ScheduleID, slot) || p["scheduled"] == nil {
		t.Fatalf("durable payload = %v", p)
	}
	got, err := b.Schedules.GetSchedule(ctx, room, sc.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != wire.ScheduleRunOK || got.RunCount != 1 || got.NextRunAtMs != slot.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("after run = %+v", got)
	}
}
