package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func promptPackForm(name string, prompts ...map[string]any) map[string]any {
	return map[string]any{"form": map[string]any{"kind": "prompt", "name": name, "displayName": "Writing", "prompts": prompts}}
}

func decodeUses(t *testing.T, raw []byte) wire.ContentUsesReply {
	t.Helper()
	var out wire.ContentUsesReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// A member creates a prompt pack by form, turns it on for themselves and
// for a room, counts uses of its prompts, and an import of a markdown file
// makes a pack; validator messages reach the caller.
func TestPromptPackCreateUseAndCount(t *testing.T) {
	g := newContentGateway(t)
	notes := map[string]any{"id": "notes", "title": "Release notes", "body": "Notes for {{audience}}", "slash": "/notes",
		"variables": []map[string]any{{"name": "audience", "label": "Audience", "options": []string{"users", "devs"}, "default": "users"}}}
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", promptPackForm("writing", notes))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	pack := decodeDetail(t, raw).Component
	if pack.Kind != "prompt" || len(pack.Prompts) != 1 || pack.Prompts[0].Slash != "/notes" || pack.Prompts[0].Variables[0].Options[1] != "devs" {
		t.Fatalf("pack = %+v", pack)
	}
	// The validator's message reaches the caller.
	bad := map[string]any{"id": "x", "title": "X", "body": "Hi {{who}}"}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components", promptPackForm("broken", bad)); code != http.StatusBadRequest || !strings.Contains(string(raw), "not declared") {
		t.Fatalf("undeclared variable = %d %s", code, raw)
	}

	// On for maya (member scope takes prompt packs) and for the room.
	if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", map[string]any{"scope": "member", "componentId": pack.ID}); code != http.StatusNoContent {
		t.Fatalf("member use = %d %s", code, raw)
	}
	if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", map[string]any{"scope": "room", "roomId": "room-1", "componentId": pack.ID}); code != http.StatusNoContent {
		t.Fatalf("room use = %d %s", code, raw)
	}

	// Uses: maya twice; jack through the room; bob without the room is
	// refused; an unknown prompt is a 404.
	for _, c := range []struct {
		user, path string
		want       int
	}{
		{"maya", "/v1/content-components/" + pack.ID + "/prompts/notes/use", http.StatusNoContent},
		{"maya", "/v1/content-components/" + pack.ID + "/prompts/notes/use", http.StatusNoContent},
		{"jack", "/v1/content-components/" + pack.ID + "/prompts/notes/use?room=room-1", http.StatusNoContent},
		{"bob", "/v1/content-components/" + pack.ID + "/prompts/notes/use", http.StatusNotFound},
		{"maya", "/v1/content-components/" + pack.ID + "/prompts/missing/use", http.StatusNotFound},
	} {
		if code, raw := g.do(c.user, http.MethodPost, c.path, "", nil); code != c.want {
			t.Fatalf("%s %s = %d %s, want %d", c.user, c.path, code, raw, c.want)
		}
	}
	code, raw = g.do("jack", http.MethodGet, "/v1/content-uses?room=room-1", "", nil)
	uses := decodeUses(t, raw)
	if code != http.StatusOK || len(uses.Room) != 1 || uses.Room[0].Component == nil || uses.Room[0].Component.Prompts[0].Uses != 3 {
		t.Fatalf("room uses = %d %s", code, raw)
	}
	code, raw = g.do("maya", http.MethodGet, "/v1/content-uses", "", nil)
	if uses := decodeUses(t, raw); code != http.StatusOK || len(uses.Member) != 1 || uses.Member[0].Kind != "prompt" {
		t.Fatalf("member uses = %d %s", code, raw)
	}

	// Import a markdown file (no archive) as a pack.
	md := "# Support\n\n## Apology\nslash: /sorry\n\nApologize to {{customer}} for the delay.\n"
	code, raw = g.do("maya", http.MethodPost, "/v1/content-components/import?kind=prompt&name=support", "text/markdown", []byte(md))
	imported := decodeDetail(t, raw).Component
	if code != http.StatusCreated || imported.Name != "support" || len(imported.Prompts) != 1 || imported.Prompts[0].Slash != "/sorry" ||
		imported.Prompts[0].Variables[0].Name != "customer" {
		t.Fatalf("import = %d %s", code, raw)
	}
	code, raw = g.do("maya", http.MethodGet, "/v1/content-components?kind=prompt", "", nil)
	var list struct{ Components []wire.ContentComponent }
	_ = json.Unmarshal(raw, &list)
	if code != http.StatusOK || len(list.Components) != 2 {
		t.Fatalf("list = %d %s", code, raw)
	}
}

