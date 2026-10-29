package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// noScheduleRuns is the rig's AI schedule runner: no run happens in these
// tests (a room package's schedules are created disabled).
type noScheduleRuns struct{}

func (noScheduleRuns) RunSchedule(context.Context, wire.RoomSchedule, time.Time, bool) (wire.ScheduleRunReply, error) {
	return wire.ScheduleRunReply{}, errors.New("no schedule runs in this test")
}

// schedule stores a schedule of maya's in room.
func (e *appGW) schedule(room, channel, text string, rcpt wire.ScheduleRecipient, rec wire.ScheduleRecurrence) {
	e.t.Helper()
	if _, err := e.b.Schedules.CreateSchedule(e.ctx(), wire.RoomSchedule{ScheduleID: ids.New("sch_"), RoomID: room, ChannelID: channel, CreatedBy: "maya",
		Text: text, Recipient: rcpt, Recurrence: rec, Timezone: "UTC", Enabled: true, CreatedAtMs: 1, UpdatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
}

// reviewRoom sets maya's room up as a room worth saving: a "reports"
// channel, the room expert (her organization's reviewer), two skill packs
// (her own charts, her organization's palettes), a prompt pack, an app
// copied in from her organization's lunch-poll work, and six schedules:
// an AI task in reports, a notice to the room and a request to jack's
// agent (kept, the request without its agent), and a message posted as
// her, a one-time task and a notice to named people (left out).
func (e *appGW) reviewRoom() {
	e.t.Helper()
	expert := e.content(store.ContentScopeOrg, contentpkg.KindExpert, "reviewer", map[string]any{})
	charts := e.content(store.ContentScopePersonal, contentpkg.KindSkill, "charts", map[string]any{"skills": []string{"charts"}})
	palettes := e.content(store.ContentScopeOrg, contentpkg.KindSkill, "palettes", map[string]any{"skills": []string{"palettes"}})
	prompts := e.content(store.ContentScopeOrg, contentpkg.KindPrompt, "review-prompts", map[string]any{"prompts": []any{map[string]any{"id": "recap", "title": "Recap", "body": "Recap"}}})
	for _, id := range []string{expert, charts, palettes, prompts} {
		c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), id)
		if err := e.b.ContentComponents.PutContentUse(e.ctx(), store.ContentUse{TenantID: e.tenant, ScopeKind: store.ContentUseRoom, ScopeID: e.roomID, ComponentID: id, Kind: c.Kind, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			e.t.Fatal(err)
		}
	}
	if r := e.call(http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: wire.ContentUseRoomExpert, RoomID: e.roomID, ComponentID: expert}, e.as("maya")); r.status != http.StatusNoContent {
		e.t.Fatalf("room expert = %d %s", r.status, r.body)
	}
	// The layout: #reports in the open section Work with its purpose,
	// rules and settings (its default agent is maya's: not captured), and
	// #private in a members-only section (not captured).
	workSec, inner := ids.New("sec_"), ids.New("sec_")
	for _, sec := range []store.RoomSection{
		{SectionID: workSec, TenantID: e.tenant, RoomID: e.roomID, Name: "Work", Visibility: wire.SectionOpen, Rules: "One thread per review.", Owners: []string{"maya"}, CreatedBy: "maya", CreatedAtMs: 1},
		{SectionID: inner, TenantID: e.tenant, RoomID: e.roomID, Name: "Leads", Position: 1, Visibility: wire.SectionMembers, Owners: []string{"maya"}, CreatedBy: "maya", CreatedAtMs: 1},
	} {
		if err := e.b.Channel.PutSection(e.ctx(), sec); err != nil {
			e.t.Fatal(err)
		}
	}
	reports := ids.New("ch_")
	for _, ch := range []store.RoomChannel{
		{ChannelID: reports, TenantID: e.tenant, RoomID: e.roomID, Name: "reports", Purpose: "The week's reports.", Rules: "One report a post.", SectionID: workSec, CreatedBy: "maya", CreatedAtMs: 1,
			Settings: wire.ChannelSettings{WhoPosts: wire.ChannelPostersAdmins, SlowModeSeconds: 30, DefaultAgent: &wire.ChannelAgent{OwnerID: "maya", Provider: "codex"}}},
		{ChannelID: ids.New("ch_"), TenantID: e.tenant, RoomID: e.roomID, Name: "private", SectionID: inner, CreatedBy: "maya", CreatedAtMs: 1},
	} {
		if err := e.b.Channel.PutChannel(e.ctx(), ch); err != nil {
			e.t.Fatal(err)
		}
	}
	daily := wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}
	e.schedule(e.roomID, reports, "Summarize yesterday.", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, daily)
	e.schedule(e.roomID, "", "Week {week}", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: json.RawMessage(`"room"`), Title: "Review", URL: "/rooms/x"}, daily)
	e.schedule(e.roomID, reports, "Status?", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "jack", Provider: "codex", Core: "acme"}, daily)
	e.schedule(e.roomID, "", "Morning, all.", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}, daily)
	e.schedule(e.roomID, "", "Once.", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, wire.ScheduleRecurrence{Kind: wire.RecurOnce, AtMs: 1})
	e.schedule(e.roomID, "", "Hi", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: json.RawMessage(`["jack"]`), Title: "Hi"}, daily)

	work := e.publishToOrg(e.publishApp("maya", e.pollSite(), wire.WorkPublishRequest{Name: "lunch-poll"}))
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("maya")); r.status != http.StatusCreated {
		e.t.Fatalf("use-app = %d %s", r.status, r.body)
	}
}

