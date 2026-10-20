package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Schedules that run when the platform changes
// (docs/prd/2026-10-20-event-triggered-schedules.md). The gateway records
// each platform event once (services/gateway/platform_events.go) and hands
// it here:
//
//	POST /internal/v1/platform-events   wire.PlatformEvent → wire.PlatformEventReply (gateway only)
//
// The dispatch only sets runs: every enabled schedule that follows the
// event (schedule.Follows, in the organizations the event reaches) and may
// run (runnable: never one waiting for an agent) gets its next run now,
// coalesced per schedule by schedule.MinInterval in the store
// (TriggerEventSchedule). The scheduler then claims and runs them through
// its one run path (runClaimedSchedule), exactly like a slot of the
// clock: the same claim, budget, audit and agent rules, the creator still
// a member who may post. The run's text carries the event's facts
// (schedule.RunText).

// handleInternalPlatformEvent dispatches one platform event.
func (s *Service) handleInternalPlatformEvent(w http.ResponseWriter, r *http.Request) {
	if !s.schedulesReady(w) {
		return
	}
	var ev wire.PlatformEvent
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &ev) {
		return
	}
	if !slices.Contains(wire.PlatformEventNames, ev.Event) {
		httpx.Error(w, fmt.Sprintf("unknown platform event %q", ev.Event), http.StatusBadRequest)
		return
	}
	n, err := s.DispatchPlatformEvent(r.Context(), ev, time.Now())
	if err != nil {
		obs.ServerError(w, "dispatch platform event failed", err)
		httpx.Error(w, "dispatch platform event failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.PlatformEventReply{Triggered: n})
}

// DispatchPlatformEvent sets a run of every enabled schedule that follows
// ev and may run, and wakes the scheduler; it returns how many it set. A
// schedule it could not set is reported (after the others were set): the
// gateway's retry sets it, and the ones set already only take the event
// again (a pending run keeps its time).
func (s *Service) DispatchPlatformEvent(ctx context.Context, ev wire.PlatformEvent, now time.Time) (int, error) {
	if s.Schedules == nil {
		return 0, nil
	}
	if ev.AtMs <= 0 {
		ev.AtMs = now.UnixMilli()
	}
	list, err := s.Schedules.ListEventSchedules(ctx, ev.Tenants)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, sc := range list {
		if !schedule.Follows(sc.Recurrence, sc.TenantID, ev) || runnable(sc) != nil {
			continue
		}
		at, ok, err := s.Schedules.TriggerEventSchedule(ctx, sc.ScheduleID, ev, now.UnixMilli(), schedule.MinInterval.Milliseconds())
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", sc.ScheduleID, err))
			continue
		}
		if ok {
			n++
			log.Printf("realtime: schedule %s room=%s runs on %s at %s", sc.ScheduleID, sc.RoomID, ev.Event, time.UnixMilli(at).UTC().Format(time.RFC3339))
		}
	}
	log.Printf("realtime: platform event %s: %d schedule(s) set to run", schedule.EventLine(ev), n)
	if n > 0 {
		s.wakeScheduler()
	}
	return n, errors.Join(errs...)
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
