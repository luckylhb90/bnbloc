package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A schedule with an event recurrence (wire.RecurEvent) runs when an event
// it subscribes to happens instead of on the clock
// (docs/prd/2026-10-21-room-events.md): its trigger is the subscription, a
// source (the platform, its own room, its own webhook URL), one or more
// type patterns and an optional filter on the event's subject and
// top-level data fields. This file owns what a trigger may say
// (validateEvent), which events a schedule takes (Triggers, with the
// no-self-trigger rule) and what a run's text carries of its event
// (RunText fenced, EventLine plain). A burst of events is coalesced per
// schedule by MinInterval where the run is set
// (store.RoomScheduleStore.TriggerEventSchedule).

// Bounds of a trigger.
const (
	maxEventTypes       = 8
	maxTypePatternLen   = 128
	maxFilterTextLen    = 256
	maxFilterDataFields = 8
	maxFilterKeyLen     = 64
)

// validateEvent normalizes an event trigger: a known source; one to
// maxEventTypes type patterns (a type, or a prefix ending in "*"), unique,
// in the order given, each taking at least one known type when the source
// has a closed list (the platform's, the room's); an optional filter,
// dropped when empty.
func validateEvent(rec wire.ScheduleRecurrence) (wire.ScheduleRecurrence, error) {
	out := wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: strings.TrimSpace(rec.Source)}
	if !slices.Contains(wire.EventSubscriptionSources, out.Source) {
		return rec, fmt.Errorf("recurrence: an event trigger's source is one of %s", strings.Join(wire.EventSubscriptionSources, ", "))
	}
	known := knownTypes(out.Source)
	for _, p := range rec.Types {
		p = strings.TrimSpace(p)
		if slices.Contains(out.Types, p) {
			continue
		}
		if !validTypePattern(p) {
			return rec, fmt.Errorf("recurrence: %q is not an event type (a type, or a prefix ending in *; no spaces)", p)
		}
		if known != nil && !slices.ContainsFunc(known, func(t string) bool { return typeMatches(p, t) }) {
			return rec, fmt.Errorf("recurrence: %s has no event %q (%s)", out.Source, p, strings.Join(known, ", "))
		}
		if out.Source == wire.SubscribeMember && wire.ReservedEventType(strings.TrimSuffix(p, "*")) {
			return rec, fmt.Errorf("recurrence: a member never sends %q: types starting %s are the platform's", p, strings.Join(wire.ReservedEventTypePrefixes, ", "))
		}
		out.Types = append(out.Types, p)
	}
	if len(out.Types) == 0 || len(out.Types) > maxEventTypes {
		return rec, fmt.Errorf("recurrence: an event trigger takes 1 to %d event types", maxEventTypes)
	}
	f, err := validateFilter(rec.Filter)
	if err != nil {
		return rec, err
	}
	out.Filter = f
	return out, nil
}

// knownTypes is the closed list of a source's event types (nil: open, a
// webhook's types are the outside system's).
func knownTypes(source string) []string {
	switch source {
	case wire.SubscribePlatform:
		return wire.PlatformEventTypes
	case wire.SubscribeRoom:
		return wire.RoomEventTypes
	}
	return nil
}

func validTypePattern(p string) bool {
	if p == "" || len(p) > maxTypePatternLen || strings.IndexFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	star := strings.IndexByte(p, '*')
	return star < 0 || star == len(p)-1
}

// typeMatches reports whether a type pattern takes typ: the same type, or
// a prefix ending in "*" ("com.github.*"; "*" takes every type).
func typeMatches(pattern, typ string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(typ, prefix)
	}
	return pattern == typ
}

// validateFilter normalizes a filter: trimmed, bounded, nil when empty.
func validateFilter(f *wire.EventFilter) (*wire.EventFilter, error) {
	if f == nil {
		return nil, nil
	}
	out := wire.EventFilter{Subject: strings.TrimSpace(f.Subject), SubjectPrefix: strings.TrimSpace(f.SubjectPrefix)}
	if len(out.Subject) > maxFilterTextLen || len(out.SubjectPrefix) > maxFilterTextLen {
		return nil, fmt.Errorf("recurrence: a filter's subject is at most %d characters", maxFilterTextLen)
	}
	if len(f.Data) > maxFilterDataFields {
		return nil, fmt.Errorf("recurrence: a filter checks at most %d data fields", maxFilterDataFields)
	}
	for k, v := range f.Data {
		k = strings.TrimSpace(k)
		if k == "" || len(k) > maxFilterKeyLen || len(v) > maxFilterTextLen {
			return nil, fmt.Errorf("recurrence: a filter's data field is a name (at most %d characters) and a value (at most %d)", maxFilterKeyLen, maxFilterTextLen)
		}
		if out.Data == nil {
			out.Data = map[string]string{}
		}
		out.Data[k] = v
	}
	if out.Subject == "" && out.SubjectPrefix == "" && len(out.Data) == 0 {
		return nil, nil
	}
	return &out, nil
}

