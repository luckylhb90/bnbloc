package schedule

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A schedule with an event recurrence (wire.RecurEvent) runs when the
// platform changes instead of on the clock
// (docs/prd/2026-10-20-event-triggered-schedules.md): it follows one or
// more of wire.PlatformEventNames, and for component.published may name
// the one component it follows. This file owns what such a recurrence may
// say (validateEvent), which schedules an event reaches (Follows) and what
// a run's text carries of the event (EventLine, RunText). A burst of
// events is coalesced per schedule by MinInterval where the run is set
// (store.RoomScheduleStore.TriggerEventSchedule).

// maxEventComponentLen bounds an event recurrence's component name.
const maxEventComponentLen = 128

// eventAuthor names who wrote an event's facts in the fence a run's text
// carries them in.
const eventAuthor = "bnbloc (a platform event)"

// validateEvent normalizes an event recurrence: its events are known,
// unique and in the list's order; a component name only with
// component.published.
func validateEvent(rec wire.ScheduleRecurrence) (wire.ScheduleRecurrence, error) {
	out := wire.ScheduleRecurrence{Kind: wire.RecurEvent}
	seen := map[string]bool{}
	for _, e := range rec.Events {
		e = strings.TrimSpace(e)
		if !slices.Contains(wire.PlatformEventNames, e) {
			return rec, fmt.Errorf("recurrence: unknown event %q (%s)", e, strings.Join(wire.PlatformEventNames, ", "))
		}
		seen[e] = true
	}
	for _, e := range wire.PlatformEventNames {
		if seen[e] {
			out.Events = append(out.Events, e)
		}
	}
	if len(out.Events) == 0 {
		return rec, fmt.Errorf("recurrence: an event schedule follows at least one event (%s)", strings.Join(wire.PlatformEventNames, ", "))
	}
	if c := strings.TrimSpace(rec.Component); c != "" {
		if !seen[wire.ComponentPublished] {
			return rec, errors.New("recurrence: a component name goes with the event " + wire.ComponentPublished)
		}
		if len(c) > maxEventComponentLen || !pkgmeta.ValidPackageName(c) {
			return rec, fmt.Errorf("recurrence: %q is not a component name", c)
		}
		out.Component = c
	}
	return out, nil
}

// OnEvent reports a recurrence that runs on platform events, never on the
// clock (it has no next run until an event it follows arrives).
func OnEvent(rec wire.ScheduleRecurrence) bool { return rec.Kind == wire.RecurEvent }

// Follows reports whether a schedule of tenantID with recurrence rec runs
// on ev: the recurrence follows the event (and, for a component publish
// with a component name, that component), and the event reaches the
// tenant (an event naming organizations reaches only them).
func Follows(rec wire.ScheduleRecurrence, tenantID string, ev wire.PlatformEvent) bool {
	if !OnEvent(rec) || !slices.Contains(rec.Events, ev.Event) {
		return false
	}
	if len(ev.Tenants) > 0 && !slices.Contains(ev.Tenants, tenantID) {
		return false
	}
	return ev.Event != wire.ComponentPublished || rec.Component == "" || rec.Component == ev.Name
}

// EventLine is the facts of ev on one line, as a run reads them:
// "component.published kind=mcp name=browser version=1.2.0 rollout=auto".
// Facts are public (names, versions, targets); the organizations an event
// reaches are not among them.
func EventLine(ev wire.PlatformEvent) string {
	parts := []string{ev.Event}
	add := func(k, v string) {
		if v = strings.Join(strings.Fields(v), "_"); v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("kind", ev.Kind)
	add("name", ev.Name)
	add("version", ev.Version)
	add("previous", ev.Previous)
	add("rollout", ev.Rollout)
	add("targets", strings.Join(ev.Targets, ","))
	return strings.Join(parts, " ")
}

// RunText is what a run of sc gives the model or an agent: the schedule's
// text and, when the run answers a platform event (an event schedule whose
// run an event set), one line of its facts fenced as data, never
// instructions. Bounded to what one chat message may hold: the schedule's
// own text is cut, never the facts. A notice sent to people as written
// reads the facts unfenced (EventLine).
func RunText(sc wire.RoomSchedule) string {
	ev := RunEvent(sc)
	if ev == nil {
		return sc.Text
	}
	facts := "\n\n" + roomlog.Fence(eventAuthor, EventLine(*ev))
	return roomlog.Truncate(sc.Text, wire.MaxChatTextBytes-len(facts)) + facts
}

// RunEvent is the platform event a run of sc answers: the event its
// pending run was set by, for an event schedule only (nil otherwise).
func RunEvent(sc wire.RoomSchedule) *wire.PlatformEvent {
	if !OnEvent(sc.Recurrence) || sc.Event == nil {
		return nil
	}
	return sc.Event
}
