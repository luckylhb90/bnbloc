package roomlog

import (
	"strings"
	"time"
)

// The live interaction trail as an agent reads it (.room/live.md): what
// members did inside running deliverables, with each deliverable's own live
// state at that moment. The bridge observes the gestures (artifact_state
// events); this file owns how they read. A live state is data a
// deliverable's code produced, so the trail is fenced like any text someone
// else wrote.

// LiveGesture is one observed interaction with a running deliverable.
type LiveGesture struct {
	AtMs     int64
	Actor    string
	Artifact string
	// State is the deliverable's compact live state (JSON), bounded by the
	// bridge when it records the gesture.
	State string
}

// liveAuthor names who wrote the trail inside its fence.
const liveAuthor = "the room's running deliverables and the members using them"

// LiveFile renders .room/live.md, newest gesture last.
func LiveFile(trail []LiveGesture) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("Live interaction trail"))
	b.WriteString("\n")
	if len(trail) == 0 {
		b.WriteString("(no live interactions yet)\n")
		return NamedFile{Name: ".room/live.md", Content: b.String()}
	}
	b.WriteString("What members did in running deliverables (ids as in artifacts.md), newest last; state is the deliverable's own live state at that moment.\n\n")
	var lines strings.Builder
	for _, g := range trail {
		actor := strings.TrimSpace(g.Actor)
		if actor == "" {
			actor = "?"
		}
		lines.WriteString("- " + time.UnixMilli(g.AtMs).UTC().Format("15:04:05") + " UTC · artifact " + g.Artifact + " · by " + actor + " · state " + g.State + "\n")
	}
	b.WriteString(Fence(liveAuthor, lines.String()))
	b.WriteString("\n")
	return NamedFile{Name: ".room/live.md", Content: b.String()}
}