// draft is what "Save as component" captures of room as subject.
func (e *appGW) draft(subject, room string) (wire.RoomComponentDraft, resp) {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/rooms/"+room+"/component-draft", nil, e.as(subject))
	var d wire.RoomComponentDraft
	if r.status == http.StatusOK && json.Unmarshal(r.body, &d) != nil {
		e.t.Fatalf("draft = %s", r.body)
	}
	return d, r
}

// The draft holds what a package can: the channels, the AI task and the
// room notice (its link dropped), the room expert, the packs and the app
// copied from a work, each by "name@^major"; the rest is said. Only the
// room's owner or an admin captures it.
func TestRoomComponentDraft(t *testing.T) {
	e := galleryGW(t)
	e.reviewRoom()
	text := "Ask about last week."
	welcome := wire.RoomWelcomeRequest{Welcome: &text, QuickPrompts: &[]wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}}}
	if r := e.call(http.MethodPatch, "/v1/rooms/"+e.roomID+"/welcome", welcome, e.as("maya")); r.status != http.StatusOK {
		t.Fatalf("welcome = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/sources/runs", map[string]string{"purpose": reviewAsk.Purpose}, e.as("maya")); r.status != http.StatusOK {
		t.Fatalf("ask = %d %s", r.status, r.body)
	}
	for _, who := range []string{"jack", "olaf"} {
		if _, r := e.draft(who, e.roomID); r.status != http.StatusForbidden {
			t.Fatalf("draft as %s = %d %s", who, r.status, r.body)
		}
	}
	d, r := e.draft("maya", e.roomID)
	if r.status != http.StatusOK {
		t.Fatalf("draft = %d %s", r.status, r.body)
	}
	if d.Welcome != "Ask about last week." || !slices.Equal(d.QuickPrompts, []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}}) ||
		!slices.Equal(d.Sources, []wire.RoomSourceAsk{reviewAsk}) {
		t.Fatalf("draft start and sources = %q %+v %+v", d.Welcome, d.QuickPrompts, d.Sources)
	}
	if d.Name != "r" || d.DisplayName != "r" || !slices.Equal(d.Channels, []wire.RoomPackageChannel{reportsChannel}) ||
		!slices.Equal(d.Sections, []wire.RoomPackageSection{{Name: "Work", Rules: "One thread per review."}}) {
		t.Fatalf("draft layout = %+v %+v", d.Sections, d.Channels)
	}
	if !slices.ContainsFunc(d.Left, func(l string) bool { return strings.Contains(l, "(Leads)") }) ||
		!slices.ContainsFunc(d.Left, func(l string) bool { return strings.Contains(l, "default agent of #reports") }) {
		t.Fatalf("left = %v", d.Left)
	}
	kinds := map[string]wire.RoomPackageSchedule{}
	for _, sc := range d.Schedules {
		kinds[sc.Recipient.Kind] = sc
	}
	if ai, notice := kinds[wire.ScheduleRecipientAI], kinds[wire.ScheduleRecipientNotice]; len(d.Schedules) != 3 || ai.Channel != "reports" ||
		notice.Text != "Week {week}" || notice.Channel != "" || notice.Recipient.URL != "" {
		t.Fatalf("draft schedules = %+v", d.Schedules)
	}
	// The request to an agent keeps its text, recurrence and channel; its
	// agent is taken out (a package names nobody's agent).
	if ask := kinds[wire.ScheduleRecipientAgent]; ask.Text != "Status?" || ask.Channel != "reports" || ask.Recurrence.Time != "09:00" || ask.Timezone != "UTC" ||
		ask.Recipient.OwnerID != "" || ask.Recipient.Provider != "" || ask.Recipient.Core != "" {
		t.Fatalf("draft request to an agent = %+v", ask)
	}
	if d.Expert == nil || d.Expert.Ref != "reviewer@^1" || len(d.Prompts) != 1 || d.Prompts[0].Ref != "review-prompts@^1" ||
		len(d.Apps) != 1 || d.Apps[0].Ref != "lunch-poll@^1" {
		t.Fatalf("draft parts = expert %+v prompts %+v apps %+v", d.Expert, d.Prompts, d.Apps)
	}
	skills := []string{}
	for _, s := range d.Skills {
		skills = append(skills, s.Ref)
	}
	slices.Sort(skills)
	if !slices.Equal(skills, []string{"charts@^1", "palettes@^1"}) {
		t.Fatalf("draft skills = %v", skills)
	}
	left := strings.Join(d.Left, "\n")
	for _, want := range []string{"1 schedule posting as a person (", "1 schedule notifying named people", "1 schedule running once",
		"the link of 1 scheduled notice", "the agent of 1 scheduled request (whoever installs it picks one of their agents)", "members and their agents"} {
		if !strings.Contains(left, want) {
			t.Errorf("left = %q, want %q", left, want)
		}
	}
}

