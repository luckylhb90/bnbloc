package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The Library as an agent reads it: the Library of the person who asked
// (a job's requester), read through the member catalog with the job's grant
// (GET /v1/components?job=&room=: the gateway acts as the requester and
// admits exactly what contentuse.Visible and extpipe.MemberVisibility admit;
// nothing here re-derives visibility). The room tools' list_components and
// the bridge's .room/library.md both read it through ReadLibrary.

// MaxLibraryItems bounds .room/library.md: the most recently updated items.
const MaxLibraryItems = 50

// LibraryKindMCP is an MCP tool's kind in the Library (the content kinds
// are their short names: expert, skill, prompt).
const LibraryKindMCP = "mcp"

// LibraryItem is one component of the Library.
type LibraryItem struct {
	ID          string `json:"id,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
	// Scope is a content component's scope (personal, org, platform,
	// shared); an MCP tool is the platform's.
	Scope string `json:"scope"`
	// Mine marks the requester's own component.
	Mine        bool  `json:"mine,omitempty"`
	UpdatedAtMs int64 `json:"updatedAtMs,omitempty"`
}

// ReadLibrary reads the Library of job's requester in room, most recently
// updated first. kind narrows it to one kind ("" = every kind).
func ReadLibrary(ctx context.Context, fetch Fetch, room, job, kind string) ([]LibraryItem, error) {
	q := url.Values{"job": {job}, "room": {room}}
	if kind != "" {
		q.Set("kind", kind)
	}
	raw, err := fetch(ctx, "/v1/components?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var reply wire.ComponentsReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("unreadable Library listing: %w", err)
	}
	items := make([]LibraryItem, 0, len(reply.Components)+len(reply.ContentComponents))
	for _, c := range reply.ContentComponents {
		items = append(items, LibraryItem{ID: c.ID, Kind: c.Kind, Name: c.Name, DisplayName: c.DisplayName, Description: c.Description,
			Version: c.LatestVersion, Scope: c.Scope, Mine: c.Mine, UpdatedAtMs: c.UpdatedAtMs})
	}
	for _, c := range reply.Components {
		if c.Kind != LibraryKindMCP {
			continue
		}
		items = append(items, LibraryItem{Kind: c.Kind, Name: c.Name, Description: c.Description, Version: c.Version,
			Scope: "platform", UpdatedAtMs: c.PublishedAtMs})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAtMs != items[j].UpdatedAtMs {
			return items[i].UpdatedAtMs > items[j].UpdatedAtMs
		}
		return items[i].Kind+"/"+items[i].Name < items[j].Kind+"/"+items[j].Name
	})
	return items, nil
}

// LibraryFile renders .room/library.md: a read-only snapshot of the
// requester's Library (name, kind, version, scope, updated time), the
// MaxLibraryItems most recently updated. A failed read (err) says so instead
// of claiming the Library is empty.
func LibraryFile(items []LibraryItem, err error) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("The Library of the person who asked you"))
	b.WriteString("\n")
	b.WriteString("bnbloc's Library: experts, skill packs, prompt packs and MCP tools this person may use. One Library, shared by\n")
	b.WriteString("every agent and core in the room and visible to the person; a component an agent saved for them is here.\n")
	b.WriteString("Read-only snapshot from this turn, most recently updated first (list_components and get_component read it live).\n\n")
	switch {
	case err != nil:
		b.WriteString("(the Library could not be read this turn; this is not a sign that it is empty)\n")
	case len(items) == 0:
		b.WriteString("(empty)\n")
	}
	shown := items
	if len(shown) > MaxLibraryItems {
		shown = shown[:MaxLibraryItems]
	}
	for _, it := range shown {
		name := it.Name
		if it.DisplayName != "" && it.DisplayName != it.Name {
			name += " (" + it.DisplayName + ")"
		}
		scope := it.Scope
		if it.Mine {
			scope += ", theirs"
		}
		updated := "unknown"
		if it.UpdatedAtMs > 0 {
			updated = time.UnixMilli(it.UpdatedAtMs).UTC().Format("2006-01-02 15:04 UTC")
		}
		fmt.Fprintf(&b, "- %s · %s · v%s · %s · updated %s\n", name, it.Kind, it.Version, scope, updated)
	}
	if n := len(items) - len(shown); n > 0 {
		fmt.Fprintf(&b, "\n(%d older items not listed)\n", n)
	}
	return NamedFile{Name: ".room/library.md", Content: b.String()}
}
