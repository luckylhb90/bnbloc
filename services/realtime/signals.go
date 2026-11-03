package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A signal (docs/prd/2026-10-26-rooms-work-together.md §2.3, D8): an event
// an agent's job or an AI task's run sends to its own room, so the room's
// next station (another task) takes it:
//
//	POST /internal/v1/rooms/{roomId}/signals   wire.SignalRequest → wire.MemberEventReply
//
// The gateway signs the caller: a bridge's owner for a job (the room tool
// signal, through the public door POST /v1/rooms/{roomId}/signals), or a
// task's creator for its AI run (the gateway's schedule run). The signal is
// sent as the job's requester (or the task's creator): its source is that
// member's (bnbloc/member/<subject>), its type never the platform's, and it
// carries the job's (or the run's) event as its cause and keeps its chain.
// The task whose run sent it never takes it (schedule.SignalTriggers); the
// room's other tasks do, within the dispatch's rate limits. Every send is
// audited (type and subject, never data).

func (s *Service) handleInternalSignal(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.roomCaller(w, r)
	if !ok || !s.schedulesReady(w) {
		return
	}
	if s.Dispatches == nil {
		httpx.Unavailable(w, "agent jobs")
		return
	}
	var in wire.SignalRequest
	if !httpx.DecodeJSON(w, r, wire.MaxEventDataBytes+4<<10, &in) {
		return
	}
	reply, err := s.Signal(r.Context(), roomID, caller, in, time.Now())
	var refused *signalRefusal
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusAccepted, reply)
	case errors.As(err, &refused):
		httpx.WriteStatus(w, refused.status, refused.msg, err)
	case errors.Is(err, wire.ErrEventDataTooLarge):
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		obs.ServerError(w, "signal failed", err)
		httpx.Error(w, "signal failed", http.StatusInternalServerError)
	}
}

// signalRefusal is a signal the caller may not send (a 4xx with why).
type signalRefusal struct {
	status int
	msg    string
}

func (e *signalRefusal) Error() string { return e.msg }

func refuseSignal(status int, format string, args ...any) error {
	return &signalRefusal{status: status, msg: fmt.Sprintf(format, args...)}
}

// signalOrigin is who a signal is sent as and what caused it.
type signalOrigin struct {
	sender, channel   string
	cause, chain      string
	causeTask, origin string
}

// Signal checks who sends in.Type and dispatches it to roomID's tasks.
func (s *Service) Signal(ctx context.Context, roomID, caller string, in wire.SignalRequest, now time.Time) (wire.MemberEventReply, error) {
	in.JobID, in.ScheduleID = strings.TrimSpace(in.JobID), strings.TrimSpace(in.ScheduleID)
	if (in.JobID == "") == (in.ScheduleID == "") {
		return wire.MemberEventReply{}, refuseSignal(http.StatusBadRequest, "a signal names its job (jobId) or its task (scheduleId), one of them")
	}
	o, err := s.signalOrigin(ctx, roomID, caller, in)
	if err != nil {
		return wire.MemberEventReply{}, err
	}
	if _, err := membership.RoomScope(ctx, s.Membership, roomID, o.sender, membership.Chat); err != nil {
		return wire.MemberEventReply{}, refuseSignal(http.StatusForbidden, "%s, whom the signal is sent as, may no longer post in the room", o.sender)
	}
	ev, err := schedule.MemberEventOf(o.sender, wire.MemberEvent{Type: in.Type, Subject: in.Subject, Data: in.Data}, now)
	if errors.Is(err, wire.ErrEventDataTooLarge) {
		return wire.MemberEventReply{}, err
	}
	if err != nil {
		return wire.MemberEventReply{}, refuseSignal(http.StatusBadRequest, "%s", err.Error())
	}
	ev.ID = ids.New("evt_")
	ev = wire.WithLineage(ev, o.channel, o.cause, o.chain)
	tid := tenant.FromContext(ctx)
	n, dropped, err := s.dispatchEvent(ctx, eventDelivery{event: ev, reach: store.EventScheduleQuery{Tenants: []string{tid}, RoomID: roomID},
		signal: true, causeTask: o.causeTask}, now)
	if err != nil {
		return wire.MemberEventReply{}, err
	}
	audit.Record(ctx, s.Audit, store.ExtAuditRecord{
		Kind: wire.AuditKindEvent, Name: roomID, Action: "signal", Actor: o.sender, TenantID: tid, Outcome: wire.AuditOutcomeOK, AtMs: now.UnixMilli(),
		Detail: fmt.Sprintf("%s (subject %q) signalled by %s for %s: %d task(s) set, %d dropped", ev.Type, ev.Subject, o.origin, o.sender, n, dropped),
	})
	return wire.MemberEventReply{Event: ev, Triggered: n, Dropped: dropped}, nil
}

