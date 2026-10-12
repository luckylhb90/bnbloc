package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Scheduled prompts (N2). A schedule is ADDRESSING, not orchestration: a
// member's own message, posted into the room at a time, as that member,
// through PostChat — the exact path a typed message takes. An agent-addressed
// schedule therefore creates the same agent job the bridge claims by
// recipient, and the bridge enforces borrow authorization at drive time; the
// scheduler grants nothing.
//
// realtime owns schedules because it owns chat publish. Every replica runs
// the scheduler: the claim protocol (store.ClaimDueSchedules) keeps runs
// exactly-once across replicas, the claimed slot is posted by the room's
// owner (postScheduledAtOwner), and the per-slot clientMsgId keeps a
// re-claimed slot (crashed run) from posting twice.

// Schedule caps. These are protocol-level sanity bounds of the feature, not
// deployment data: a room's schedule list is a human-managed panel (more
// than 50 entries is not a usable panel, it is a script), and one member
// owning more than 20 schedules in one room is automation that belongs in an
// agent, not in room addressing. They bound the scheduler's per-room work and
// the chat volume a single member can automate.
const (
	maxSchedulesPerRoom   = 50
	maxSchedulesPerMember = 20
	// scheduleClaimLease is how long a claimed run is held before another
	// tick may re-claim it (a crashed run). A run is one local chat publish.
	scheduleClaimLease = 2 * time.Minute
	// scheduleClaimBatch bounds one tick's work.
	scheduleClaimBatch = 100
	// scheduleMisfireGrace is how late a run may still post. A slot missed by
	// more (realtime was down, or the post kept failing) is skipped and
	// recorded, not posted hours late: a "daily 09:00" prompt at 15:00 is
	// wrong, and the next slot comes anyway.
	scheduleMisfireGrace = time.Hour
)

// RoomRoleStore reads room existence and member roles (the shared room
// store). Schedule moderation (owner/admin pause/delete) and the scheduler's
// "room deleted" diagnosis use it.
type RoomRoleStore interface {
	GetRoom(ctx context.Context, roomID string) (store.Room, error)
	MemberRole(ctx context.Context, roomID, userID string) (store.Role, bool)
}

func (s *Service) registerScheduleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rooms/{roomId}/schedules", s.handleListSchedules)
	mux.HandleFunc("POST /v1/rooms/{roomId}/schedules", s.handleCreateSchedule)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}/schedules/{scheduleId}", s.handlePatchSchedule)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/schedules/{scheduleId}", s.handleDeleteSchedule)
	mux.HandleFunc("POST /v1/rooms/{roomId}/schedules/{scheduleId}/run", s.handleRunSchedule)
}

func (s *Service) schedulesReady(w http.ResponseWriter) bool {
	if s.Schedules == nil {
		obs.ServerError(w, "schedules unavailable", nil)
		httpx.Error(w, "schedules unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// canModerateSchedules reports whether userID is the room's owner or an admin.
func (s *Service) canModerateSchedules(ctx context.Context, roomID, userID string) bool {
	if s.RoomRoles == nil {
		return false
	}
	role, ok := s.RoomRoles.MemberRole(ctx, roomID, userID)
	return ok && role.CanInvite()
}

// scheduleInput is the create body and (with every field optional) the
// patch body.
type scheduleInput struct {
	Text       *string                  `json:"text"`
	ChannelID  *string                  `json:"channelId"`
	Recipient  *wire.ScheduleRecipient  `json:"recipient"`
	Recurrence *wire.ScheduleRecurrence `json:"recurrence"`
	Timezone   *string                  `json:"timezone"`
	Enabled    *bool                    `json:"enabled"`
}

func validScheduleText(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("text is required")
	}
	if len(text) > maxChatTextBytes {
		return errors.New("text too long")
	}
	return nil
}

// planNextRun computes the next run of an enabled schedule from now; a
// schedule that would never fire is rejected rather than silently stored.
func planNextRun(sc *wire.RoomSchedule, now time.Time) error {
	rec, loc, err := schedule.Validate(sc.Recurrence, sc.Timezone)
	if err != nil {
		return err
	}
	sc.Recurrence = rec
	sc.NextRunAtMs = 0
	if !sc.Enabled {
		return nil
	}
	next, ok := schedule.Next(rec, loc, now)
	if !ok {
		return errors.New("the schedule never fires (is the time in the past?)")
	}
	sc.NextRunAtMs = next.UnixMilli()
	return nil
}

func writeScheduleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrScheduleNotFound):
		httpx.Error(w, "schedule not found", http.StatusNotFound)
	default:
		obs.ServerError(w, "schedule store error", err)
		httpx.Error(w, "schedule store error", http.StatusInternalServerError)
	}
}

