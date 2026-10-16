// Package notes is the one owner of what a room note may be and of the order
// an agent reads them in (docs/prd/2026-10-16-room-memory-and-evolution.md):
// the type and status rules, the size limits, and Rank, the ordering behind
// the agent's index. The stores keep the rows, the room service checks every
// write through Check, and the bridge renders what Rank returns; nothing else
// decides which notes come first or whether a note is valid.
package notes

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ValidType reports a known note type.
func ValidType(t string) bool { return slices.Contains(wire.NoteTypes, t) }

// ValidStatus reports a known note status.
func ValidStatus(s string) bool {
	return s == wire.NoteActive || s == wire.NoteSuperseded || s == wire.NoteArchived
}

// BodyLimit is the most bytes a note of typ's body may hold: a brief may be
// longer than any other note.
func BodyLimit(typ string) int {
	if typ == wire.NoteBrief {
		return wire.MaxBriefBodyBytes
	}
	return wire.MaxNoteBodyBytes
}

// Check says why a note's content is refused, "" when it is fine: a known
// type, a title of 1 to MaxNoteTitleRunes characters on one line, a body in
// the type's limit and valid UTF-8, at most MaxNoteSources well-formed
// sources. The error text is for the person who wrote it.
func Check(typ, title, body string, sources []wire.NoteSource) error {
	if !ValidType(typ) {
		return fmt.Errorf("type is one of %s", strings.Join(wire.NoteTypes, ", "))
	}
	if err := CheckTitle(title); err != nil {
		return err
	}
	if err := CheckBody(typ, body); err != nil {
		return err
	}
	if len(sources) > wire.MaxNoteSources {
		return fmt.Errorf("a note names at most %d sources", wire.MaxNoteSources)
	}
	for _, s := range sources {
		if (s.Kind != wire.NoteSourceMessage && s.Kind != wire.NoteSourceArtifact) || strings.TrimSpace(s.Ref) == "" || len(s.Ref) > 64 || s.Version < 0 {
			return errors.New("a source is a message (its sequence number) or a deliverable (its id and version)")
		}
	}
	return nil
}

// CheckTitle refuses an empty, over-long or multi-line title.
func CheckTitle(title string) error {
	t := strings.TrimSpace(title)
	if t == "" || !utf8.ValidString(t) {
		return errors.New("a note needs a title")
	}
	if utf8.RuneCountInString(t) > wire.MaxNoteTitleRunes {
		return fmt.Errorf("a title is at most %d characters", wire.MaxNoteTitleRunes)
	}
	if strings.IndexFunc(t, func(r rune) bool { return r == '\n' || r == '\r' || unicode.IsControl(r) }) >= 0 {
		return errors.New("a title is one line")
	}
	return nil
}

// CheckBody refuses a body that is not text or is over its type's limit. A
// note may have no body (its title says it).
func CheckBody(typ, body string) error {
	if !utf8.ValidString(body) {
		return errors.New("a note's body is text")
	}
	if limit := BodyLimit(typ); len(body) > limit {
		return fmt.Errorf("a %s's body is at most %d bytes", typ, limit)
	}
	return nil
}

// durable is the types that outrank dated memory in the agent's index: a
// decision or a lesson is read before the chatter of last week.
func durable(typ string) bool { return typ == wire.NoteKnowledge || typ == wire.NoteLesson }

// freshness is when a note was last known to be true: the later of its last
// edit and its last confirmation.
func freshness(n wire.RoomNote) int64 { return max(n.UpdatedAtMs, n.VerifiedAtMs) }

// Rank is the active notes of list that are not the brief, in the order an
// agent's index lists them: pinned first, then durable types (knowledge,
// lessons) before dated memory, each by how fresh it is (newest first), and
// the note id to break a tie, so the order is stable. Notes that are
// superseded or archived never appear. The brief is not here: the agent
// reads it first, apart.
func Rank(list []wire.RoomNote) []wire.RoomNote {
	out := make([]wire.RoomNote, 0, len(list))
	for _, n := range list {
		if n.Status == wire.NoteActive && n.Type != wire.NoteBrief {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if da, db := durable(a.Type), durable(b.Type); da != db {
			return da
		}
		if fa, fb := freshness(a), freshness(b); fa != fb {
			return fa > fb
		}
		return a.NoteID < b.NoteID
	})
	return out
}

// Brief is the room's active brief in list, nil when it has none.
func Brief(list []wire.RoomNote) *wire.RoomNote {
	for i := range list {
		if list[i].Type == wire.NoteBrief && list[i].Status == wire.NoteActive {
			n := list[i]
			return &n
		}
	}
	return nil
}
