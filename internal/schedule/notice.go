package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A notice schedule (wire.ScheduleRecipientNotice) says everything about what a
// run sends, so a run needs no model: where it goes, its title and its body.
// The title and the body may hold names in braces that stand for values of the
// run itself ({week} is the run's ISO week), computed from the slot in the
// schedule's time zone. NoticeVars is the one list of them.

// maxNoticeURL bounds a notice's link.
const maxNoticeURL = 2048

// NoticeVar is one name a notice's title or body may use.
type NoticeVar struct {
	Name string
	// Doc says what it stands for, with an example.
	Doc string
	// Value is its value for a run at slot, in loc, in room.
	Value func(slot time.Time, loc *time.Location, room string) string
}

// NoticeVars are the names a notice may use. A name is date-and-time only when
// it needs no language, so it reads the same in any message ("{weekday}" is not
// here: its words belong to the message's language).
var NoticeVars = []NoticeVar{
	{"date", "the run's date, 2026-10-22", func(t time.Time, l *time.Location, _ string) string { return t.In(l).Format("2006-01-02") }},
	{"time", "the run's time, 09:00", func(t time.Time, l *time.Location, _ string) string { return t.In(l).Format("15:04") }},
	{"year", "the year, 2026", func(t time.Time, l *time.Location, _ string) string { return t.In(l).Format("2006") }},
	{"month", "the year and month, 2026-10", func(t time.Time, l *time.Location, _ string) string { return t.In(l).Format("2006-01") }},
	{"day", "the day of the month, 22", func(t time.Time, l *time.Location, _ string) string { return fmt.Sprint(t.In(l).Day()) }},
	{"week", "the ISO week, 2026-W43", func(t time.Time, l *time.Location, _ string) string {
		y, w := t.In(l).ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	}},
	{"room", "the room's name", func(_ time.Time, _ *time.Location, room string) string { return room }},
}

func noticeVar(name string) (NoticeVar, bool) {
	for _, v := range NoticeVars {
		if v.Name == name {
			return v, true
		}
	}
	return NoticeVar{}, false
}

// NoticeVarNames lists the names, for a message that says which are known.
func NoticeVarNames() string {
	names := make([]string, 0, len(NoticeVars))
	for _, v := range NoticeVars {
		names = append(names, "{"+v.Name+"}")
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// walkNotice reads s as text with names in braces ("{{" and "}}" are literal
// braces), calling name for each; it returns the text with each name replaced
// by what name returns.
func walkNotice(s string, name func(string) (string, error)) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			out.WriteByte('{')
			i += 2
		case strings.HasPrefix(s[i:], "}}"):
			out.WriteByte('}')
			i += 2
		case s[i] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", errors.New(`an opening "{" is never closed (write "{{" for a brace)`)
			}
			v, err := name(s[i+1 : i+end])
			if err != nil {
				return "", err
			}
			out.WriteString(v)
			i += end + 1
		case s[i] == '}':
			return "", errors.New(`a closing "}" opens nothing (write "}}" for a brace)`)
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String(), nil
}

// CheckNoticeText refuses a title or body that names something NoticeVars does
// not know, or leaves a brace open: a typo must fail when the schedule is made,
// not send "{wek}" to a group.
func CheckNoticeText(s string) error {
	_, err := walkNotice(s, func(n string) (string, error) {
		if _, ok := noticeVar(n); !ok {
			return "", fmt.Errorf("{%s} is not a name a notice knows (%s)", n, NoticeVarNames())
		}
		return "", nil
	})
	return err
}

// ExpandNotice is s with each name replaced by its value for a run at slot, in
// the schedule's zone, in room. s passed CheckNoticeText when it was stored; a
// name that no longer exists is left as it was written.
func ExpandNotice(s string, slot time.Time, loc *time.Location, room string) string {
	if loc == nil {
		loc = time.UTC
	}
	out, err := walkNotice(s, func(n string) (string, error) {
		if v, ok := noticeVar(n); ok {
			return v.Value(slot, loc, room), nil
		}
		return "{" + n + "}", nil
	})
	if err != nil {
		return s
	}
	return out
}

// normalizeNotice validates and canonicalizes a notice recipient: its target in
// the one form an app's notify takes, its title and its link.
func normalizeNotice(r wire.ScheduleRecipient) (wire.ScheduleRecipient, error) {
	to, err := normalizeNoticeTo(r.To)
	if err != nil {
		return r, err
	}
	title := strings.TrimSpace(r.Title)
	switch {
	case title == "":
		return r, errors.New("recipient: a notice needs a title")
	case !utf8.ValidString(title) || utf8.RuneCountInString(title) > notify.MaxAlertTitleRunes:
		return r, fmt.Errorf("recipient: the title is at most %d characters", notify.MaxAlertTitleRunes)
	}
	if err := CheckNoticeText(title); err != nil {
		return r, fmt.Errorf("recipient: the title: %w", err)
	}
	link := strings.TrimSpace(r.URL)
	if len(link) > maxNoticeURL {
		return r, errors.New("recipient: the link is too long")
	}
	return wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: to, Title: title, URL: link, Compose: r.Compose}, nil
}

// normalizeNoticeTo is a notice's target as an app's notify names it: "room", "group:<name>" or
// a list of logins, in canonical JSON.
func normalizeNoticeTo(raw json.RawMessage) (json.RawMessage, error) {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		one = strings.TrimSpace(one)
		switch {
		case one == wire.AppNotifyRoom:
			return json.Marshal(one)
		case strings.HasPrefix(one, wire.AppNotifyGroupPrefix):
			name := strings.TrimSpace(strings.TrimPrefix(one, wire.AppNotifyGroupPrefix))
			if n := utf8.RuneCountInString(name); n == 0 || n > wire.MaxNotifyGroupLen {
				return nil, fmt.Errorf("recipient: a group name is 1 to %d characters", wire.MaxNotifyGroupLen)
			}
			return json.Marshal(wire.AppNotifyGroupPrefix + name)
		}
		return nil, errors.New(`recipient: a notice goes to "room", "group:<name>" or a list of logins`)
	}
	var logins []string
	if json.Unmarshal(raw, &logins) != nil || len(logins) == 0 {
		return nil, errors.New(`recipient: a notice goes to "room", "group:<name>" or a list of logins`)
	}
	if len(logins) > wire.MaxAppNotifyLogins {
		return nil, fmt.Errorf("recipient: a notice names at most %d logins", wire.MaxAppNotifyLogins)
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(logins))
	for _, l := range logins {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			clean = append(clean, l)
		}
	}
	if len(clean) == 0 {
		return nil, errors.New("recipient: a list of logins names someone")
	}
	return json.Marshal(clean)
}

// RunsAtGateway reports whether a recipient kind's slot is carried out by the
// gateway (the AI model and the notification emitter live there) rather than
// posted as chat by realtime.
func RunsAtGateway(kind string) bool {
	return kind == wire.ScheduleRecipientAI || kind == wire.ScheduleRecipientNotice
}
