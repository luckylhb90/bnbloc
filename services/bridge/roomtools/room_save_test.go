package roomtools

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// roomDraftJSON is what the gateway's component-draft answers for room-1.
var roomDraftJSON, _ = json.Marshal(wire.RoomComponentDraft{RoomID: "room-1", Name: "r", DisplayName: "r", Description: "Reviews.",
	Welcome: "Turn the task on.", QuickPrompts: []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}},
	Channels: []string{"reports"}, Schedules: []wire.RoomPackageSchedule{{Channel: "reports", Text: "Summarize.", Recipient: wire.ScheduleRecipient{Kind: "ai"},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}, Timezone: "UTC"}},
	Expert: &wire.RoomDraftPart{Ref: "reviewer@^1"}, Skills: []wire.RoomDraftPart{{Ref: "charts@^1"}}, Prompts: []wire.RoomDraftPart{{Ref: "recaps@^2"}},
	Apps: []wire.RoomDraftPart{{Ref: "lunch-poll@^1"}}, Sources: []wire.RoomSourceAsk{{Name: "runs", Purpose: "Judge outcomes."}},
	Tools: []wire.RoomDraftTool{{Name: "browser", Permission: "mcp.stdio"}, {Name: "search", Permission: "mcp.http:tools.example.com"}}})

// What the agent and the person agreed rides the save: the instructions
// (which replace the room's expert in the package, said in the note) and
// the apps made here and room files to carry, by id, as the save's include
// of this room; the tools its runs had are required with their
// permissions. Neither belongs to another kind's save.
func TestSaveComponentRoomCarries(t *testing.T) {
	gw := &recordingGateway{reply: func(method, p string) ([]byte, error) {
		if method == "GET" {
			return roomDraftJSON, nil
		}
		return []byte(`{"component":{"id":"cc_9","kind":"room","name":"weekly-review","latestVersion":"1.0.0","mine":true}}`), nil
	}}
	res := saveComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(
		`{"kind":"room","instructions":" Review the week in three bullets. ","include":{"apps":["art_board"],"files":["art_faq","art_tpl"]}}`))
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	body := gw.calls[1].Body.(map[string]any)
	f := body["form"].(contentpkg.Form)
	if f.Instructions != "Review the week in three bullets." || f.Requires.Expert != "" ||
		len(f.Requires.MCP) != 2 || f.Requires.MCP[1].Name != "search" || strings.Join(f.Requires.Permissions, ",") != "mcp.stdio,mcp.http:tools.example.com" {
		t.Fatalf("form = %+v", f)
	}
	if inc, _ := body["include"].(wire.RoomInclude); inc.RoomID != "room-1" || strings.Join(inc.Apps, ",") != "art_board" || strings.Join(inc.Files, ",") != "art_faq,art_tpl" {
		t.Fatalf("include = %+v", body["include"])
	}
	if note := res.Value.(map[string]any)["note"].(string); !strings.Contains(note, "reviewer@^1) is left out") {
		t.Fatalf("note = %s", note)
	}
	gw.calls = nil
	if res := saveComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"kind":"room"}`)); res.Err != "" {
		t.Fatal(res.Err)
	}
	if body := gw.calls[1].Body.(map[string]any); body["include"] != nil || body["form"].(contentpkg.Form).Requires.Expert != "reviewer@^1" {
		t.Fatalf("a plain save = %+v", body)
	}
	if res := saveComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"artifactId":"art_x","instructions":"x"}`)); !strings.Contains(res.Err, "are a room's") {
		t.Fatalf("instructions on a work = %q", res.Err)
	}
}

// save_component's room kind captures this room through component-draft
// (the dialog's capture) under the job's grant and saves the content form
// of it with the agent's listing; with id it is a new version; sources
// default to the room's own asks.
func TestSaveComponentRoom(t *testing.T) {
	gw := &recordingGateway{reply: func(method, p string) ([]byte, error) {
		if method == "GET" {
			return roomDraftJSON, nil
		}
		return []byte(`{"component":{"id":"cc_9","kind":"room","name":"weekly-review","latestVersion":"1.0.0","mine":true}}`), nil
	}}
	res := saveComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(
		`{"kind":"room","name":"weekly-review","displayName":"Weekly review","description":"Reviews the week's conversations.","category":"review","tags":["review"]}`))
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	if len(gw.calls) != 2 || !strings.HasPrefix(gw.calls[0].Path, "/v1/rooms/room-1/component-draft?") || !strings.HasPrefix(gw.calls[1].Path, "/v1/content-components?") {
		t.Fatalf("calls = %+v", gw.calls)
	}
	for _, c := range gw.calls {
		if u, _ := url.Parse(c.Path); u.Query().Get("job") != "job-1" || u.Query().Get("room") != "room-1" {
			t.Fatalf("%s without the job's grant", c.Path)
		}
	}
	f := gw.calls[1].Body.(map[string]any)["form"].(contentpkg.Form)
	if f.Kind != contentpkg.KindRoom || f.Name != "weekly-review" || f.DisplayName != "Weekly review" || f.Description != "Reviews the week's conversations." ||
		f.Category != "review" || len(f.Tags) != 1 || f.Welcome != "Turn the task on." || len(f.QuickPrompts) != 1 || f.QuickPrompts[0].Prompt != "Recap the week" ||
		f.Requires.Expert != "reviewer@^1" || f.Requires.Skills[0] != "charts@^1" || f.Requires.Prompts[0] != "recaps@^2" ||
		f.Channels[0] != "reports" || len(f.Schedules) != 1 || f.Apps[0] != "lunch-poll@^1" || len(f.Sources) != 1 || f.Sources[0].Purpose != "Judge outcomes." {
		t.Fatalf("form = %+v", f)
	}
	if note := res.Value.(map[string]any)["note"].(string); !strings.Contains(note, "room component weekly-review") || !strings.Contains(note, "with a click") {
		t.Fatalf("note = %s", note)
	}

	// A new version of their own keeps the draft's listing where the agent
	// gives none.
	gw.calls = nil
	if res := saveComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"kind":"room","id":"cc_9"}`)); res.Err != "" {
		t.Fatal(res.Err)
	}
	if !strings.HasPrefix(gw.calls[1].Path, "/v1/content-components/cc_9/versions?") {
		t.Fatalf("new version path = %s", gw.calls[1].Path)
	}
	if f := gw.calls[1].Body.(map[string]any)["form"].(contentpkg.Form); f.Name != "r" || f.Description != "Reviews." || len(f.Sources) != 1 {
		t.Fatalf("a version keeps the draft's listing: %+v", f)
	}

	// The capture's refusal (not the room's owner or an admin) comes back.
	refused := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 403, Body: []byte(`{"error":"only the room's owner or admins save it as a component"}`)}
	}}
	if res := saveComponent(context.Background(), contentEnv(nil, refused, true), json.RawMessage(`{"kind":"room"}`)); !strings.Contains(res.Err, "only the room's owner or admins") || len(refused.calls) != 1 {
		t.Fatalf("refused = %q, calls %d", res.Err, len(refused.calls))
	}
}
