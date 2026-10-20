package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// save_component's room kind (docs/prd/2026-10-20-room-setup-by-conversation.md
// §2.3, docs/prd/2026-10-20-complete-room-capture.md §2.1): this room saved
// as a room component in the requester's Library. The room is captured by
// the same owner as the room's "Save as component"
// (GET /v1/rooms/{roomId}/component-draft, read through the job's grant:
// the room's owner or admins only), and saved through the content form
// like the dialog's save, so the one validator decides. The agent writes the
// listing and, when the person agreed, the room's instructions and which
// apps made here and room files the package carries (include, by artifact
// id: the gateway reads them from the room); the rest is the room as the
// draft holds it, the MCP tools its runs had required.

// contentKindRoom is save_component's kind for this room.
const contentKindRoom = "room"

// saveArgs are save_component's arguments.
type saveArgs struct {
	Dir          string       `json:"dir"`
	File         string       `json:"file"`
	ArtifactID   string       `json:"artifactId"`
	Kind         string       `json:"kind"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Category     string       `json:"category"`
	DisplayName  string       `json:"displayName"`
	Description  string       `json:"description"`
	Tags         []string     `json:"tags"`
	Request      string       `json:"request"`
	Instructions string       `json:"instructions"`
	Include      *roomInclude `json:"include"`
}

// roomInclude is save_component's include: the apps made in this room and
// the room files the package carries, by artifact id.
type roomInclude struct {
	Apps  []string `json:"apps"`
	Files []string `json:"files"`
}

// roomSave captures this room and answers the content route that saves it
// (a new component, or with id a new version of the requester's own), its
// body (the content form of a room package, and what it includes from the
// room) and what the capture left out that the agent should say.
func roomSave(ctx context.Context, env *Env, grant url.Values, id string, in saveArgs) (string, any, string, error) {
	room := env.Session.Scope().RoomID
	raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(room)+"/component-draft?"+grant.Encode(), nil)
	if err != nil {
		return "", nil, "", err
	}
	var d wire.RoomComponentDraft
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", nil, "", err
	}
	form, note := roomForm(d, in)
	p := "/v1/content-components?" + grant.Encode()
	if id != "" {
		p = "/v1/content-components/" + url.PathEscape(id) + "/versions?" + grant.Encode()
	}
	body := map[string]any{"form": form}
	if inc := in.Include; inc != nil && len(inc.Apps)+len(inc.Files) > 0 {
		body["include"] = wire.RoomInclude{RoomID: room, Apps: inc.Apps, Files: inc.Files}
	}
	return p, body, note, nil
}

// roomForm is the content form of draft d with the agent's listing and
// instructions: every part the draft holds (a part is named by its
// "name@^major"), the MCP tools its runs had (with the permission each
// needs) and the room's own asks for sources (room_setup's ask_source
// changes them). Instructions become the room's expert where it is
// installed, so they replace the room's expert in the package, which the
// note says.
func roomForm(d wire.RoomComponentDraft, in saveArgs) (contentpkg.Form, string) {
	f := contentpkg.Form{Kind: contentpkg.KindRoom, Name: firstNonEmptyString(strings.TrimSpace(in.Name), d.Name),
		DisplayName: firstNonEmptyString(strings.TrimSpace(in.DisplayName), d.DisplayName),
		Description: firstNonEmptyString(strings.TrimSpace(in.Description), d.Description),
		Category:    strings.TrimSpace(in.Category), Tags: in.Tags, Welcome: d.Welcome, Instructions: strings.TrimSpace(in.Instructions),
		Channels: d.Channels, Schedules: d.Schedules, Sources: d.Sources}
	for _, q := range d.QuickPrompts {
		f.QuickPrompts = append(f.QuickPrompts, contentpkg.QuickPrompt{Title: q.Title, Prompt: q.Prompt})
	}
	note := ""
	switch {
	case d.Expert != nil && f.Instructions != "":
		note = " The room's expert (" + d.Expert.Ref + ") is left out: the instructions you gave become the room's expert where it is installed."
	case d.Expert != nil:
		f.Requires.Expert = d.Expert.Ref
	}
	for _, s := range d.Skills {
		f.Requires.Skills = append(f.Requires.Skills, s.Ref)
	}
	for _, s := range d.Prompts {
		f.Requires.Prompts = append(f.Requires.Prompts, s.Ref)
	}
	for _, a := range d.Apps {
		f.Apps = append(f.Apps, a.Ref)
	}
	for _, t := range d.Tools {
		f.Requires.MCP = append(f.Requires.MCP, contentpkg.MCPRequirement{Name: t.Name})
		if t.Permission != "" && !slices.Contains(f.Requires.Permissions, t.Permission) {
			f.Requires.Permissions = append(f.Requires.Permissions, t.Permission)
		}
	}
	return f, note
}
