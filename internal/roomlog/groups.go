package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The group chats a room's members connected to it
// (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §5a), as an agent
// reads them. An app's `app.notify({to: "group:<name>"})` and a scheduled task
// name a group by the name its members gave it; the agent that writes the app
// has no other way to learn the names, so .room/groups.md lists them: the
// name and the notifier that delivers there, never the chat's own address.

// ReadGroups lists the room's connected group chats, read as the room's member
// (the bridge's own session). An agent host that is not a member gets an error.
func ReadGroups(ctx context.Context, fetch Fetch, room string) ([]wire.RoomGroup, error) {
	raw, err := fetch(ctx, "/v1/rooms/"+url.PathEscape(room)+"/groups")
	if err != nil {
		return nil, err
	}
	var out wire.RoomGroupsReply
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable group chats: %w", err)
	}
	return out.Groups, nil
}

// GroupsUpdate is .room/groups.md for this turn, and whether to write it: only
// in a room that has a connected group, and every turn after the session's
// workspace got one (listed), so it never goes stale; a read that failed is not
// "no groups" (GroupsFile says so) and writes nothing where there was no file.
// next is the session's new listed state.
func GroupsUpdate(listed bool, groups []wire.RoomGroup, err error) (f NamedFile, write, next bool) {
	if len(groups) == 0 && !listed {
		return NamedFile{}, false, false
	}
	return GroupsFile(groups, err), true, true
}

// GroupsFile renders .room/groups.md.
func GroupsFile(groups []wire.RoomGroup, err error) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("Connected group chats"))
	b.WriteString("\n")
	b.WriteString("Group chats this room's members connected (an organization bot is in each). Send to one from an app with\n")
	b.WriteString("app.notify({to: \"group:<name>\", title, text}), or name it in a scheduled task. The name is the one the\n")
	b.WriteString("members gave it; nothing here is the chat itself.\n\n")
	switch {
	case err != nil && len(groups) == 0:
		b.WriteString("(the group chats could not be read this turn; this is not a sign that there are none)\n")
	case err != nil:
		b.WriteString("(the list could not be refreshed this turn; it may be out of date)\n\n")
	case len(groups) == 0:
		b.WriteString("(no group chats now)\n")
	}
	for _, g := range groups {
		fmt.Fprintf(&b, "- group:%s (delivered by %s)\n", g.Name, g.Notifier)
	}
	return NamedFile{Name: ".room/groups.md", Content: b.String()}
}
