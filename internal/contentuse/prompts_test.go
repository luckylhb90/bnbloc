package contentuse

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func promptPack(version string, prompts ...map[string]any) map[string]any {
	return map[string]any{"version": version, "prompts": prompts}
}

// An expert's quick prompt "pack/prompt" resolves, as the viewer, to the
// highest version satisfying the expert's requires.prompts among the packs
// they may use and the room's (a room pin fixes its version).
func TestResolvePromptRef(t *testing.T) {
	f := fixture{t: t, st: store.NewMemoryContentComponents()}
	ctx := context.Background()
	notes := func(title string) map[string]any {
		return map[string]any{"id": "notes", "title": title, "body": "Notes for {{who}}", "variables": []map[string]any{{"name": "who", "options": []string{"users", "devs"}}}}
	}
	f.put("pp-maya", contentpkg.KindPrompt, "writing", "maya", promptPack("1.0.0", notes("v1")), promptPack("1.1.0", notes("v1.1")), promptPack("2.0.0", notes("v2")))
	f.put("pp-jack", contentpkg.KindPrompt, "writing", "jack", promptPack("1.5.0", notes("jack")))
	req := []string{"writing@^1"}

	got, err := ResolvePromptRef(ctx, f.st, viewer("maya"), "", req, "writing/notes")
	if err != nil || got == nil || got.ComponentID != "pp-maya" || got.Version != "1.1.0" || got.Prompt.Title != "v1.1" || got.Prompt.Variables[0].Options[1] != "devs" {
		t.Fatalf("maya = %+v, %v", got, err)
	}
	// bob may use neither pack; in the room jack's pack is turned on.
	if got, err := ResolvePromptRef(ctx, f.st, viewer("bob"), "", req, "writing/notes"); err != nil || got != nil {
		t.Fatalf("bob = %+v, %v", got, err)
	}
	f.use(store.ContentUseRoom, "room-1", "pp-jack", contentpkg.KindPrompt, "")
	if got, err := ResolvePromptRef(ctx, f.st, viewer("bob"), "room-1", req, "writing/notes"); err != nil || got == nil || got.ComponentID != "pp-jack" || got.Prompt.Title != "jack" {
		t.Fatalf("bob in room = %+v, %v", got, err)
	}
	// Unknown prompt, a pack the expert does not require, a bad ref.
	for _, c := range []struct {
		req []string
		ref string
	}{{req, "writing/missing"}, {nil, "writing/notes"}, {req, "writing"}, {[]string{"writing@^3"}, "writing/notes"}} {
		if got, err := ResolvePromptRef(ctx, f.st, viewer("maya"), "", c.req, c.ref); err != nil || got != nil {
			t.Fatalf("%v %s = %+v, %v", c.req, c.ref, got, err)
		}
	}
}
