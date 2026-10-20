package schedule

import (
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An agent recipient names its agent (owner and provider) or none of it:
// the one without waits for an agent and stays one; half a target is
// refused.
func TestAgentRecipientWaitsForAgent(t *testing.T) {
	waiting, err := NormalizeRecipient(wire.ScheduleRecipient{Kind: " agent ", OwnerID: " "})
	if err != nil || !WaitsForAgent(waiting) || waiting.OwnerID != "" || waiting.Kind != wire.ScheduleRecipientAgent {
		t.Fatalf("a target-less agent recipient = %+v, %v", waiting, err)
	}
	named, err := NormalizeRecipient(wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "maya", Provider: "codex"})
	if err != nil || WaitsForAgent(named) {
		t.Fatalf("a named agent = %+v, %v", named, err)
	}
	for _, half := range []wire.ScheduleRecipient{{OwnerID: "maya"}, {Provider: "codex"}, {Core: "acme"}} {
		half.Kind = wire.ScheduleRecipientAgent
		if WaitsForAgent(half) {
			t.Errorf("%+v waits for an agent", half)
		}
		if _, err := NormalizeRecipient(half); err == nil {
			t.Errorf("half a target %+v was accepted", half)
		}
	}
	for _, k := range []string{wire.ScheduleRecipientAI, wire.ScheduleRecipientRoom, wire.ScheduleRecipientNotice, ""} {
		if WaitsForAgent(wire.ScheduleRecipient{Kind: k}) {
			t.Errorf("kind %q waits for an agent", k)
		}
	}
}

func mustZone(t *testing.T, tz string) *time.Location {
	t.Helper()
	loc, err := LoadZone(tz)
	if err != nil {
		t.Fatalf("zone %s: %v", tz, err)
	}
	return loc
}

func daily(hhmm string) wire.ScheduleRecurrence {
	return wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: hhmm}
}

// next asserts Next(rec, loc, after) == want (RFC3339 in loc).
func expectNext(t *testing.T, rec wire.ScheduleRecurrence, loc *time.Location, after time.Time, want string) time.Time {
	t.Helper()
	got, ok := Next(rec, loc, after)
	if !ok {
		t.Fatalf("Next(%+v, after %s) = none, want %s", rec, after.In(loc), want)
	}
	if s := got.In(loc).Format(time.RFC3339); s != want {
		t.Fatalf("Next(%+v, after %s) = %s, want %s", rec, after.In(loc).Format(time.RFC3339), s, want)
	}
	return got
}

func TestDailyKeepsLocalWallClockAcrossDST(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// 2026 US DST: starts Sun Mar 8, ends Sun Nov 1.
	after := time.Date(2026, 3, 6, 10, 0, 0, 0, ny)
	n1 := expectNext(t, daily("09:00"), ny, after, "2026-03-07T09:00:00-05:00")
	n2 := expectNext(t, daily("09:00"), ny, n1, "2026-03-08T09:00:00-04:00")
	if d := n2.Sub(n1); d != 23*time.Hour {
		t.Fatalf("spring-forward day should be 23h apart in real time, got %s", d)
	}
	n3 := expectNext(t, daily("09:00"), ny, time.Date(2026, 10, 31, 9, 0, 0, 0, ny), "2026-11-01T09:00:00-05:00")
	if d := n3.Sub(time.Date(2026, 10, 31, 9, 0, 0, 0, ny)); d != 25*time.Hour {
		t.Fatalf("fall-back day should be 25h apart in real time, got %s", d)
	}
}

func TestSpringForwardGapRunsAfterTheGap(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// 02:30 does not exist on 2026-03-08 in New York: it runs at 03:30 EDT.
	n := expectNext(t, daily("02:30"), ny, time.Date(2026, 3, 7, 12, 0, 0, 0, ny), "2026-03-08T03:30:00-04:00")
	// And the day after is back to 02:30.
	expectNext(t, daily("02:30"), ny, n, "2026-03-09T02:30:00-04:00")
}

