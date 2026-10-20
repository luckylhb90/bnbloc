package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeScheduleGateway exchanges an API token for a session of its subject
// and serves a room's schedule list and "run now" (creator only, like
// realtime's route).
type fakeScheduleGateway struct {
	*httptest.Server
	mu   sync.Mutex
	runs []string
}

func newFakeScheduleGateway(t *testing.T) *fakeScheduleGateway {
	g := &fakeScheduleGateway{}
	schedules := []wire.RoomSchedule{
		{ScheduleID: "sch_e2e", CreatedBy: "svc:ci", Enabled: true, Timezone: "UTC", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: []string{wire.PlatformDeployed, wire.ComponentPublished}}},
		{ScheduleID: "sch_ai", CreatedBy: "svc:ci", Enabled: true, Timezone: "UTC", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}, NextRunAtMs: 1_800_000_000_000, LastStatus: "error"},
		{ScheduleID: "sch_maya", CreatedBy: "maya", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "0 9 * * 1"}, Timezone: "UTC"},
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/api-token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "sess-ci", "subject": "svc:ci", "tenant": "home", "role": "member"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer sess-ci" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/rooms/room-1/schedules":
			_ = json.NewEncoder(w).Encode(map[string]any{"schedules": schedules})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/rooms/room-1/schedules/") && strings.HasSuffix(r.URL.Path, "/run"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/rooms/room-1/schedules/"), "/run")
			g.runs = append(g.runs, id+"?"+r.URL.RawQuery)
			switch id {
			case "sch_e2e":
				_ = json.NewEncoder(w).Encode(map[string]int64{"sequence": 42})
			case "sch_ai":
				_ = json.NewEncoder(w).Encode(wire.ScheduleRunReply{Status: wire.ScheduleRunError, Error: "AI is not available", Actions: []wire.ScheduleAction{{Name: "notify", Detail: "to the room", DryRun: true}}})
			default:
				http.Error(w, `{"error":"only the creator may run this schedule"}`, http.StatusForbidden)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// A script lists a room's schedules and runs one as its creator with an
// API token; a failed AI run and a refusal fail the command.
func TestSchedulesCommand(t *testing.T) {
	isolateHome(t)
	g := newFakeScheduleGateway(t)
	t.Setenv("BNBLOC_TOKEN", "")
	t.Setenv(apiTokenEnv, "bnbloc_at_ci")

	var out bytes.Buffer
	if err := schedulesCmd([]string{"list", "--gateway", g.URL, "room-1"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sch_e2e", "on platform.deployed,component.published", "sch_ai", "daily 09:00 UTC", "error", "cron 0 9 * * 1 UTC", "paused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := schedulesCmd([]string{"run", "--gateway", g.URL, "room-1", "sch_e2e"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "ran sch_e2e: posted message #42\n" {
		t.Fatalf("run = %q", got)
	}

	out.Reset()
	err := schedulesCmd([]string{"run", "--gateway", g.URL, "--dry", "room-1", "sch_ai"}, &out)
	if err == nil || !strings.Contains(err.Error(), "AI is not available") || !strings.Contains(out.String(), "notify: to the room (dry run)") {
		t.Fatalf("a failed AI run = %v\n%s", err, out.String())
	}
	if err := schedulesCmd([]string{"run", "--gateway", g.URL, "room-1", "sch_maya"}, &out); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("running another member's schedule = %v", err)
	}
	if err := schedulesCmd([]string{"run", "--gateway", g.URL, "room-1"}, &out); err == nil {
		t.Fatal("run without a schedule id passed")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.Join(g.runs, " ") != "sch_e2e? sch_ai?dry=1 sch_maya?" {
		t.Fatalf("runs = %v", g.runs)
	}
}