// TriggerText is an event trigger in one line, for a person and an agent:
// "on bnbloc/room room.file.added where subject^=reports/ data.kind=site"
// (= equals, ^= starts with). The CLI and the room tools say a trigger
// this way.
func TriggerText(rec wire.ScheduleRecurrence) string {
	out := "on " + rec.Source + " " + strings.Join(rec.Types, ", ")
	var where []string
	if f := rec.Filter; f != nil {
		if f.Subject != "" {
			where = append(where, "subject="+f.Subject)
		}
		if f.SubjectPrefix != "" {
			where = append(where, "subject^="+f.SubjectPrefix)
		}
		keys := slices.Sorted(maps.Keys(f.Data))
		for _, k := range keys {
			where = append(where, "data."+k+"="+f.Data[k])
		}
	}
	if len(where) > 0 {
		out += " where " + strings.Join(where, " ")
	}
	return out
}

// OnEvent reports a recurrence that runs on events, never on the clock (it
// has no next run until an event it takes arrives).
func OnEvent(rec wire.ScheduleRecurrence) bool { return rec.Kind == wire.RecurEvent }

// Triggers reports whether ev, caused by a run of a task of causeRoom ("":
// by none), runs sc. sc's trigger takes it (Takes), and the no-self-trigger
// rule holds: an event a room's own scheduled run caused (the run's job id
// travels on what it does: a file its agent published, an app document it
// wrote) never runs that room's tasks, so a task cannot loop on what it
// does. Who an event reaches (its tenants, its room, a webhook's own task)
// is the dispatcher's query; this is the rest of the one rule.
func Triggers(sc wire.RoomSchedule, ev wire.CloudEvent, causeRoom string) bool {
	return (causeRoom == "" || causeRoom != sc.RoomID) && Takes(sc, ev)
}

// Takes reports whether sc's event trigger takes ev: its source (the
// platform; its own room; its own webhook URL), one of its type patterns,
// and its filter.
func Takes(sc wire.RoomSchedule, ev wire.CloudEvent) bool {
	rec := sc.Recurrence
	if !OnEvent(rec) || !sourceMatches(sc, ev.Source) || !slices.ContainsFunc(rec.Types, func(p string) bool { return typeMatches(p, ev.Type) }) {
		return false
	}
	return filterMatches(rec.Filter, ev)
}

func sourceMatches(sc wire.RoomSchedule, source string) bool {
	switch sc.Recurrence.Source {
	case wire.SubscribePlatform:
		return source == wire.SourcePlatform
	case wire.SubscribeRoom:
		return sc.RoomID != "" && source == wire.SourceRoomPrefix+sc.RoomID
	case wire.SubscribeMember:
		// Any member of the room (the dispatcher reaches only the room the
		// member sent it to).
		return strings.HasPrefix(source, wire.SourceMemberPrefix)
	case wire.SubscribeWebhook:
		return sc.Hook != nil && source == wire.SourceWebhookPrefix+sc.Hook.HookID
	}
	return false
}

func filterMatches(f *wire.EventFilter, ev wire.CloudEvent) bool {
	if f == nil {
		return true
	}
	if (f.Subject != "" && ev.Subject != f.Subject) || (f.SubjectPrefix != "" && !strings.HasPrefix(ev.Subject, f.SubjectPrefix)) {
		return false
	}
	if len(f.Data) == 0 {
		return true
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(ev.Data, &top) != nil {
		return false
	}
	for k, want := range f.Data {
		got, ok := scalarText(top[k])
		if !ok || got != want {
			return false
		}
	}
	return true
}

// scalarText is a JSON scalar as its text: a string unquoted, a number or a
// boolean as written; false for null, an object, an array or nothing.
func scalarText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case '{', '[', 'n':
		return "", false
	}
	return string(raw), true
}

