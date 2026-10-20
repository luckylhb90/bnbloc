package assist

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// A room's conversation (wire.AssistContextConversation,
// docs/prd/2026-10-20-complete-room-capture.md §2.1): what the room-saving
// assist reads to say what a room's way of working relies on and to draft
// its instructions. The gateway reads the room's channels the member reads
// (never the people chat) through its one history read and hands the lines
// here newest first; NewConversation keeps the newest within the budget,
// and the block shows them in the order they were said and says how many
// older ones it left out.

// Bounds of a conversation: the lines kept, each line's characters and
// all lines' characters together (about 6,000 tokens at most, beside a
// task's instructions and its reply's 2,000).
const (
	MaxConversationLines = 80
	MaxConversationRunes = 20000
	maxConversationLine  = 1500
)

// Conversation is a room's conversation as an assist reads it: the room's
// name, the kept lines oldest first, and how many older lines the budget
// left out.
type Conversation struct {
	Room  string
	Lines []Line
	Left  int
}

// NewConversation keeps the newest lines of newestFirst (each cut to
// maxConversationLine characters) while they fit MaxConversationLines and
// MaxConversationRunes, in the order they were said; Left counts the rest.
func NewConversation(room string, newestFirst []Line) *Conversation {
	c := &Conversation{Room: room}
	budget := MaxConversationRunes
	for i, l := range newestFirst {
		l.Text = cut(l.Text, maxConversationLine)
		n := utf8.RuneCountInString(l.Text) + utf8.RuneCountInString(l.Speaker) + utf8.RuneCountInString(l.Channel)
		if len(c.Lines) == MaxConversationLines || n > budget {
			c.Left = len(newestFirst) - i
			break
		}
		budget -= n
		c.Lines = append(c.Lines, l)
	}
	slices.Reverse(c.Lines)
	return c
}

// conversation writes the conversation block: the room, what the budget
// left out, then "[speaker in #channel] text" a line, every text fenced.
func conversation(b *strings.Builder, c Conversation) {
	b.WriteString("\n<conversation>\n")
	fmt.Fprintf(b, "room: %s\n", fence(cut(c.Room, maxPartRunes)))
	if c.Left > 0 {
		fmt.Fprintf(b, "(the latest %d messages, oldest first; %d older ones left out)\n", len(c.Lines), c.Left)
	} else {
		fmt.Fprintf(b, "(%d messages, oldest first)\n", len(c.Lines))
	}
	for _, l := range c.Lines {
		who := fence(cut(l.Speaker, 80))
		if l.Channel != "" {
			who += " in #" + fence(cut(l.Channel, 80))
		}
		fmt.Fprintf(b, "[%s] %s\n", who, fence(l.Text))
	}
	b.WriteString("</conversation>\n")
}
