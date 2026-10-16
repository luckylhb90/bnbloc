package schedule

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestNormalizeNotice(t *testing.T) {
	ok := func(r wire.ScheduleRecipient) wire.ScheduleRecipient {
		t.Helper()
		out, err := NormalizeRecipient(r)
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		return out
	}
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	got := ok(wire.ScheduleRecipient{Kind: "notice", To: raw(" group: Infra "), Title: "  Weekly report {week}  ", URL: " ", Compose: true})
	if string(got.To) != `"group:Infra"` || got.Title != "Weekly report {week}" || got.URL != "" || !got.Compose || got.Kind != wire.ScheduleRecipientNotice {
		t.Errorf("group: %+v", got)
	}
	if got := ok(wire.ScheduleRecipient{Kind: "notice", To: raw("room"), Title: "x"}); string(got.To) != `"room"` {
		t.Errorf("room: %+v", got)
	}
	if got := ok(wire.ScheduleRecipient{Kind: "notice", To: raw([]string{" maya ", "jack", "maya", ""}), Title: "x"}); string(got.To) != `["maya","jack"]` {
		t.Errorf("logins: %s", got.To)
	}
	if got := ok(wire.ScheduleRecipient{Kind: "notice", To: raw("room"), Title: "x", OwnerID: "o", Provider: "p"}); got.OwnerID != "" || got.Provider != "" {
		t.Errorf("a notice carries no agent: %+v", got)
	}

	for name, r := range map[string]wire.ScheduleRecipient{
		"no target":          {Kind: "notice", Title: "x"},
		"unknown target":     {Kind: "notice", To: raw("everyone"), Title: "x"},
		"empty group":        {Kind: "notice", To: raw("group:  "), Title: "x"},
		"long group":         {Kind: "notice", To: raw("group:" + strings.Repeat("g", wire.MaxNotifyGroupLen+1)), Title: "x"},
		"empty list":         {Kind: "notice", To: raw([]string{}), Title: "x"},
		"blank logins":       {Kind: "notice", To: raw([]string{" ", ""}), Title: "x"},
		"too many logins":    {Kind: "notice", To: raw(make([]string, wire.MaxAppNotifyLogins+1)), Title: "x"},
		"no title":           {Kind: "notice", To: raw("room"), Title: "  "},
		"unknown name":       {Kind: "notice", To: raw("room"), Title: "Week {wek}"},
		"link too long":      {Kind: "notice", To: raw("room"), Title: "x", URL: strings.Repeat("u", maxNoticeURL+1)},
		"target not a shape": {Kind: "notice", To: raw(7), Title: "x"},
	} {
		if _, err := NormalizeRecipient(r); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestNoticeText(t *testing.T) {
	for _, bad := range []string{"{wek}", "open {date", "close }", "{}", "{ date }"} {
		if CheckNoticeText(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	for _, good := range []string{"", "plain", "{date} {time} {week} {room}", "a {{literal}} brace", "{{"} {
		if err := CheckNoticeText(good); err != nil && good != "{{" {
			t.Errorf("%q: %v", good, err)
		}
	}
	if err := CheckNoticeText("{nope}"); err == nil || !strings.Contains(err.Error(), "{week}") {
		t.Errorf("the refusal names the known names: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	slot := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC) // Thursday 09:00 in Shanghai, ISO week 43
	got := ExpandNotice("{room}: {week} due {date} {time} ({month}, {year}, day {day}) {{kept}}", slot, loc, "Infra team")
	want := "Infra team: 2026-W43 due 2026-10-22 09:00 (2026-10, 2026, day 22) {kept}"
	if got != want {
		t.Errorf("expanded = %q\nwant      %q", got, want)
	}
	// The week and date are the zone's, not UTC's: Sunday 23:30 UTC is Monday in Shanghai.
	sun := time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC)
	if got := ExpandNotice("{date} {week}", sun, loc, ""); got != "2026-10-26 2026-W44" {
		t.Errorf("zone: %q", got)
	}
	if got := ExpandNotice("{week}", slot, nil, ""); got != "2026-W43" {
		t.Errorf("no zone is UTC: %q", got)
	}
	// A stored text whose name was removed is sent as written, never failing a run.
	if got := ExpandNotice("hello {gone}", slot, loc, ""); got != "hello {gone}" {
		t.Errorf("unknown name: %q", got)
	}
	if !RunsAtGateway("ai") || !RunsAtGateway("notice") || RunsAtGateway("room") || RunsAtGateway("agent") {
		t.Error("RunsAtGateway")
	}
	if len(NoticeVars) < 5 {
		t.Error("the names a notice knows")
	}
}