func TestFallBackOverlapRunsOnce(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// 01:30 exists twice on 2026-11-01: run once, at the earlier (EDT) instant.
	n := expectNext(t, daily("01:30"), ny, time.Date(2026, 10, 31, 12, 0, 0, 0, ny), "2026-11-01T01:30:00-04:00")
	// The next run is the following day, not the second 01:30 an hour later.
	expectNext(t, daily("01:30"), ny, n, "2026-11-02T01:30:00-05:00")
	// A cron "every 30 minutes" walks through the overlap without repeating a
	// wall-clock slot (the repeated hour is not run twice).
	every30 := wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "*/30 * * * *"}
	at := time.Date(2026, 11, 1, 0, 45, 0, 0, ny)
	var got []string
	for i := 0; i < 4; i++ {
		nx, ok := Next(every30, ny, at)
		if !ok {
			t.Fatal("no next")
		}
		got = append(got, nx.In(ny).Format("15:04Z07:00"))
		at = nx
	}
	want := "01:00-04:00 01:30-04:00 02:00-05:00 02:30-05:00"
	if strings.Join(got, " ") != want {
		t.Fatalf("overlap walk = %v, want %s", got, want)
	}
}

func TestSouthernHemisphereAndHalfHourZones(t *testing.T) {
	syd := mustZone(t, "Australia/Sydney")
	// Sydney DST ends 2026-04-05 03:00 -> 02:00; 09:00 keeps its wall clock.
	expectNext(t, daily("09:00"), syd, time.Date(2026, 4, 4, 10, 0, 0, 0, syd), "2026-04-05T09:00:00+10:00")
	kol := mustZone(t, "Asia/Kolkata")
	expectNext(t, daily("09:00"), kol, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01T09:00:00+05:30")
}

func TestWeekly(t *testing.T) {
	berlin := mustZone(t, "Europe/Berlin")
	rec, _, err := Validate(wire.ScheduleRecurrence{Kind: wire.RecurWeekly, Time: "17:15", Days: []int{5, 1, 1, 7}}, "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Days; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 5 {
		t.Fatalf("normalized days = %v, want [0 1 5]", got)
	}
	// 2026-03-25 is a Wednesday; the next is Friday 27th, then Sunday 29th
	// (the CET->CEST switch day), then Monday 30th.
	n := expectNext(t, rec, berlin, time.Date(2026, 3, 25, 12, 0, 0, 0, berlin), "2026-03-27T17:15:00+01:00")
	n = expectNext(t, rec, berlin, n, "2026-03-29T17:15:00+02:00")
	expectNext(t, rec, berlin, n, "2026-03-30T17:15:00+02:00")
}

func TestOnce(t *testing.T) {
	utc := mustZone(t, "UTC")
	at := time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	rec := wire.ScheduleRecurrence{Kind: wire.RecurOnce, AtMs: at.UnixMilli()}
	expectNext(t, rec, utc, at.Add(-time.Hour), "2026-12-01T08:00:00Z")
	if _, ok := Next(rec, utc, at); ok {
		t.Fatal("a one-shot must not fire again after its slot")
	}
	if _, ok := NextRun(rec, utc, at, at.Add(time.Second)); ok {
		t.Fatal("NextRun of a fired one-shot must be none")
	}
}

func TestCronLite(t *testing.T) {
	utc := mustZone(t, "UTC")
	cases := []struct {
		cron, after, want string
	}{
		{"0 9 * * 1-5", "2026-10-09T10:00:00Z", "2026-10-12T09:00:00Z"},    // Fri after 9 -> Mon
		{"*/15 8-9 * * *", "2026-10-08T09:50:00Z", "2026-10-09T08:00:00Z"}, // window ended
		{"30 6 1 * *", "2026-10-08T00:00:00Z", "2026-11-01T06:30:00Z"},     // monthly
		{"0 12 29 2 *", "2026-03-01T00:00:00Z", "2028-02-29T12:00:00Z"},    // leap day
		{"0 0 13 * 5", "2026-10-08T00:00:00Z", "2026-10-09T00:00:00Z"},     // dom OR dow: Friday 9th
		{"0 18 * * 0", "2026-10-08T00:00:00Z", "2026-10-11T18:00:00Z"},     // Sunday
		{"0 18 * * 7", "2026-10-08T00:00:00Z", "2026-10-11T18:00:00Z"},     // 7 = Sunday
		{"5,35 * * * *", "2026-10-08T10:05:00Z", "2026-10-08T10:35:00Z"},   // list
		{"0 0-23/6 * * *", "2026-10-08T07:00:00Z", "2026-10-08T12:00:00Z"}, // stepped range
	}
	for _, c := range cases {
		rec, _, err := Validate(wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: c.cron}, "UTC")
		if err != nil {
			t.Fatalf("%q: %v", c.cron, err)
		}
		after, _ := time.Parse(time.RFC3339, c.after)
		expectNext(t, rec, utc, after, c.want)
	}
}

