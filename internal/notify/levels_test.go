package notify

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// recipientsByKey maps each event's dedup key to its recipients.
func recipientsByKey(events []Event) map[string][]string {
	out := map[string][]string{}
	for _, ev := range events {
		out[ev.DedupKey] = ev.Recipients
	}
	return out
}

// Levels, @here and @channel (channels PRD C4, C5): a direct mention
// notifies whatever the level; @channel reaches every listener not muted;
// @here only those here; level "all" hears every message; each person is told
// once and the author never.
func TestMessageEventsByLevel(t *testing.T) {
	o := NewObserver(nil, fakeMembership{"r1/bob": true, "r1/carol": true, "r1/dave": true, "r1/erin": true, "r1/alice": true}, 8)
	var reaches []string
	o.Listeners = func(_ context.Context, roomID, channelID, reach string) ([]Listener, error) {
		reaches = append(reaches, reach)
		return []Listener{
			{UserID: "alice", Level: wire.NotifyLevelAll, Here: true},
			{UserID: "bob", Level: wire.NotifyLevelMuted, Here: true},
			{UserID: "carol", Level: wire.NotifyLevelAll},
			{UserID: "dave", Level: wire.NotifyLevelMentions, Here: true},
			{UserID: "erin", Level: wire.NotifyLevelMentions},
		}, nil
	}
	msg := func(text string) map[string][]string {
		return recipientsByKey(o.messageEvents(observation{tenantID: "t", kind: "chat_message", roomID: "r1", channelID: "ch1", seq: 4, author: "alice", text: text}))
	}
	key := "msg:r1:4"
	cases := []struct {
		text  string
		reach string
		want  map[string][]string
	}{
		// A plain message: the listeners at "all" (carol), not the author.
		{"hello", ReachFollowers, map[string][]string{key + ":all": {"carol"}}},
		// A muted member is still told of a direct mention.
		{"@bob look", ReachFollowers, map[string][]string{key: {"bob"}, key + ":all": {"carol"}}},
		// @channel: everyone not muted, once; the all-level listener by it.
		{"@channel ship it", ReachChannel, map[string][]string{key + ":channel": {"carol", "dave", "erin"}}},
		// @here: who is here (dave); carol, not here, by her level.
		{"@here anyone?", ReachHere, map[string][]string{key + ":here": {"dave"}, key + ":all": {"carol"}}},
		// A direct mention wins over @channel for that person.
		{"@erin @channel", ReachChannel, map[string][]string{key: {"erin"}, key + ":channel": {"carol", "dave"}}},
	}
	for _, c := range cases {
		reaches = nil
		if got := msg(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: recipients = %v, want %v", c.text, got, c.want)
		}
		if len(reaches) != 1 || reaches[0] != c.reach {
			t.Errorf("%q: asked listeners for %v, want %s", c.text, reaches, c.reach)
		}
	}
	// A broadcast mention is marked so quiet hours hold it back.
	for _, ev := range o.messageEvents(observation{tenantID: "t", roomID: "r1", channelID: "ch1", seq: 5, author: "alice", text: "@here"}) {
		if ev.Type == wire.NotificationMention && !ev.Broadcast {
			t.Errorf("@here event not marked broadcast: %+v", ev)
		}
	}
	// Without Listeners only direct mentions notify, as before.
	o.Listeners = nil
	if got := msg("@channel @carol hi"); !reflect.DeepEqual(got, map[string][]string{key: {"carol"}}) {
		t.Errorf("no listeners: %v", got)
	}
}

// The effective level (C4): the member's own; else all for a thread they
// follow or a channel they posted in; mentions otherwise.
func TestEffectiveNotifyLevel(t *testing.T) {
	for _, c := range []struct {
		set                       string
		thread, following, posted bool
		want                      string
	}{
		{"", false, false, false, wire.NotifyLevelMentions},
		{"", false, true, false, wire.NotifyLevelMentions},
		{"", false, true, true, wire.NotifyLevelAll},
		{"", true, true, false, wire.NotifyLevelAll},
		{"", true, false, false, wire.NotifyLevelMentions},
		{wire.NotifyLevelMuted, false, true, true, wire.NotifyLevelMuted},
		{wire.NotifyLevelAll, false, false, false, wire.NotifyLevelAll},
	} {
		if got := wire.EffectiveNotifyLevel(c.set, c.thread, c.following, c.posted); got != c.want {
			t.Errorf("%+v: level = %s, want %s", c, got, c.want)
		}
	}
}

// Quiet hours hold back every notification inside the window, a direct
// mention only when the member does not allow mentions, and never one
// outside it; channel messages follow their own preference.
func TestQuietHoursAndChannelMessages(t *testing.T) {
	q := &wire.QuietHours{Start: "22:00", End: "07:00", TZ: "Asia/Shanghai", AllowMentions: true}
	p := store.DefaultNotificationPrefs()
	p.QuietHours = q
	loc, _ := time.LoadLocation("Asia/Shanghai")
	night := time.Date(2026, 10, 30, 23, 30, 0, 0, loc)
	morning := time.Date(2026, 10, 30, 6, 59, 0, 0, loc)
	day := time.Date(2026, 10, 30, 7, 0, 0, 0, loc)
	mention := Event{Type: wire.NotificationMention}
	broadcast := Event{Type: wire.NotificationMention, Broadcast: true}
	finished := Event{Type: wire.NotificationAgentFinished}
	if Quiet(p, mention, night) || !Quiet(p, broadcast, night) || !Quiet(p, finished, morning) || Quiet(p, finished, day) {
		t.Error("quiet hours: mention through, broadcast and others held, nothing at 07:00")
	}
	q.AllowMentions = false
	if !Quiet(p, mention, night) {
		t.Error("a mention came through quiet hours that do not allow mentions")
	}
	if !Allows(p, wire.NotificationChannelMessage, "r1") {
		t.Error("channel messages are on by default")
	}
	p.ChannelMessages = false
	if Allows(p, wire.NotificationChannelMessage, "r1") {
		t.Error("channel messages turned off still allowed")
	}
	for _, bad := range []wire.QuietHours{{Start: "7:00", End: "08:00", TZ: "UTC"}, {Start: "08:00", End: "08:00", TZ: "UTC"}, {Start: "22:00", End: "07:00", TZ: "Mars/Base"}, {Start: "22:00", End: "07:00"}} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if (wire.QuietHours{Start: "09:00", End: "17:00", TZ: "UTC"}).Contains(time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC)) {
		t.Error("the window's end is outside it")
	}
}