// A package's request to an agent installs paused and waiting for one: its
// installer can neither turn it on nor run it until they pick one of their
// agents for it (the schedules dialog's edit, realtime's route through the
// gateway), then they turn it on.
func TestInstalledRequestWaitsForAnAgent(t *testing.T) {
	e := galleryGW(t)
	e.reviewRoom()
	d, _ := e.draft("maya", e.roomID)
	pkg := e.saveRoomPackage("maya", roomForm(d, "weekly-review"))
	if r := e.call(http.MethodPost, "/v1/content-components/"+pkg.ID+"/create-room", wire.RoomInstallRequest{RoomID: "jacks-review"}, e.as("jack")); r.status != http.StatusCreated {
		t.Fatalf("create-room = %d %s", r.status, r.body)
	}
	scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), "jacks-review")
	var ask wire.RoomSchedule
	for _, sc := range scheds {
		if sc.Recipient.Kind == wire.ScheduleRecipientAgent {
			ask = sc
		}
	}
	if ask.ScheduleID == "" || ask.Text != "Status?" || !schedule.WaitsForAgent(ask.Recipient) || ask.Enabled {
		t.Fatalf("the installed request = %+v", ask)
	}
	path := "/v1/rooms/jacks-review/schedules/" + ask.ScheduleID
	if r := e.call(http.MethodPatch, path, map[string]any{"enabled": true}, e.as("jack")); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "waits for an agent") {
		t.Fatalf("turned on without an agent = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, path+"/run", nil, e.as("jack")); r.status != http.StatusConflict {
		t.Fatalf("run without an agent = %d %s", r.status, r.body)
	}
	pick := map[string]any{"recipient": wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "jack", Provider: "claude"}}
	if r := e.call(http.MethodPatch, path, pick, e.as("jack")); r.status != http.StatusOK {
		t.Fatalf("pick an agent = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPatch, path, map[string]any{"enabled": true}, e.as("jack")); r.status != http.StatusOK {
		t.Fatalf("turn it on = %d %s", r.status, r.body)
	}
	got, err := e.b.Schedules.GetSchedule(e.ctx(), "jacks-review", ask.ScheduleID)
	if err != nil || !got.Enabled || got.NextRunAtMs == 0 || got.Recipient.OwnerID != "jack" || got.Recipient.Provider != "claude" || got.Text != "Status?" {
		t.Fatalf("after the pick = %+v, %v", got, err)
	}
}

// reportsChannel is #reports as a package holds it: its default agent
// (someone's) left out.
var reportsChannel = wire.RoomPackageChannel{Name: "reports", Purpose: "The week's reports.", Rules: "One report a post.", Section: "Work",
	Settings: wire.RoomPackageChannelSettings{WhoPosts: wire.ChannelPostersAdmins, SlowModeSeconds: 30}}

// reviewAsk is the runs source a review package asks for.
var reviewAsk = wire.RoomSourceAsk{Name: wire.RoomSourceRuns, Purpose: "Judge whether the week's conversations reached their goal."}

