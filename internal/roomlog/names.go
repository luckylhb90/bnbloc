package roomlog

import (
	"strings"
	"unicode/utf8"
)

// People's display names (docs/prd/2026-10-14-multi-member-rooms.md rule
// 8): records name a person by display name, a file's first mention of them
// with their id ("Maya Lee (maya)"), so a name never stands in for an
// identity: a name is taken from the row's author id only, never from
// message text, and one that could pass for record syntax or another id is
// not used.

// maxNameRunes bounds a display name in records.
const maxNameRunes = 64

// usableName is name as records may show it for id, or "" when it adds
// nothing (the id itself) or could mislead (line breaks, record syntax:
// an agent's "owner/provider", "→", "[", "(", ":").
func usableName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == id || utf8.RuneCountInString(name) > maxNameRunes || strings.ContainsAny(name, "/:→[]()\r\n\t") {
		return ""
	}
	return name
}

// PersonLabel is how a person is named where their identity matters (the
// [now] marker's requester): "Name (id)", or the id alone.
func PersonLabel(name, id string) string {
	if n := usableName(name, id); n != "" {
		return n + " (" + id + ")"
	}
	return id
}

// SetNames gives each person's line its speaker's display name (names: id →
// display name), by the row's author id.
func SetNames(entries []Entry, names map[string]string) []Entry {
	if len(names) == 0 {
		return entries
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		if e.Kind == KindHuman {
			e.Name = usableName(names[e.Speaker], e.Speaker)
		}
		out[i] = e
	}
	return out
}

// firstMentions marks, within one rendered file, every line of a person
// after their first as named by name alone.
func firstMentions(entries []Entry) []Entry {
	seen := map[string]bool{}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		if e.Name != "" {
			e.short = seen[e.Speaker]
			seen[e.Speaker] = true
		}
		out[i] = e
	}
	return out
}

// speakerLabel is the speaker as a line names them (Sender).
func speakerLabel(e Entry, sp string) string {
	switch {
	case e.Name == "":
		return sp
	case e.short:
		return e.Name
	default:
		return e.Name + " (" + sp + ")"
	}
}