// signalOrigin resolves who a signal is sent as and what caused it: a job
// the caller's bridge runs now (its requester, its channel, its run's event,
// the task whose run started it), or a task of the caller's whose run is
// answering (its creator, its channel, its pending event, itself).
func (s *Service) signalOrigin(ctx context.Context, roomID, caller string, in wire.SignalRequest) (signalOrigin, error) {
	if in.ScheduleID != "" {
		sc, err := s.Schedules.GetSchedule(ctx, roomID, in.ScheduleID)
		if errors.Is(err, store.ErrScheduleNotFound) {
			return signalOrigin{}, refuseSignal(http.StatusNotFound, "no task %s in this room", in.ScheduleID)
		}
		if err != nil {
			return signalOrigin{}, err
		}
		if sc.CreatedBy != caller {
			return signalOrigin{}, refuseSignal(http.StatusForbidden, "only the task's creator's run signals for it")
		}
		o := signalOrigin{sender: sc.CreatedBy, channel: sc.ChannelID, causeTask: sc.ScheduleID, origin: "task " + sc.ScheduleID}
		if ev := schedule.RunEvent(sc); ev != nil {
			o.cause, o.chain = ev.ID, wire.ChainOf(*ev)
		}
		return o, nil
	}
	job, err := store.LoadLeasedJob(ctx, s.Dispatches, in.JobID, roomID, caller)
	switch {
	case errors.Is(err, store.ErrJobNotInRoom):
		return signalOrigin{}, refuseSignal(store.LeasedJobStatus(err), "job %s is not in this room", in.JobID)
	case errors.Is(err, store.ErrJobNotLive), errors.Is(err, store.ErrJobNotCallers):
		return signalOrigin{}, refuseSignal(store.LeasedJobStatus(err), "a signal is sent only while its job runs on the caller's bridge")
	case err != nil:
		return signalOrigin{}, err
	}
	task, err := s.causeTask(ctx, job)
	if err != nil {
		return signalOrigin{}, err
	}
	return signalOrigin{sender: job.RequesterUserID, channel: job.ChannelID, cause: job.CauseEventID, chain: job.ChainEventID,
		causeTask: task, origin: "job " + job.JobID}, nil
}

// causeTask is the task whose run started j ("" for none): j's request, or
// the request at the root of the dispatch chain it sits in, posted by a
// scheduled run (its client id names the task, schedule.RunSlotSchedule).
// A link of the chain that cannot be read fails the signal (a missed
// self-trigger guard would let a task loop on itself).
func (s *Service) causeTask(ctx context.Context, j store.AgentJob) (string, error) {
	for hop := 0; hop <= wire.MaxDispatchDepth; hop++ {
		if task := schedule.RunSlotSchedule(j.ClientMsgID); task != "" {
			return task, nil
		}
		if j.DispatchedByJobID == "" {
			return "", nil
		}
		up, err := s.Dispatches.GetAgentRun(ctx, j.DispatchedByJobID)
		if errors.Is(err, store.ErrAgentJobNotFound) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read the job's dispatch chain: %w", err)
		}
		j = up
	}
	return "", nil
}