// EventLine is ev on one plain line, as people read it in a notice:
// "component.published browser kind=mcp name=browser version=1.2.0": the
// type, the subject, then the data's top-level scalar fields (a list of
// scalars joined by commas) in the order the data has them; nested values
// are left out.
func EventLine(ev wire.CloudEvent) string {
	parts := []string{ev.Type}
	if s := oneWord(ev.Subject); s != "" {
		parts = append(parts, s)
	}
	for _, f := range topFields(ev.Data) {
		if v := oneWord(f.value); v != "" {
			parts = append(parts, f.key+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

func oneWord(s string) string { return strings.Join(strings.Fields(s), "_") }

type field struct{ key, value string }

// topFields are the top-level scalar (or list of scalars) fields of a JSON
// object, in order.
func topFields(data json.RawMessage) []field {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []field
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return out
		}
		if v, ok := scalarText(raw); ok {
			out = append(out, field{key, v})
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
			var vals []string
			for _, item := range list {
				if v, ok := scalarText(item); ok {
					vals = append(vals, v)
				}
			}
			if len(vals) == len(list) {
				out = append(out, field{key, strings.Join(vals, ",")})
			}
		}
	}
	return out
}

// maxRunEventBytes bounds what a run's text carries of its event, so the
// task's own words always keep room in one chat message.
const maxRunEventBytes = 8 << 10

// eventAuthor names who wrote an event's data in the fence a run reads it
// in.
func eventAuthor(source string) string {
	switch {
	case source == wire.SourcePlatform:
		return "bnbloc (a platform event)"
	case strings.HasPrefix(source, wire.SourceRoomPrefix):
		return "bnbloc (an event of this room)"
	case strings.HasPrefix(source, wire.SourceWebhookPrefix):
		return "an outside system (this task's webhook)"
	case strings.HasPrefix(source, wire.SourceMemberPrefix):
		return strings.TrimPrefix(source, wire.SourceMemberPrefix) + " (an event they sent this room)"
	}
	return "an event's source"
}

// MemberEventOf is the event a room's member (subject) sends with in, at
// now: its source theirs (wire.SourceMemberPrefix + subject), a fresh
// id and the time; its type never one of the platform's
// (wire.ReservedEventTypePrefixes); bounded like every event
// (wire.ValidEvent).
func MemberEventOf(subject string, in wire.MemberEvent, now time.Time) (wire.CloudEvent, error) {
	typ := strings.TrimSpace(in.Type)
	if wire.ReservedEventType(typ) {
		return wire.CloudEvent{}, fmt.Errorf("event: type %q is the platform's (types starting %s are only the platform's)", typ, strings.Join(wire.ReservedEventTypePrefixes, ", "))
	}
	return wire.ValidEvent(wire.CloudEvent{ID: ids.New("evt_"), Source: wire.SourceMemberPrefix + subject, Type: typ,
		Time: wire.EventTime(now), Subject: strings.TrimSpace(in.Subject), Data: in.Data})
}

// eventBlock is ev as a run reads it: its attributes and its data, the data
// cut (and the cut said) so the block fits maxRunEventBytes.
func eventBlock(ev wire.CloudEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: %s\nsource: %s\n", ev.Type, ev.Source)
	if ev.Subject != "" {
		fmt.Fprintf(&b, "subject: %s\n", ev.Subject)
	}
	fmt.Fprintf(&b, "time: %s\nid: %s\n", ev.Time, ev.ID)
	if len(ev.Data) > 0 {
		room := maxRunEventBytes - b.Len() - len("data: ") - 80
		data := string(ev.Data)
		if len(data) > room {
			data = roomlog.Truncate(data, room) + fmt.Sprintf(" (the data is %d bytes; cut here)", len(ev.Data))
		}
		b.WriteString("data: " + data + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RunText is what a run of sc gives the model or an agent: the schedule's
// text and, when the run answers an event (an event schedule whose run an
// event set), the event fenced as data, never instructions. Bounded to
// what one chat message may hold: the schedule's own text is cut, never
// the event's attributes. A notice sent to people as written reads the
// event unfenced (EventLine).
func RunText(sc wire.RoomSchedule) string {
	ev := RunEvent(sc)
	if ev == nil {
		return sc.Text
	}
	facts := "\n\n" + roomlog.Fence(eventAuthor(ev.Source), eventBlock(*ev))
	return roomlog.Truncate(sc.Text, wire.MaxChatTextBytes-len(facts)) + facts
}

// RunEvent is the event a run of sc answers: the event its pending run was
// set by, for an event schedule only (nil otherwise).
func RunEvent(sc wire.RoomSchedule) *wire.CloudEvent {
	if !OnEvent(sc.Recurrence) || sc.Event == nil {
		return nil
	}
	return sc.Event
}

// errNoType refuses a webhook request that names no event type.
var errNoType = errors.New("the request names no event type: send " + wire.HookEventHeader + " (or " + wire.GitHubEventHeader + "), or a top-level \"type\" in the JSON body")