func (s *Service) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.room(w, r, membership.AnyMember)
	if !ok || !s.schedulesReady(w) {
		return
	}
	list, err := s.Schedules.ListSchedules(r.Context(), roomID)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

func (s *Service) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	// A schedule posts as its creator, which only a member who may chat may.
	roomID, userID, _, ok := s.room(w, r, membership.Chat)
	if !ok || !s.schedulesReady(w) {
		return
	}
	var in scheduleInput
	if !httpx.DecodeJSON(w, r, maxChatBodyBytes, &in) {
		return
	}
	now := time.Now()
	sc := wire.RoomSchedule{
		ScheduleID: ids.New("sch_"), RoomID: roomID, CreatedBy: userID, Enabled: true,
		CreatedAtMs: now.UnixMilli(), UpdatedAtMs: now.UnixMilli(),
	}
	if in.Text != nil {
		sc.Text = *in.Text
	}
	if in.Recipient != nil {
		sc.Recipient = *in.Recipient
	}
	if in.Recurrence != nil {
		sc.Recurrence = *in.Recurrence
	}
	if in.Timezone != nil {
		sc.Timezone = strings.TrimSpace(*in.Timezone)
	}
	if in.Enabled != nil {
		sc.Enabled = *in.Enabled
	}
	if in.ChannelID != nil {
		sc.ChannelID = strings.TrimSpace(*in.ChannelID)
	}
	if status, err := s.checkScheduleSpec(r.Context(), &sc, now); err != nil {
		writeScheduleSpecErr(w, status, err)
		return
	}
	room, mine, err := s.Schedules.CountSchedules(r.Context(), roomID, userID)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	if room >= maxSchedulesPerRoom {
		httpx.Error(w, fmt.Sprintf("this room already has %d schedules (the maximum)", maxSchedulesPerRoom), http.StatusConflict)
		return
	}
	if mine >= maxSchedulesPerMember {
		httpx.Error(w, fmt.Sprintf("you already own %d schedules in this room (the maximum)", maxSchedulesPerMember), http.StatusConflict)
		return
	}
	created, err := s.Schedules.CreateSchedule(r.Context(), sc)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, created)
}

// checkScheduleSpec validates text, recipient, channel, recurrence and
// timezone, normalizing sc and planning its next run.
// writeScheduleSpecErr writes checkScheduleSpec's refusal. Its 5xx (a failed
// channel lookup) is logged and never echoes the store error to the caller.
func writeScheduleSpecErr(w http.ResponseWriter, status int, err error) {
	msg := err.Error()
	if status >= http.StatusInternalServerError {
		msg = "schedule channel lookup failed"
	}
	httpx.WriteStatus(w, status, msg, err)
}

func (s *Service) checkScheduleSpec(ctx context.Context, sc *wire.RoomSchedule, now time.Time) (int, error) {
	if err := validScheduleText(sc.Text); err != nil {
		if len(sc.Text) > maxChatTextBytes {
			return http.StatusRequestEntityTooLarge, err
		}
		return http.StatusBadRequest, err
	}
	rcpt, err := schedule.NormalizeRecipient(sc.Recipient)
	if err != nil {
		return http.StatusBadRequest, err
	}
	sc.Recipient = rcpt
	if sc.ChannelID != "" {
		if _, err := s.resolveChannel(ctx, sc.RoomID, sc.ChannelID); err != nil {
			return http.StatusNotFound, err
		}
		if sc.Recipient.Kind == wire.ScheduleRecipientAgent {
			people, err := s.isPeopleChannel(ctx, sc.RoomID, sc.ChannelID)
			if err != nil {
				return http.StatusInternalServerError, err
			}
			if people {
				return http.StatusBadRequest, errAgentInPeopleChat
			}
		}
	}
	if err := planNextRun(sc, now); err != nil {
		return http.StatusBadRequest, err
	}
	return 0, nil
}