// "Save as prompt": the first save creates the member's personal pack and
// turns it on for them; later saves append as new versions with unique ids
// and declared placeholders. Viewers cannot save.
func TestSavePromptPersonalPack(t *testing.T) {
	g := newContentGateway(t)
	save := func(user string, body map[string]any) (int, wire.SavePromptReply, []byte) {
		t.Helper()
		code, raw := g.json(user, http.MethodPost, "/v1/content-components/prompts", body)
		var out wire.SavePromptReply
		_ = json.Unmarshal(raw, &out)
		return code, out, raw
	}
	code, first, raw := save("maya", map[string]any{"body": "Summarize this thread for {{audience}}.\nKeep it short.", "tags": []string{"Writing"}})
	if code != http.StatusCreated || !first.Created || first.PromptID != "summarize-this-thread-for-audience" ||
		first.Component.Name != "my-prompts" || first.Component.DisplayName != "My prompts" || first.Component.LatestVersion != "1.0.0" {
		t.Fatalf("first save = %d %s", code, raw)
	}
	p := first.Component.Prompts[0]
	if len(p.Variables) != 1 || p.Variables[0].Name != "audience" || p.Variables[0].Label != "Audience" || p.Tags[0] != "writing" {
		t.Fatalf("saved prompt = %+v", p)
	}
	code, raw = g.do("maya", http.MethodGet, "/v1/content-uses", "", nil)
	if uses := decodeUses(t, raw); code != http.StatusOK || len(uses.Member) != 1 || uses.Member[0].ComponentID != first.Component.ID {
		t.Fatalf("personal pack not on for maya: %s", raw)
	}
	code, second, raw := save("maya", map[string]any{"title": "Summarize this thread for {{audience}}.", "body": "Again"})
	if code != http.StatusCreated || second.Created || second.Component.ID != first.Component.ID || second.Component.LatestVersion != "1.0.1" ||
		len(second.Component.Prompts) != 2 || second.PromptID != "summarize-this-thread-for-audience-2" {
		t.Fatalf("second save = %d %s", code, raw)
	}
	// jack's pack is his own.
	if code, other, raw := save("jack", map[string]any{"body": "Mine"}); code != http.StatusCreated || !other.Created || other.Component.ID == first.Component.ID {
		t.Fatalf("jack save = %d %s", code, raw)
	}
	if code, _, _ := save("maya", map[string]any{"body": "  "}); code != http.StatusBadRequest {
		t.Fatalf("empty save = %d", code)
	}
	if code, _, _ := save("vic", map[string]any{"body": "x"}); code != http.StatusForbidden {
		t.Fatalf("viewer save = %d", code)
	}
}

// An expert's quick prompt "pack/prompt" resolves for the viewer: maya's
// own pack for maya; for jack only through the room the pack is on in.
func TestExpertQuickPromptResolves(t *testing.T) {
	g := newContentGateway(t)
	notes := map[string]any{"id": "notes", "title": "Release notes", "body": "Write notes."}
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", promptPackForm("writing", notes))
	if code != http.StatusCreated {
		t.Fatalf("pack = %d %s", code, raw)
	}
	pack := decodeDetail(t, raw).Component
	expert := map[string]any{"form": map[string]any{"kind": "expert", "name": "writer", "instructions": "Write well.",
		"requires":     map[string]any{"prompts": []string{"writing@^1"}},
		"quickPrompts": []map[string]string{{"ref": "writing/notes"}, {"ref": "writing/absent"}}}}
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components", expert)
	if code != http.StatusCreated {
		t.Fatalf("expert = %d %s", code, raw)
	}
	ex := decodeDetail(t, raw).Component
	q := ex.QuickPrompts
	if len(q) != 2 || q[0].Resolved == nil || q[0].Resolved.ComponentID != pack.ID || q[0].Title != "Release notes" || q[1].Resolved != nil || q[1].Ref != "writing/absent" {
		t.Fatalf("maya quick prompts = %+v", q)
	}
	// Pinned in the room with the pack on there: jack reads it resolved.
	for _, id := range []string{ex.ID, pack.ID} {
		if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", map[string]any{"scope": "room", "roomId": "room-1", "componentId": id}); code != http.StatusNoContent {
			t.Fatalf("room use = %d %s", code, raw)
		}
	}
	code, raw = g.do("jack", http.MethodGet, "/v1/content-uses?room=room-1", "", nil)
	for _, u := range decodeUses(t, raw).Room {
		if u.Kind == "expert" && (u.Component == nil || u.Component.QuickPrompts[0].Resolved == nil) {
			t.Fatalf("jack's room expert = %d %s", code, raw)
		}
	}
	// A version read through the room resolves it too.
	code, raw = g.do("jack", http.MethodGet, "/v1/content-components/"+ex.ID+"/versions/1.0.0?room=room-1", "", nil)
	var vd wire.ContentVersionDetail
	_ = json.Unmarshal(raw, &vd)
	if code != http.StatusOK || vd.Component.QuickPrompts[0].Resolved == nil {
		t.Fatalf("jack version in room = %d %s", code, raw)
	}
}
