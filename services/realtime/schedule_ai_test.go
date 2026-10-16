package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeAIRuns answers runs with reply (or err) and records them.
type fakeAIRuns struct {
	mu    sync.Mutex
	reply wire.ScheduleRunReply
	err   error
	runs  []bool // dry of each run
}

func (f *fakeAIRuns) RunSchedule(_ context.Context, sc wire.RoomSchedule, _ time.Time, dry bool) (wire.ScheduleRunReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, dry)
	return f.reply, f.err
}

func aiBody(task string) map[string]any { return dailyBody(task, map[string]any{"kind": "ai"}) }

// An ai schedule is refused where no runner is wired; with one, its due
// slot goes to the runner (no chat post by realtime), the run's summary
// is recorded and the next run planned.
func TestAIScheduleRunsThroughTheRunner(t *testing.T) {
	f := newSchedFixture(t)
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", aiBody("remind who has not filled in")); code != http.StatusServiceUnavailable {
		t.Fatalf("an ai schedule without a runner = %d %s", code, out)
	}
	runs := &fakeAIRuns{reply: wire.ScheduleRunReply{Status: wire.ScheduleRunOK, Summary: "Notified 2 people."}}
	f.svc.AIRuns = runs
	sc := f.create("alice", aiBody("remind who has not filled in"))
	if sc.Recipient.Kind != wire.ScheduleRecipientAI {
		t.Fatalf("recipient = %+v", sc.Recipient)
	}
	slot := time.UnixMilli(sc.NextRunAtMs)
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(time.Second)); n != 1 {
		t.Fatalf("ran %d", n)
	}
	if len(runs.runs) != 1 || runs.runs[0] || len(f.history()) != 0 {
		t.Fatalf("runs %v, history %d", runs.runs, len(f.history()))
	}
	got := f.get(sc.ScheduleID)
	if got.LastStatus != wire.ScheduleRunOK || got.LastSummary != "Notified 2 people." || got.NextRunAtMs != slot.Add(24*time.Hour).UnixMilli() || got.FailedRuns != 0 {
		t.Fatalf("after run = %+v", got)
	}
}

// Run now with ?dry=1 answers what a run would do and records nothing; a
// real run now is recorded.
func TestAIScheduleDryRun(t *testing.T) {
	f := newSchedFixture(t)
	runs := &fakeAIRuns{reply: wire.ScheduleRunReply{Status: wire.ScheduleRunOK, Summary: "would notify 2", Actions: []wire.ScheduleAction{{Name: "notify", Detail: "would notify 2 people: x", DryRun: true}}}}
	f.svc.AIRuns = runs
	sc := f.create("alice", aiBody("remind"))
	code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run?dry=1", nil)
	var reply wire.ScheduleRunReply
	if code != http.StatusOK || json.Unmarshal(out, &reply) != nil || len(reply.Actions) != 1 || !reply.Actions[0].DryRun {
		t.Fatalf("dry run = %d %s", code, out)
	}
	if got := f.get(sc.ScheduleID); got.LastRunAtMs != 0 || len(runs.runs) != 1 || !runs.runs[0] {
		t.Fatalf("a dry run was recorded: %+v, runs %v", got, runs.runs)
	}
	if code, out := f.do("bob", http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run?dry=1", nil); code != http.StatusForbidden {
		t.Fatalf("someone else's dry run = %d %s", code, out)
	}
	if code, _ := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run", nil); code != http.StatusOK {
		t.Fatalf("run now = %d", code)
	}
	if got := f.get(sc.ScheduleID); got.LastSummary != "would notify 2" || got.LastRunAtMs == 0 {
		t.Fatalf("a real run now was not recorded: %+v", got)
	}
}