// handlePatchSchedule edits a schedule. The creator may change anything; the
// room owner or an admin may only pause another member's schedule (resuming
// would post as someone else, so only the creator resumes).
func (s *Service) handlePatchSchedule(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.AnyMember)
	if !ok || !s.schedulesReady(w) {
		return
	}
	cur, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	var in scheduleInput
	if !httpx.DecodeJSON(w, r, maxChatBodyBytes, &in) {
		return
	}
	if cur.CreatedBy != userID {
		pauseOnly := in.Enabled != nil && !*in.Enabled && in.Text == nil && in.ChannelID == nil &&
			in.Recipient == nil && in.Recurrence == nil && in.Timezone == nil
		if !pauseOnly || !s.canModerateSchedules(r.Context(), roomID, userID) {
			httpx.Error(w, "only the creator may change this schedule (owners and admins may pause it)", http.StatusForbidden)
			return
		}
	}
	now := time.Now()
	next := cur
	timing := false
	if in.Text != nil {
		next.Text = *in.Text
	}
	if in.ChannelID != nil {
		next.ChannelID = strings.TrimSpace(*in.ChannelID)
	}
	if in.Recipient != nil {
		next.Recipient = *in.Recipient
	}
	if in.Recurrence != nil {
		next.Recurrence, timing = *in.Recurrence, true
	}
	if in.Timezone != nil {
		next.Timezone, timing = strings.TrimSpace(*in.Timezone), true
	}
	if in.Enabled != nil && *in.Enabled != cur.Enabled {
		next.Enabled, timing = *in.Enabled, true
		if next.Enabled {
			// Resuming clears the reason it was paused (auto-pause error).
			next.LastError = ""
		}
	}
	saved := next.NextRunAtMs
	if status, err := s.checkScheduleSpec(r.Context(), &next, now); err != nil {
		writeScheduleSpecErr(w, status, err)
		return
	}
	if !timing && next.Enabled && saved > 0 {
		// A text/recipient/channel edit keeps the planned slot (a run already
		// due re-posts under the same slot id, which dedupes).
		next.NextRunAtMs = saved
	}
	next.UpdatedAtMs = now.UnixMilli()
	if err := s.Schedules.UpdateSchedule(r.Context(), next); err != nil {
		writeScheduleErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, next)
}

func (s *Service) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.AnyMember)
	if !ok || !s.schedulesReady(w) {
		return
	}
	cur, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	if cur.CreatedBy != userID && !s.canModerateSchedules(r.Context(), roomID, userID) {
		httpx.Error(w, "only the creator, the room owner, or an admin may delete this schedule", http.StatusForbidden)
		return
	}
	if err := s.Schedules.DeleteSchedule(r.Context(), roomID, cur.ScheduleID); err != nil {
		writeScheduleErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunSchedule posts a schedule's message now (creator only: it posts as
// the creator). It does not move the planned next run.
func (s *Service) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.AnyMember)
	if !ok || !s.schedulesReady(w) {
		return
	}
	cur, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	if cur.CreatedBy != userID {
		httpx.Error(w, "only the creator may run this schedule", http.StatusForbidden)
		return
	}
	now := time.Now()
	seq, err := s.postScheduled(r.Context(), cur, now)
	res := store.ScheduleRunResult{ScheduleID: cur.ScheduleID, AtMs: now.UnixMilli(), Status: wire.ScheduleRunOK, Seq: seq}
	if err != nil {
		res.Status, res.Error = wire.ScheduleRunError, err.Error()
	}
	if cerr := s.Schedules.CompleteScheduleRun(r.Context(), res); cerr != nil {
		log.Printf("realtime: schedule %s record run: %v", cur.ScheduleID, cerr)
	}
	if err != nil {
		writeChatPostErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"sequence": seq})
}

// postScheduled posts sc's message as its creator through PostChat. slot
// identifies the run (the dedupe key of its post).
func (s *Service) postScheduled(ctx context.Context, sc wire.RoomSchedule, slot time.Time) (int64, error) {
	post := ChatPost{
		RoomID:      sc.RoomID,
		Author:      sc.CreatedBy,
		Text:        sc.Text,
		ClientMsgID: schedule.RunSlotClientMsgID(sc.ScheduleID, slot),
		ChannelID:   sc.ChannelID,
		Scheduled:   &wire.ScheduledMarker{ScheduleID: sc.ScheduleID},
	}
	if sc.Recipient.Kind == wire.ScheduleRecipientAgent {
		post.Recipient = &ChatRecipient{OwnerID: sc.Recipient.OwnerID, Provider: sc.Recipient.Provider, Core: sc.Recipient.Core}
	}
	posted, err := s.PostChat(ctx, post)
	return posted.Sequence, err
}

