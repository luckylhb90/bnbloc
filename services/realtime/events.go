package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/ratelimit"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Events a room subscribes to (docs/prd/2026-10-21-room-events.md). A
// task's event trigger is the subscription; dispatchEvent is the one
// dispatcher of every event, whatever emitted it:
//
//   - platform events, recorded once by the gateway
//     (services/gateway/platform_events.go) and handed here:
//     POST /internal/v1/events   wire.EventDispatch → wire.EventDispatchReply (gateway only);
//   - room events, emitted by this service's publish tap from the room's
//     own feed (room_events.go), reaching only that room's tasks;
//   - webhook events, verified at a task's own URL (hooks.go), reaching
//     only that task.
//
// The dispatch only sets runs: every enabled task whose trigger takes the
// event (schedule.Triggers: its source, types and filter, and never an
// event a run of its own room's tasks caused) and that may run (runnable)
// gets its next run now, coalesced per task by schedule.MinInterval in the
// store (TriggerEventSchedule). Rate limits bound what one source and one
// task's trigger may set (eventLimits); an event over a limit is dropped,
// counted on the task (store.CountEventDropped) and told once to the
// room's owner. The scheduler then claims and runs the set tasks through
// its one run path (runClaimedSchedule), exactly like a slot of the clock;
// the run's text carries the event fenced as data (schedule.RunText).

// Event rate limits: a product bound of the feature (an event trigger is a
// reaction, not a stream processor; a burst past these is a storm the
// filters should narrow), not deployment data. Coalescing already runs a
// task at most once per schedule.MinInterval; these bound the dispatch
// work and the store writes a noisy source can cause.
const (
	// sourceEventRate / sourceEventBurst bound the events one source (the
	// platform, one room, one webhook) may set runs for, per second.
	sourceEventRate  = 5.0
	sourceEventBurst = 60.0
	// taskEventRate / taskEventBurst bound the events one task's trigger
	// may take, per second (a webhook's requests spend it too).
	taskEventRate  = 1.0
	taskEventBurst = 20.0
)

// eventLimiters are the dispatcher's two budgets, per replica's share
// (ratelimit divides by the live replica count).
type eventLimiters struct {
	source, task ratelimit.Lazy
}

func (s *Service) replicas() int { return s.Peers.Set().Size() }

func (s *Service) sourceLimiter() *ratelimit.Limiter {
	return s.eventLimits.source.Get(sourceEventRate, sourceEventBurst, s.replicas)
}

func (s *Service) taskLimiter() *ratelimit.Limiter {
	return s.eventLimits.task.Get(taskEventRate, taskEventBurst, s.replicas)
}

// eventDelivery is one event and who it may reach: the tenants and room of
// reach (a platform event: its tenants, or every one; a room's event: its
// tenant and room), or only one task (a webhook's: only, already read).
// causeRoom is the room whose scheduled run caused it ("" for none).
type eventDelivery struct {
	event     wire.CloudEvent
	reach     store.EventScheduleQuery
	only      *wire.RoomSchedule
	causeRoom string
}