func TestValidateRejects(t *testing.T) {
	bad := []struct {
		rec wire.ScheduleRecurrence
		tz  string
	}{
		{daily("09:00"), ""},
		{daily("09:00"), "Local"},
		{daily("09:00"), "Mars/Olympus"},
		{daily("9"), "UTC"},
		{daily("24:00"), "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurWeekly, Time: "09:00"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurWeekly, Time: "09:00", Days: []int{8}}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurOnce}, "UTC"},
		{wire.ScheduleRecurrence{Kind: "hourly"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "* * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "60 * * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "5/2 * * * *"}, "UTC"},
		// Minimum interval: every minute, every 4 minutes, a 2-minute pair,
		// and the circular wrap across midnight.
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "* * * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "*/4 * * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "0,2 9 * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "58 23 * * *,0 0 * * *"}, "UTC"},
		{wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "0,58 0,23 * * *"}, "UTC"},
	}
	for _, b := range bad {
		if _, _, err := Validate(b.rec, b.tz); err == nil {
			t.Errorf("Validate(%+v, %q) accepted, want error", b.rec, b.tz)
		}
	}
	for _, ok := range []string{"*/5 * * * *", "0 * * * *", "0,30 9-17 * * 1-5"} {
		if _, _, err := Validate(wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: ok}, "UTC"); err != nil {
			t.Errorf("Validate(%q) = %v, want ok", ok, err)
		}
	}
}

func TestNextRunCollapsesMissedSlotsAndKeepsSpacing(t *testing.T) {
	utc := mustZone(t, "UTC")
	rec := daily("09:00")
	slot := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	// Scheduler down for a week: the next run is the next future slot, not
	// the seven missed ones.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	n, ok := NextRun(rec, utc, slot, now)
	if !ok || !n.Equal(time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextRun after downtime = %s, want 2026-10-09T09:00Z", n)
	}
	// Spacing: an every-5-minutes cron right after its slot moves on by 5m.
	every5 := wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "*/5 * * * *"}
	n, _ = NextRun(every5, utc, slot, slot.Add(time.Second))
	if d := n.Sub(slot); d != 5*time.Minute {
		t.Fatalf("every-5 NextRun spacing = %s", d)
	}
	// DST compression: "0,58 1,3" is never closer than 58 minutes on the
	// wall clock, but on the spring-forward day 01:58 EST and 03:00 EDT are
	// only 2 real minutes apart. The runtime guard skips the 03:00 run.
	ny := mustZone(t, "America/New_York")
	tight := wire.ScheduleRecurrence{Kind: wire.RecurCron, Cron: "0,58 1,3 * * *"}
	if _, _, err := Validate(tight, "America/New_York"); err != nil {
		t.Fatalf("0,58 1,3 should validate: %v", err)
	}
	prev := time.Date(2026, 3, 8, 1, 58, 0, 0, ny)
	if raw, _ := Next(tight, ny, prev); raw.Sub(prev) != 2*time.Minute {
		t.Fatalf("expected the raw next run 2 real minutes later, got %s", raw.Sub(prev))
	}
	n, _ = NextRun(tight, ny, prev, prev)
	if got := n.In(ny).Format(time.RFC3339); got != "2026-03-08T03:58:00-04:00" {
		t.Fatalf("runtime guard next = %s, want 03:58 EDT", got)
	}
}

func TestRunSlotClientMsgIDIsStable(t *testing.T) {
	slot := time.UnixMilli(1791000000000)
	a := RunSlotClientMsgID("sch_1", slot)
	if a != RunSlotClientMsgID("sch_1", slot) || a == RunSlotClientMsgID("sch_1", slot.Add(time.Minute)) {
		t.Fatalf("slot ids must be stable per slot and distinct across slots: %s", a)
	}
	if len(a) > 128 {
		t.Fatalf("client msg id too long for room_messages.client_msg_id: %d", len(a))
	}
}
