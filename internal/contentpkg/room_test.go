package contentpkg

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// aiTask is a room package schedule: a daily AI task in the room's default
// channel.
func aiTask() map[string]any {
	return map[string]any{"text": "Summarize yesterday's work.", "recipient": map[string]any{"kind": wire.ScheduleRecipientAI},
		"recurrence": map[string]any{"kind": wire.RecurDaily, "time": "09:00"}, "timezone": "UTC"}
}

// runsAsk asks for the runs source with a purpose.
func runsAsk() map[string]any {
	return map[string]any{"name": wire.RoomSourceRuns, "purpose": "Judge whether conversations reached their goal."}
}

// roomBundle is a room package with a channel, an AI task, a room expert,
// a prompt pack, an app and a source; extra manifest fields are set or
// removed (nil), and files added or removed ("").
func roomBundle(t *testing.T, extra map[string]any, files map[string]string) []byte {
	t.Helper()
	m := map[string]any{"kind": KindRoom, "name": "weekly-review", "version": "1.0.0", "displayName": "Weekly review",
		"description": "Reviews the week's work.", "welcome": "Turn the schedule on to start.",
		"quickPrompts": []map[string]any{{"title": "Plan", "prompt": "Plan the week."}, {"ref": "review-prompts/recap"}},
		"sections":     []map[string]any{{"name": "Work", "rules": "One thread per review."}},
		"channels":     []map[string]any{chanOf("reports", "Work")}, "schedules": []map[string]any{aiTask()},
		"requires": map[string]any{"expert": "reviewer@^1", "skills": []string{"charts@^2"}, "prompts": []string{"review-prompts@^1"}},
		"apps":     []string{"triage-board@^1"}, "sources": []map[string]any{runsAsk()}}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	all := map[string][]byte{ManifestFile: manifestJSON(t, m)}
	for k, v := range files {
		if v == "" {
			delete(all, k)
			continue
		}
		all[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(all, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRoomPackageValidation(t *testing.T) {
	p, err := Validate(roomBundle(t, nil, map[string]string{ReadmeFile: "# Weekly review\n"}), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	spec := p.Manifest.RoomSpec()
	if spec == nil || spec.Channels[0].Name != "reports" || spec.Channels[0].Section != "Work" || spec.Channels[0].Settings.SlowModeSeconds != 30 ||
		spec.Sections[0] != (wire.RoomPackageSection{Name: "Work", Rules: "One thread per review."}) || spec.Schedules[0].Recipient.Kind != wire.ScheduleRecipientAI ||
		spec.Apps[0] != "triage-board@^1" || spec.Sources[0] != (wire.RoomSourceAsk{Name: wire.RoomSourceRuns, Purpose: "Judge whether conversations reached their goal."}) || p.Manifest.Requires.Expert != "reviewer@^1" {
		t.Fatalf("room spec = %+v (requires %+v)", spec, p.Manifest.Requires)
	}
	notice := map[string]any{"text": "Week {week}: the review is due.", "recurrence": map[string]any{"kind": wire.RecurWeekly, "time": "08:30", "days": []int{1}},
		"timezone": "Europe/Berlin", "channel": "reports",
		"recipient": map[string]any{"kind": wire.ScheduleRecipientNotice, "to": wire.AppNotifyRoom, "title": "Weekly review"}}
	if _, err := Validate(roomBundle(t, map[string]any{"schedules": []map[string]any{aiTask(), notice}}, nil), "", Options{}); err != nil {
		t.Fatalf("an AI task and a notice to the room: %v", err)
	}
	// A request to an agent is held without its agent: the installer picks one.
	ask := map[string]any{"text": "What changed since yesterday?", "recipient": map[string]any{"kind": wire.ScheduleRecipientAgent},
		"recurrence": map[string]any{"kind": wire.RecurWeekly, "time": "09:30", "days": []int{1, 2, 3, 4, 5}}, "timezone": "Asia/Shanghai", "channel": "reports"}
	if p, err := Validate(roomBundle(t, map[string]any{"schedules": []map[string]any{ask}}, nil), "", Options{}); err != nil ||
		p.Manifest.Schedules[0].Recipient.Kind != wire.ScheduleRecipientAgent || p.Manifest.Schedules[0].Recipient.OwnerID != "" {
		t.Fatalf("a request to an agent without its agent: %v", err)
	}
	if _, err := Validate(roomBundle(t, map[string]any{"schedules": nil, "requires": nil, "apps": nil, "sources": nil, "channels": nil, "quickPrompts": nil}, nil), "", Options{}); err != nil {
		t.Fatalf("a room package of only a description: %v", err)
	}
	with := func(field string, edit func(map[string]any)) []map[string]any {
		s := aiTask()
		edit(s)
		return []map[string]any{s}
	}
	bad := []struct {
		name  string
		extra map[string]any
		files map[string]string
		want  string
	}{
		{"a schedule that posts as a person", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientRoom}
		})}, nil, "one that posts as a person is not"},
		{"a schedule that names no recipient", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{}
		})}, nil, "one that posts as a person is not"},
		{"a request naming its agent", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientAgent, "ownerId": "maya", "provider": "codex"}
		})}, nil, "names no agent"},
		{"a request naming only a core", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientAgent, "core": "acme"}
		})}, nil, "names no agent"},
		{"a request without text", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientAgent}
			s["text"] = ""
		})}, nil, "text is required"},
		{"a one-time request", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientAgent}
			s["recurrence"] = map[string]any{"kind": wire.RecurOnce, "atMs": 1}
		})}, nil, "recurs"},
		{"a notice to named people", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientNotice, "to": []string{"maya"}, "title": "Hi"}
		})}, nil, "names no people"},
		{"a notice to a group", map[string]any{"schedules": with("recipient", func(s map[string]any) {
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientNotice, "to": "group:team", "title": "Hi"}
		})}, nil, "names no people"},
		{"an unknown notice name", map[string]any{"schedules": with("text", func(s map[string]any) {
			s["text"] = "{wek}"
			s["recipient"] = map[string]any{"kind": wire.ScheduleRecipientNotice, "to": wire.AppNotifyRoom, "title": "Hi"}
		})}, nil, "not a name a notice knows"},
		{"an AI task without text", map[string]any{"schedules": with("text", func(s map[string]any) { s["text"] = " " })}, nil, "text is required"},
		{"a one-time run", map[string]any{"schedules": with("recurrence", func(s map[string]any) {
			s["recurrence"] = map[string]any{"kind": wire.RecurOnce, "atMs": 1}
		})}, nil, "recurs"},
		{"no timezone", map[string]any{"schedules": with("timezone", func(s map[string]any) { delete(s, "timezone") })}, nil, "timezone"},
		{"a channel not listed", map[string]any{"schedules": with("channel", func(s map[string]any) { s["channel"] = "elsewhere" })}, nil, "not one of the package's channels"},
		{"the default channel listed", map[string]any{"channels": []map[string]any{chanOf("General", "")}}, nil, "every room has it"},
		{"a channel twice", map[string]any{"channels": []map[string]any{chanOf("reports", ""), chanOf("Reports", "")}}, nil, "twice"},
		{"a padded channel", map[string]any{"channels": []map[string]any{chanOf(" reports", "")}}, nil, "surrounding spaces"},
		{"too many channels", map[string]any{"channels": chans(names("c", maxRoomChannels+1)), "schedules": nil}, nil, "at most"},
		{"a channel by name alone", map[string]any{"channels": []string{"reports"}}, nil, "channels"},
		{"a channel in a section not made", map[string]any{"channels": []map[string]any{chanOf("reports", "Elsewhere")}}, nil, "not one of the package's sections"},
		{"a purpose past its bound", map[string]any{"channels": []map[string]any{{"name": "reports", "purpose": strings.Repeat("p", wire.MaxChannelPurpose+1)}}}, nil, "longer than"},
		{"a purpose on two lines", map[string]any{"channels": []map[string]any{{"name": "reports", "purpose": "a\nb"}}}, nil, "one line"},
		{"channel rules past their bound", map[string]any{"channels": []map[string]any{{"name": "reports", "rules": strings.Repeat("r", wire.MaxChannelRules+1)}}}, nil, "longer than"},
		{"an unknown poster", map[string]any{"channels": []map[string]any{{"name": "reports", "settings": map[string]any{"whoPosts": "owners"}}}}, nil, "whoPosts"},
		{"a negative slow mode", map[string]any{"channels": []map[string]any{{"name": "reports", "settings": map[string]any{"slowModeSeconds": -1}}}}, nil, "slowModeSeconds"},
		{"a section twice", map[string]any{"sections": []map[string]any{{"name": "Work"}, {"name": "work"}}}, nil, "twice"},
		{"a padded section", map[string]any{"sections": []map[string]any{{"name": "Work "}}, "channels": nil, "schedules": nil}, nil, "surrounding spaces"},
		{"section rules past their bound", map[string]any{"sections": []map[string]any{{"name": "Work", "rules": strings.Repeat("r", wire.MaxChannelRules+1)}}}, nil, "longer than"},
		{"too many sections", map[string]any{"sections": sects(names("s", maxRoomSections+1)), "channels": nil, "schedules": nil}, nil, "at most"},
		{"an unknown source", map[string]any{"sources": []map[string]any{{"name": "mail", "purpose": "x"}}}, nil, "source \"mail\""},
		{"a source twice", map[string]any{"sources": []map[string]any{runsAsk(), runsAsk()}}, nil, "twice"},
		{"a source without its purpose", map[string]any{"sources": []map[string]any{{"name": wire.RoomSourceRuns, "purpose": " "}}}, nil, "say its purpose"},
		{"a purpose past its bound", map[string]any{"sources": []map[string]any{{"name": wire.RoomSourceRuns, "purpose": strings.Repeat("p", wire.MaxRoomSourcePurpose+1)}}}, nil, "longer than"},
		{"a source as a bare name", map[string]any{"sources": []string{wire.RoomSourceRuns}}, nil, "sources"},
		{"a bad app reference", map[string]any{"apps": []string{"Triage Board"}}, nil, "apps"},
		{"an app twice", map[string]any{"apps": []string{"board@^1", "board@^2"}}, nil, "twice"},
		{"a bad expert reference", map[string]any{"requires": map[string]any{"expert": "reviewer@nope"}}, nil, "requires.expert"},
		{"a quick prompt of a pack not required", map[string]any{"quickPrompts": []map[string]any{{"ref": "other/x"}}}, nil, "requires.prompts"},
		{"a description past a room's", map[string]any{"description": strings.Repeat("d", maxRoomDescription+1)}, nil, "room's description"},
		{"scripts", map[string]any{"requires": map[string]any{"permissions": []string{PermScripts}}}, nil, "no scripts"},
		{"skills", map[string]any{"skills": []string{"x"}}, nil, "skills"},
		{"an extra file", nil, map[string]string{"notes.txt": "x"}, "a room package holds"},
		{"instructions beside an expert", nil, map[string]string{InstructionsFile: "Review the week."}, "not both"},
	}
	for _, c := range bad {
		_, err := Validate(roomBundle(t, c.extra, c.files), "", Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// chanOf is a package channel in a section ("" the top level), with a
// purpose, rules and slow mode.
func chanOf(name, section string) map[string]any {
	return map[string]any{"name": name, "purpose": "The week's reports.", "rules": "One report a post.", "section": section,
		"settings": map[string]any{"slowModeSeconds": 30}}
}

// chans and sects are package channels and sections by name.
func chans(ns []string) []map[string]any {
	out := make([]map[string]any, len(ns))
	for i, n := range ns {
		out[i] = map[string]any{"name": n}
	}
	return out
}

func sects(ns []string) []map[string]any { return chans(ns) }

// names are n distinct names with a prefix.
func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strings.Repeat("x", i+1)
	}
	return out
}

