package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// runSlot calls the internal run route as realtime would for schedule sc at
// slot, and answers the reply.
func (e *appGW) runSlot(sc wire.RoomSchedule, slot time.Time, dry bool) (int, wire.ScheduleRunReply) {
	e.t.Helper()
	sc.TenantID, sc.RoomID, sc.CreatedBy, sc.Timezone = e.tenant, e.roomID, "jack", "Asia/Shanghai"
	body, _ := json.Marshal(wire.ScheduleRunRequest{Schedule: sc, SlotMs: slot.UnixMilli(), DryRun: dry})
	req, _ := svcauth.Key(appGWKey).NewRequest(context.Background(), http.MethodPost, e.srv.URL+"/internal/v1/schedule-runs", bytes.NewReader(body), svcauth.ServiceSubject("realtime"), e.tenant)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out wire.ScheduleRunReply
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func noticeSchedule(to any, title, text string, compose bool) wire.RoomSchedule {
	raw, _ := json.Marshal(to)
	return wire.RoomSchedule{ScheduleID: "sch_n", Text: text, Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: raw, Title: title, Compose: compose}}
}

// A notice schedule sends what it says, with no model: its title and body name
// the slot's own values (the zone's week and date), one slot is one notice
// however often it is claimed, and a dry run sends nothing and says what it
// would.
func TestNoticeScheduleRunSendsWithoutAModel(t *testing.T) {
	e, model, outbox := scheduleGW(t)
	slot := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC) // Thursday 09:00 in Shanghai, ISO week 43
	sc := noticeSchedule([]string{"nora"}, "Weekly report {week}", "Fill {week} in by Friday ({date}).", false)

	code, reply := e.runSlot(sc, slot, false)
	if code != http.StatusOK || reply.Status != wire.ScheduleRunOK || len(reply.Actions) != 1 || reply.Actions[0].Name != "notify" {
		t.Fatalf("run = %d %+v", code, reply)
	}
	got := queued(t, outbox)
	if len(got) != 1 || strings.Join(recipientsOf(got[0]), ",") != "nora" || !strings.Contains(got[0].Title, "Weekly report 2026-W43") || got[0].Text != "Fill 2026-W43 in by Friday (2026-10-22)." {
		t.Fatalf("notices = %+v", got)
	}
	if len(model.seen) != 0 {
		t.Fatal("a notice that says what it sends asked a model")
	}
	// The same slot again (a re-claimed run) sends nothing twice; the next week's slot does.
	e.runSlot(sc, slot, false)
	if n := len(queued(t, outbox)); n != 1 {
		t.Fatalf("a re-run of the slot queued %d notices", n)
	}
	e.runSlot(sc, slot.AddDate(0, 0, 7), false)
	// The outbox lists by notice id, not by send order: look for the title.
	got = queued(t, outbox)
	if len(got) != 2 || !strings.Contains(got[0].Title+got[1].Title, "2026-W44") {
		t.Fatalf("next week's notices = %+v", got)
	}

	before := len(queued(t, outbox))
	_, dry := e.runSlot(noticeSchedule("room", "Standup", "", false), slot, true)
	if dry.Status != wire.ScheduleRunOK || len(dry.Actions) != 1 || !dry.Actions[0].DryRun || !strings.Contains(dry.Actions[0].Detail, "would notify") {
		t.Fatalf("dry run = %+v", dry)
	}
	if len(queued(t, outbox)) != before {
		t.Fatal("a dry run sent a notice")
	}
}

// A refusal is a run that failed, with its sentence: a group nobody connected,
// a title the emitter cannot take.
func TestNoticeScheduleRunFailsWithASentence(t *testing.T) {
	e, _, outbox := scheduleGW(t)
	slot := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	_, reply := e.runSlot(noticeSchedule("group:Infra", "Weekly report", "x", false), slot, false)
	if reply.Status != wire.ScheduleRunError || !strings.Contains(reply.Error, "no group chat of that name") {
		t.Fatalf("a group nobody connected = %+v", reply)
	}
	if len(queued(t, outbox)) != 0 {
		t.Fatal("a failed run sent a notice")
	}
	// A title the expansion made longer than a notice may carry is cut, not refused.
	long := strings.Repeat("t", 119) + "{date}"
	if _, r := e.runSlot(noticeSchedule("room", long, "", false), slot, false); r.Status != wire.ScheduleRunOK {
		t.Fatalf("a long title = %+v", r)
	}
}

// With Compose the model only writes the body: it gets the owner's instruction,
// no tools, and what it writes is sent to the fixed destination under the fixed
// title, once per slot (a second try writes other words and sends nothing).
func TestNoticeScheduleRunComposesTheBody(t *testing.T) {
	e, model, outbox := scheduleGW(t)
	slot := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	sc := noticeSchedule([]string{"nora"}, "Weekly report {week}", "Ask the team to fill in the weekly report, friendly and short.", true)

	model.turns = []any{map[string]any{"content": "  Please fill in this week's report today.  "}}
	_, reply := e.runSlot(sc, slot, false)
	if reply.Status != wire.ScheduleRunOK || len(reply.Actions) != 2 || reply.Actions[0].Name != "compose" || reply.Actions[1].Name != "notify" {
		t.Fatalf("run = %+v", reply)
	}
	if len(model.seen) != 1 || len(model.tools) != 0 {
		t.Fatalf("the model was called %d times with tools %v", len(model.seen), model.tools)
	}
	sys, user := fmt.Sprint(model.seen[0][0]["content"]), fmt.Sprint(model.seen[0][1]["content"])
	if !strings.Contains(sys, "body of a notice") || !strings.Contains(user, "friendly and short") {
		t.Fatalf("the model was sent %q / %q", sys, user)
	}
	got := queued(t, outbox)
	if len(got) != 1 || got[0].Text != "Please fill in this week's report today." || !strings.Contains(got[0].Title, "Weekly report 2026-W43") || strings.Join(recipientsOf(got[0]), ",") != "nora" {
		t.Fatalf("notices = %+v", got)
	}
	model.turns = []any{map[string]any{"content": "A different text for the same slot."}}
	e.runSlot(sc, slot, false)
	if n := len(queued(t, outbox)); n != 1 {
		t.Fatalf("a re-run of the slot with another body queued %d notices", n)
	}

	// Dry: the model still writes (a read), nothing is sent, and the body is shown.
	before := len(queued(t, outbox))
	model.turns = []any{map[string]any{"content": "Draft body."}}
	_, dry := e.runSlot(sc, slot.AddDate(0, 0, 7), true)
	if dry.Status != wire.ScheduleRunOK || len(dry.Actions) != 2 || !dry.Actions[0].DryRun || !strings.Contains(dry.Actions[0].Detail, "Draft body.") {
		t.Fatalf("dry run = %+v", dry)
	}
	if len(queued(t, outbox)) != before {
		t.Fatal("a dry run sent a notice")
	}
	// A model that writes nothing fails the run, and sends nothing.
	model.turns = []any{map[string]any{"content": "   "}}
	if _, r := e.runSlot(sc, slot.AddDate(0, 0, 14), false); r.Status != wire.ScheduleRunError || !strings.Contains(r.Error, "no text") {
		t.Fatalf("an empty body = %+v", r)
	}
}