// handleInternalEvent dispatches one platform event from the gateway.
func (s *Service) handleInternalEvent(w http.ResponseWriter, r *http.Request) {
	if !s.schedulesReady(w) {
		return
	}
	var in wire.EventDispatch
	if !httpx.DecodeJSON(w, r, maxChatBodyBytes, &in) {
		return
	}
	ev, err := wire.ValidEvent(in.Event)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ev.Source != wire.SourcePlatform || !slices.Contains(wire.PlatformEventTypes, ev.Type) {
		httpx.Error(w, fmt.Sprintf("not a platform event: %s %s", ev.Source, ev.Type), http.StatusBadRequest)
		return
	}
	d := eventDelivery{event: ev, reach: store.EventScheduleQuery{Tenants: in.Tenants}}
	if in.CauseJob != "" && in.CauseTenant != "" {
		// A publish an agent made under its job's grant: the room whose
		// scheduled run started that job does not take it.
		d.causeRoom = s.causeRoom(tenant.NewContext(r.Context(), in.CauseTenant), "", in.CauseJob)
	}
	n, dropped, err := s.dispatchEvent(r.Context(), d, time.Now())
	if err != nil {
		obs.ServerError(w, "dispatch event failed", err)
		httpx.Error(w, "dispatch event failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.EventDispatchReply{Triggered: n, Dropped: dropped})
}

// dispatchEvent sets a run of every enabled task d reaches whose trigger
// takes the event and that may run, within the rate limits, and wakes the
// scheduler; it returns how many it set and how many it dropped. A task it
// could not set is reported (after the others were set): a platform
// event's retry sets it, and the ones set already only take the event
// again (a pending run keeps its time).
func (s *Service) dispatchEvent(ctx context.Context, d eventDelivery, now time.Time) (triggered, dropped int, err error) {
	if s.Schedules == nil {
		return 0, 0, nil
	}
	ev := d.event
	var list []wire.RoomSchedule
	if d.only != nil {
		if d.only.Enabled {
			list = []wire.RoomSchedule{*d.only}
		}
	} else if list, err = s.Schedules.ListEventSchedules(ctx, d.reach); err != nil {
		return 0, 0, err
	}
	var takers []wire.RoomSchedule
	for _, sc := range list {
		if schedule.Triggers(sc, ev, d.causeRoom) && runnable(sc) == nil {
			takers = append(takers, sc)
		}
	}
	if len(takers) == 0 {
		return 0, 0, nil
	}
	sourceOK := s.sourceLimiter().Allow(ev.Source)
	var errs []error
	for _, sc := range takers {
		if !sourceOK || !s.taskLimiter().Allow(sc.ScheduleID) {
			dropped++
			s.dropEvent(ctx, sc, ev)
			continue
		}
		at, ok, err := s.Schedules.TriggerEventSchedule(ctx, sc.ScheduleID, ev, now.UnixMilli(), schedule.MinInterval.Milliseconds())
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", sc.ScheduleID, err))
			continue
		}
		if ok {
			triggered++
			log.Printf("realtime: schedule %s room=%s runs on %s at %s", sc.ScheduleID, sc.RoomID, ev.Type, time.UnixMilli(at).UTC().Format(time.RFC3339))
		}
	}
	log.Printf("realtime: event %s from %s: %d task(s) set to run, %d dropped", ev.Type, ev.Source, triggered, dropped)
	if triggered > 0 {
		s.wakeScheduler()
	}
	return triggered, dropped, errors.Join(errs...)
}

// dropEvent counts an event a rate limit dropped from sc's trigger and, the
// first time since sc was last edited, tells the room's owner.
func (s *Service) dropEvent(ctx context.Context, sc wire.RoomSchedule, ev wire.CloudEvent) {
	tctx := tenant.NewContext(ctx, sc.TenantID)
	notice, err := s.Schedules.CountEventDropped(tctx, sc.ScheduleID)
	slog.Warn("bnbloc realtime: event dropped over a rate limit", "event", "bnbloc.schedule.event_dropped",
		"schedule", sc.ScheduleID, "room", sc.RoomID, "type", ev.Type, "source", ev.Source, "error", err)
	if err != nil || !notice || s.Notifications == nil || s.Notifications.Emitter == nil {
		return
	}
	owners := s.roomOwners(tctx, sc.RoomID)
	if len(owners) == 0 {
		return
	}
	if _, err := s.Notifications.Emitter.Emit(tctx, dropNotice(sc, ev, owners)); err != nil {
		slog.Warn("bnbloc realtime: drop notice enqueue failed", "event", "bnbloc.notify.enqueue_failed",
			"type", wire.NotificationScheduleNotice, "error", err)
	}
}

// dropNotice is the notice of sc's first dropped event, to the room's
// owners. Its dedup key is the task as last edited: one notice per edit.
func dropNotice(sc wire.RoomSchedule, ev wire.CloudEvent, owners []string) notify.Event {
	return notify.Event{
		Type: wire.NotificationScheduleNotice, RoomID: sc.RoomID, Recipients: owners,
		Title: "A scheduled task is getting more events than it may take",
		Text: fmt.Sprintf("The task %q dropped %s from %s and drops any more beyond its limit; the Schedules list counts them. "+
			"Narrow its trigger with a filter, or slow the sender.", taskLabel(sc.Text), ev.Type, ev.Source),
		DedupKey: fmt.Sprintf("schedule-drops:%s:%d", sc.ScheduleID, sc.UpdatedAtMs),
	}
}

// taskLabel is a task's text in a few words, for a notice.
func taskLabel(text string) string {
	words := strings.Fields(text)
	if len(words) > 12 {
		return strings.Join(words[:12], " ") + "…"
	}
	return strings.Join(words, " ")
}

// roomOwners are the people who own roomID (the room's owner role).
func (s *Service) roomOwners(ctx context.Context, roomID string) []string {
	if s.RoomRoles == nil {
		return nil
	}
	members, err := s.RoomRoles.Members(ctx, roomID)
	if err != nil {
		slog.Warn("bnbloc realtime: read room owners failed", "room", roomID, "error", err)
		return nil
	}
	var out []string
	for _, m := range members {
		if m.Role == store.RoleOwner && !m.AgentHost {
			out = append(out, m.UserID)
		}
	}
	return out
}

// schedulerWake is the channel that wakes RunScheduler before its minute.
func (s *Service) schedulerWake() chan struct{} {
	s.wakeOnce.Do(func() { s.wake = make(chan struct{}, 1) })
	return s.wake
}

// wakeScheduler asks RunScheduler for a tick now (a wake already pending
// covers it).
func (s *Service) wakeScheduler() {
	select {
	case s.schedulerWake() <- struct{}{}:
	default:
	}
}