// ---------------------------------------------------------------- scheduler

// RunScheduler wakes at each minute boundary and runs the due schedules
// until ctx ends. A nil schedule store runs nothing.
func (s *Service) RunScheduler(ctx context.Context) {
	if s.Schedules == nil {
		return
	}
	for {
		now := time.Now()
		// One second past the boundary: a slot at HH:MM:00 is due by then.
		wait := now.Truncate(time.Minute).Add(time.Minute + time.Second).Sub(now)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.RunDueSchedules(ctx, time.Now())
	}
}

// RunDueSchedules claims and runs every schedule due at now, returning how
// many it processed. Exported for tests and for an operator-triggered tick.
func (s *Service) RunDueSchedules(ctx context.Context, now time.Time) int {
	if s.Schedules == nil {
		return 0
	}
	done := 0
	for {
		token, due, err := s.Schedules.ClaimDueSchedules(ctx, now.UnixMilli(), now.Add(scheduleClaimLease).UnixMilli(), scheduleClaimBatch)
		if err != nil {
			log.Printf("realtime: scheduler claim: %v", err)
			return done
		}
		for _, sc := range due {
			s.runClaimedSchedule(ctx, sc, token, now)
		}
		done += len(due)
		if len(due) < scheduleClaimBatch {
			return done
		}
	}
}

// runClaimedSchedule runs one claimed slot and records the outcome.
func (s *Service) runClaimedSchedule(ctx context.Context, sc wire.RoomSchedule, token string, now time.Time) {
	tctx := tenant.NewContext(ctx, sc.TenantID)
	slot := time.UnixMilli(sc.NextRunAtMs)
	res := store.ScheduleRunResult{ScheduleID: sc.ScheduleID, ClaimToken: token, AtMs: now.UnixMilli()}
	finish := func() {
		if err := s.Schedules.CompleteScheduleRun(tctx, res); err != nil {
			log.Printf("realtime: schedule %s record run: %v", sc.ScheduleID, err)
		}
	}
	pause := func(status, why string) {
		res.Status, res.Error, res.Disable, res.NextRunAtMs = status, why, true, 0
		log.Printf("realtime: schedule %s room=%s paused: %s", sc.ScheduleID, sc.RoomID, why)
		finish()
	}
	loc, err := schedule.LoadZone(sc.Timezone)
	if err != nil {
		pause(wire.ScheduleRunError, err.Error())
		return
	}
	if next, ok := schedule.NextRun(sc.Recurrence, loc, slot, now); ok {
		res.NextRunAtMs = next.UnixMilli()
	} else {
		res.Disable = true // a one-shot that fired, or nothing left to fire
	}
	if late := now.Sub(slot); late > scheduleMisfireGrace {
		res.Status = wire.ScheduleRunSkipped
		res.Error = fmt.Sprintf("missed the %s run (%s late)", slot.In(loc).Format("2006-01-02 15:04 MST"), late.Round(time.Minute))
		finish()
		return
	}
	// Only the room's owner sequences its messages: a slot claimed here for a
	// room another replica owns is posted there (scaleout.go).
	seq, err := s.postScheduledAtOwner(tctx, sc, slot)
	switch {
	case err == nil:
		res.Status, res.Seq = wire.ScheduleRunOK, seq
	case membership.IsRefusal(err):
		// The creator must still be able to post here, exactly like a typed
		// message (PostChat checks it). A room deleted, or a creator removed
		// or made a visitor, pauses the schedule.
		why := "the creator may no longer post in the room"
		if s.RoomRoles != nil {
			if _, err := s.RoomRoles.GetRoom(tctx, sc.RoomID); err != nil {
				why = "the room no longer exists"
			}
		}
		pause(wire.ScheduleRunSkipped, why)
		return
	case errors.Is(err, errUnknownChannel):
		pause(wire.ScheduleRunError, "the schedule's channel no longer exists")
		return
	case errors.Is(err, errEmptyMessage), errors.Is(err, errMessageTooLong):
		pause(wire.ScheduleRunError, err.Error())
		return
	default:
		// Transient (persistence): keep the slot so the next tick retries it;
		// the misfire grace bounds the retries.
		res.Status, res.Error, res.NextRunAtMs, res.Disable = wire.ScheduleRunError, err.Error(), slot.UnixMilli(), false
		log.Printf("realtime: schedule %s room=%s post failed (will retry): %v", sc.ScheduleID, sc.RoomID, err)
	}
	finish()
}