// roomForm is the content form a member saves from a draft (every part
// ticked) as a room package, with the runs source asked for.
func roomForm(d wire.RoomComponentDraft, name string) contentpkg.Form {
	f := contentpkg.Form{Kind: "room", Name: name, DisplayName: "Weekly review", Description: "Reviews the week.",
		Welcome: "Turn the daily summary on.", Sections: d.Sections, Channels: d.Channels, Schedules: d.Schedules, Sources: []wire.RoomSourceAsk{reviewAsk},
		QuickPrompts: []contentpkg.QuickPrompt{{Title: "Weekly recap", Ref: "review-prompts/recap"}, {Title: "Misses", Prompt: "What failed this week?"}}}
	if d.Expert != nil {
		f.Requires.Expert = d.Expert.Ref
	}
	for _, p := range d.Skills {
		f.Requires.Skills = append(f.Requires.Skills, p.Ref)
	}
	for _, p := range d.Prompts {
		f.Requires.Prompts = append(f.Requires.Prompts, p.Ref)
	}
	for _, p := range d.Apps {
		f.Apps = append(f.Apps, p.Ref)
	}
	return f
}

// saveRoomPackage saves form as subject's and publishes it to their
// organization (which publishes without approval here): the
// organization's row.
func (e *appGW) saveRoomPackage(subject string, f contentpkg.Form) wire.ContentComponent {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": f}, e.as(subject))
	var saved wire.ContentComponentDetail
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &saved) != nil {
		e.t.Fatalf("save room package = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+saved.Component.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as(subject)); r.status != http.StatusCreated {
		e.t.Fatalf("publish = %d %s", r.status, r.body)
	}
	list := e.call(http.MethodGet, "/v1/content-components?kind=room", nil, e.as("jack"))
	var lib struct {
		Components []wire.ContentComponent `json:"components"`
	}
	_ = json.Unmarshal(list.body, &lib)
	for _, c := range lib.Components {
		if c.Name == f.Name && c.Scope == "org" {
			return c
		}
	}
	e.t.Fatalf("jack's Library has no %s: %s", f.Name, list.body)
	return wire.ContentComponent{}
}