// A failing task pauses after three failed runs in a row; a transport
// failure keeps the slot for the next tick instead.
func TestAIScheduleFailuresPause(t *testing.T) {
	f := newSchedFixture(t)
	runs := &fakeAIRuns{err: errors.New("gateway down")}
	f.svc.AIRuns = runs
	sc := f.create("alice", aiBody("remind"))
	slot := time.UnixMilli(sc.NextRunAtMs)
	f.svc.RunDueSchedules(context.Background(), slot.Add(time.Second))
	if got := f.get(sc.ScheduleID); got.NextRunAtMs != slot.UnixMilli() || !got.Enabled {
		t.Fatalf("a transport failure moved the slot: %+v", got)
	}
	runs.err, runs.reply = nil, wire.ScheduleRunReply{Status: wire.ScheduleRunError, Error: "AI is not available"}
	at := slot
	for i := 0; i < 3; i++ {
		sc = f.get(sc.ScheduleID)
		at = time.UnixMilli(sc.NextRunAtMs)
		f.svc.RunDueSchedules(context.Background(), at.Add(2*time.Minute+time.Second))
	}
	got := f.get(sc.ScheduleID)
	if got.Enabled || got.FailedRuns != 3 {
		t.Fatalf("after three failed runs = %+v", got)
	}
}

func noticeBody(text string, recipient map[string]any) map[string]any {
	recipient["kind"] = "notice"
	return dailyBody(text, recipient)
}

// A notice schedule is made from what it sends (where, a title, a body that may
// name the run's own values) and refuses what a typo or a missing part would send
// wrong; it needs the gateway's run route like an ai schedule, and its due slot
// goes there, not out as chat.
func TestNoticeScheduleIsValidatedAndRunsThroughTheRunner(t *testing.T) {
	f := newSchedFixture(t)
	good := noticeBody("Fill in {week} by Friday.", map[string]any{"to": "group:Infra", "title": "Weekly report {week}"})
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", good); code != http.StatusServiceUnavailable {
		t.Fatalf("a notice schedule without a runner = %d %s", code, out)
	}
	runs := &fakeAIRuns{reply: wire.ScheduleRunReply{Status: wire.ScheduleRunOK, Summary: "notified the group Infra"}}
	f.svc.AIRuns = runs

	for name, body := range map[string]map[string]any{
		"no target":      noticeBody("x", map[string]any{"title": "t"}),
		"no title":       noticeBody("x", map[string]any{"to": "room"}),
		"unknown name":   noticeBody("Due {wek}", map[string]any{"to": "room", "title": "t"}),
		"unknown title":  noticeBody("x", map[string]any{"to": "room", "title": "Week {wek}"}),
		"no instruction": noticeBody("", map[string]any{"to": "room", "title": "t", "compose": true}),
	} {
		if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", body); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, code, out)
		}
	}
	// A body may be empty (a title is a notice), and a compose instruction may
	// hold braces (the model reads it as words).
	f.create("alice", noticeBody("", map[string]any{"to": "room", "title": "Standup"}))
	f.create("alice", noticeBody("Write a short reminder, as {a list}.", map[string]any{"to": "room", "title": "t", "compose": true}))

	sc := f.create("alice", good)
	if sc.Recipient.Kind != wire.ScheduleRecipientNotice || string(sc.Recipient.To) != `"group:Infra"` || sc.Recipient.Title != "Weekly report {week}" {
		t.Fatalf("stored recipient = %+v", sc.Recipient)
	}
	slot := time.UnixMilli(sc.NextRunAtMs)
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(time.Second)); n < 1 {
		t.Fatalf("ran %d", n)
	}
	if len(runs.runs) < 1 || len(f.history()) != 0 {
		t.Fatalf("runs %v, history %d: a notice is not chat", runs.runs, len(f.history()))
	}
	if got := f.get(sc.ScheduleID); got.LastStatus != wire.ScheduleRunOK || got.LastSummary != "notified the group Infra" {
		t.Fatalf("after run = %+v", got)
	}
	// Run now and its dry run go to the runner too.
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run?dry=1", nil); code != http.StatusOK {
		t.Fatalf("dry run = %d %s", code, out)
	}
}