// A room's fields belong to the room kind: no other kind carries them,
// from a manifest or a form.
func TestRoomFieldsOnlyInRoomPackages(t *testing.T) {
	for _, extra := range []map[string]any{
		{"channels": []map[string]any{chanOf("reports", "")}},
		{"sections": []map[string]any{{"name": "Work"}}},
		{"sources": []map[string]any{runsAsk()}},
		{"apps": []string{"board@^1"}},
		{"schedules": []map[string]any{aiTask()}},
		{"requires": map[string]any{"expert": "reviewer@^1"}},
	} {
		if _, err := Validate(assistBundle(t, extra, nil), "", Options{}); err == nil || !strings.Contains(err.Error(), "belong to a room package") {
			t.Errorf("an assist with %v: err = %v", extra, err)
		}
	}
	f := Form{Kind: "expert", Name: "x", Instructions: "Do it.", Channels: []wire.RoomPackageChannel{{Name: "reports"}}}
	if _, _, err := Build(f, "1.0.0", Options{}, nil); err == nil || !strings.Contains(err.Error(), "belong to a room package") {
		t.Errorf("an expert form with channels: err = %v", err)
	}
}

// The form lays a room package out as its manifest alone.
func TestRoomPackageFromForm(t *testing.T) {
	f := Form{Kind: "room", Name: "weekly-review", Description: "Reviews the week.", Welcome: " Hello ",
		Requires: Requires{Expert: "reviewer@^1"}, Sections: []wire.RoomPackageSection{{Name: " Work ", Rules: " Be brief. "}},
		Channels: []wire.RoomPackageChannel{{Name: " reports ", Purpose: " The reports. ", Section: " Work ", Settings: wire.RoomPackageChannelSettings{WhoPosts: wire.ChannelPostersAdmins}}}, Sources: []wire.RoomSourceAsk{{Name: wire.RoomSourceRuns, Purpose: " Review outcomes. "}},
		Schedules: []wire.RoomPackageSchedule{{Text: "Go.", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}, Timezone: "UTC", Channel: "reports"}}}
	p, _, err := Build(f, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Bundle.Paths(); len(got) != 1 || got[0] != ManifestFile {
		t.Fatalf("files = %v", got)
	}
	if m := p.Manifest; m.Welcome != "Hello" || m.Channels[0] != (wire.RoomPackageChannel{Name: "reports", Purpose: "The reports.", Section: "Work", Settings: wire.RoomPackageChannelSettings{WhoPosts: wire.ChannelPostersAdmins}}) ||
		m.Sections[0] != (wire.RoomPackageSection{Name: "Work", Rules: "Be brief."}) || m.Requires.Expert != "reviewer@^1" || len(m.Schedules) != 1 || m.Sources[0].Purpose != "Review outcomes." {
		t.Fatalf("manifest = %+v", m)
	}
	f.Instructions = "Be a reviewer."
	if _, _, err := Build(f, "1.0.0", Options{}, nil); err == nil {
		t.Error("a room form with instructions and a room expert saved")
	}
}

func TestMajorRef(t *testing.T) {
	for in, want := range map[[2]string]string{{"charts", "2.3.1"}: "charts@^2", {"charts", "0.4.0"}: "charts@^0", {"charts", "x"}: "charts"} {
		if got := MajorRef(in[0], in[1]); got != want {
			t.Errorf("MajorRef(%v) = %q, want %q", in, got, want)
		}
	}
}