// A room package saved from a draft installs as a room its installer owns:
// the description, channels, the room expert and the packs they may use
// turned on, the app copied in, the schedules created paused and theirs,
// the sources recorded as the room's asks, waiting for the organization
// admin's consent; the install counts as a use. What is not theirs to use
// is reported, never granted.
func TestCreateRoomFromComponent(t *testing.T) {
	e := galleryGW(t)
	e.reviewRoom()
	d, _ := e.draft("maya", e.roomID)
	pkg := e.saveRoomPackage("maya", roomForm(d, "weekly-review"))
	if pkg.Kind != "room" || pkg.Room == nil || !slices.Equal(pkg.Room.Sources, []wire.RoomSourceAsk{reviewAsk}) || pkg.Requires.Expert != "reviewer@^1" || len(pkg.Room.Schedules) != 3 {
		t.Fatalf("the package's view = %+v", pkg)
	}

	r := e.call(http.MethodPost, "/v1/content-components/"+pkg.ID+"/create-room", wire.RoomInstallRequest{RoomID: "jacks-review", Name: "Jack's review"}, e.as("jack"))
	var got wire.RoomInstallReply
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &got) != nil {
		t.Fatalf("create-room = %d %s", r.status, r.body)
	}
	if got.RoomID != "jacks-review" || got.From != "weekly-review@1.0.0" || !slices.Equal(got.Channels, []string{"reports"}) || !slices.Equal(got.Sections, []string{"Work"}) || got.Expert != "Reviewer" ||
		!slices.Equal(got.Sources, []wire.RoomSourceAsk{reviewAsk}) || len(got.Apps) != 1 || len(got.Schedules) != 3 ||
		!slices.Equal(got.WaitingForAgent, []string{"Status?"}) {
		t.Fatalf("reply = %+v", got)
	}
	if !slices.Equal(got.Skipped, []string{"skill pack charts@^1 (not one you can use)"}) {
		t.Fatalf("skipped = %v", got.Skipped)
	}
	room, err := e.b.Room.GetRoom(e.ctx(), "jacks-review")
	if err != nil || room.Name != "Jack's review" || room.Description != "Reviews the week." || room.Welcome != "Turn the daily summary on." ||
		!slices.Equal(room.QuickPrompts, []wire.RoomQuickPrompt{{Title: "Weekly recap", Prompt: "Recap"}, {Title: "Misses", Prompt: "What failed this week?"}}) {
		t.Fatalf("the room = %+v, %v", room, err)
	}
	if role, ok, _ := e.b.Room.MemberRole(e.ctx(), "jacks-review", "jack"); !ok || role != store.RoleOwner {
		t.Fatalf("jack's role = %q %v", role, ok)
	}
	if _, ok, _ := e.b.Room.MemberRole(e.ctx(), "jacks-review", "maya"); ok {
		t.Fatal("the package's author was added to the installer's room")
	}
	channels, _ := e.b.Channel.ListChannels(e.ctx(), "jacks-review")
	sections, _ := e.b.Channel.ListSections(e.ctx(), "jacks-review")
	if len(sections) != 1 || sections[0].Name != "Work" || sections[0].Rules != "One thread per review." || sections[0].Visibility != wire.SectionOpen ||
		!slices.Equal(sections[0].Owners, []string{"jack"}) {
		t.Fatalf("the new room's sections = %+v", sections)
	}
	reports := ""
	for _, c := range channels {
		if c.Name == "reports" {
			reports = c.ChannelID
			// The package's setup, made as jack: his channel, in Work, no
			// default agent.
			if c.Purpose != reportsChannel.Purpose || c.Rules != reportsChannel.Rules || c.SectionID != sections[0].SectionID || c.OwnerID != "jack" ||
				!reflect.DeepEqual(c.Settings, wire.ChannelSettings{WhoPosts: wire.ChannelPostersAdmins, SlowModeSeconds: 30}) {
				t.Fatalf("#reports in the new room = %+v", c)
			}
		}
	}
	scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), "jacks-review")
	if len(scheds) != 3 || reports == "" {
		t.Fatalf("schedules = %+v (reports %q)", scheds, reports)
	}
	for _, sc := range scheds {
		if sc.Enabled || sc.CreatedBy != "jack" || sc.NextRunAtMs != 0 {
			t.Fatalf("a schedule not paused or not jack's: %+v", sc)
		}
		if want := map[string]string{wire.ScheduleRecipientAI: reports, wire.ScheduleRecipientNotice: "", wire.ScheduleRecipientAgent: reports}[sc.Recipient.Kind]; sc.ChannelID != want {
			t.Fatalf("the %s schedule is in channel %q, want %q", sc.Recipient.Kind, sc.ChannelID, want)
		}
		if sc.Recipient.Kind == wire.ScheduleRecipientAgent && !schedule.WaitsForAgent(sc.Recipient) {
			t.Fatalf("the request names an agent the package carried: %+v", sc.Recipient)
		}
	}
	uses, _ := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoom, "jacks-review")
	on := []string{}
	for _, u := range uses {
		c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), u.ComponentID)
		on = append(on, c.Name)
	}
	slices.Sort(on)
	if !slices.Equal(on, []string{"palettes", "review-prompts", "reviewer"}) {
		t.Fatalf("turned on = %v", on)
	}
	if expert, _ := e.gw.roomExpertOf(e.ctx(), "jacks-review"); expert == "" {
		t.Fatal("no room expert")
	}
	apps, _ := e.b.Artifacts.ListArtifacts(e.ctx(), store.ArtifactQuery{RoomID: "jacks-review", Limit: 10})
	if len(apps) != 1 || apps[0].Kind != store.ArtifactKindSite || apps[0].CreatedBy != "jack" {
		t.Fatalf("the room's apps = %+v", apps)
	}
	if installs, _ := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoomInstall, "jacks-review"); len(installs) != 1 || installs[0].ComponentID != pkg.ID {
		t.Fatalf("install uses = %+v", installs)
	}
	ask, err := e.b.RoomSources.GetRoomSourceGrant(e.ctx(), "jacks-review", wire.RoomSourceRuns)
	if err != nil || ask.Scope != "" || ask.Purpose != reviewAsk.Purpose || ask.ComponentID != pkg.ID || ask.ComponentVersion != "1.0.0" || ask.AskedBy != "jack" || len(ask.AliasKey) != 32 {
		t.Fatalf("the room's ask = %+v, %v (waiting, the package's purpose)", ask, err)
	}

	// The id is taken now; a work is not a room package; someone else's
	// personal package is not there.
	if r := e.call(http.MethodPost, "/v1/content-components/"+pkg.ID+"/create-room", wire.RoomInstallRequest{RoomID: "jacks-review"}, e.as("jack")); r.status != http.StatusConflict {
		t.Fatalf("a taken room id = %d %s", r.status, r.body)
	}
	work := e.libraryApp("jack", "lunch-poll")
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/create-room", nil, e.as("jack")); r.status != http.StatusBadRequest {
		t.Fatalf("a work = %d %s", r.status, r.body)
	}
	mine := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": contentpkg.Form{Kind: "room", Name: "private-room"}}, e.as("maya"))
	var private wire.ContentComponentDetail
	_ = json.Unmarshal(mine.body, &private)
	if r := e.call(http.MethodPost, "/v1/content-components/"+private.Component.ID+"/create-room", nil, e.as("jack")); r.status != http.StatusNotFound {
		t.Fatalf("maya's personal package as jack = %d %s", r.status, r.body)
	}
}
