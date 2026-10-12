package contentshare

import (
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Share is one share of a component version, announced to the members it
// gave a copy (or moved theirs).
type Share struct {
	Source   store.ContentComponent
	Version  string
	SharedBy string
	// Note is the author's message; empty uses the default text.
	Note string
	// Link opens the recipient's Library ("" when no public URL is known).
	Link string
	// ViaRoom marks copies given because the member joined a room the
	// component is shared with.
	ViaRoom bool
}

// Announce is the ONE content_shared notification of a share: one event
// per member, each with ShareDedupKey, so a member hears about a version
// once however it reached them (a direct share, a room share, a later join)
// and a retried share notifies nobody twice. The gateway's share route and
// realtime's member-join step both announce through it.
func Announce(s Share, members []string) []notify.Event {
	text := s.Note
	if text == "" {
		text = "It is in your Library, pinned to version " + s.Version + "."
		if s.ViaRoom {
			text += " You got it by joining a room it is shared with."
		}
	}
	out := make([]notify.Event, 0, len(members))
	for _, m := range members {
		out = append(out, notify.Event{
			Type: wire.NotificationContentShared, Recipients: []string{m}, Actor: s.SharedBy,
			Title: s.SharedBy + " shared the " + Label(s.Source) + " with you", Text: text, URL: s.Link,
			DedupKey: ShareDedupKey(s.Source.ID, s.Version, m),
		})
	}
	return out
}

// ShareDedupKey is the notification dedup key of one member's copy of one
// component version.
func ShareDedupKey(componentID, version, member string) string {
	return "share:" + componentID + "@" + version + ":" + member
}
