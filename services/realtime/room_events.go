package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's own events (docs/prd/2026-10-21-room-events.md §2.2): realtime
// sees every event of a room's feed (publish), so this publish tap is the
// one emitter of the room's CloudEvents, from what the room's owners
// already announce:
//
//   - room.file.added from artifact_updated with a new version (the room
//     service's publishArtifactEvent), caused by the agent job that
//     published it (wire.ArtifactUpdated.SourceJobID);
//   - room.member.joined from member_joined (the room service);
//   - room.app.data.changed from app_changed (the room service's app data
//     owner), caused by the agent job whose agent wrote
//     (wire.AppChanged.SourceJobID), coalesced per app and cause:
//     the first change at once, the rest of a window as one event at its
//     end (appEventWindow), never more than one event per window.
//
// Each goes to dispatchEvent reaching only the room's own tasks. The cause
// is the room whose scheduled run started the job (causeRoom), so a task
// never runs on what its own room's runs do. Emission is detached from the
// publish: the event is out already, and a slow store never holds a feed.

// roomEventTimeout bounds one room event's dispatch.
const roomEventTimeout = 30 * time.Second

// appEventWindow is the coalescing window of room.app.data.changed, and
// maxAppWindows bounds the windows one replica keeps open (one per app and
// cause with a change in the last window; past it a change is dropped and
// logged, never queued without bound).
const (
	appEventWindow = time.Minute
	maxAppWindows  = 10000
)

// emitRoomEvents is the publish tap: the room event ev stands for, if any.
func (s *Service) emitRoomEvents(ctx context.Context, ev wire.Event) {
	if s.Schedules == nil || ev.RoomID == "" {
		return
	}
	switch ev.Kind {
	case wire.EventArtifactUpdated:
		p, ok := wire.DecodePayload[wire.ArtifactUpdated](ev.Payload)
		if !ok || p.Reason != "" || p.CurrentVersion <= 0 {
			return // a thumbnail, or an artifact with no file yet
		}
		subject := strings.TrimSpace(p.FileName)
		if subject == "" {
			subject = strings.TrimSpace(p.Title)
		}
		s.goRoomEvent(ctx, ev, roomEventOf{typ: wire.RoomFileAdded, subject: subject, causeJob: p.SourceJobID}, wire.RoomFileAddedData{
			ArtifactID: p.ArtifactID, Kind: p.Kind, Title: p.Title, FileName: p.FileName, MimeType: p.MimeType,
			Version: p.CurrentVersion, By: p.UpdatedBy,
		})
	case wire.EventMemberJoined:
		if member := joinedMember(ev); member != "" {
			s.goRoomEvent(ctx, ev, roomEventOf{typ: wire.RoomMemberJoined, subject: member}, wire.RoomMemberJoinedData{UserID: member})
		}
	case wire.EventAppChanged:
		if c, ok := wire.DecodePayload[wire.AppChanged](ev.Payload); ok && c.AppID != "" {
			s.appChanged(ctx, ev, c)
		}
	case wire.EventAgentJobUpdated:
		if j, ok := wire.DecodePayload[wire.AgentJob](ev.Payload); ok && j.JobID != "" {
			if typ := jobEventType(j.Status); typ != "" {
				s.goRoomEvent(ctx, ev, roomEventOf{typ: typ, subject: j.JobID, causeJob: j.JobID, channel: j.ChannelID, ended: &j}, nil)
			}
		}
	}
}

// jobEventType is the room event a job status ends with ("" for a status
// that is not final: a failure the platform retries is requeued, never
// "failed").
func jobEventType(status string) string {
	switch store.AgentJobStatus(status) {
	case store.AgentJobReplied:
		return wire.RoomJobReplied
	case store.AgentJobFailed, store.AgentJobTimedOut:
		return wire.RoomJobFailed
	case store.AgentJobCancelled:
		return wire.RoomJobCancelled
	}
	return ""
}

// roomEventOf is what a room event says before its data: its type and
// subject, the channel it happened in, and the job that caused it (its
// cause and chain come from that job). ended is the job update a job
// event stands for: how the job ended is that update's, never the stored
// row's (which may lag the update just published).
type roomEventOf struct {
	typ, subject, causeJob, channel string
	ended                           *wire.AgentJob
}

// goRoomEvent dispatches one room event in the background, under the
// publish's tenant.
func (s *Service) goRoomEvent(ctx context.Context, ev wire.Event, of roomEventOf, data any) {
	tid := tenant.FromContext(ctx)
	if tid == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.WithoutCancel(ctx), tid), roomEventTimeout)
		defer cancel()
		s.dispatchRoomEvent(ctx, tid, ev.RoomID, of, data, time.UnixMilli(serverTimeIfUnset(ev.AtUnixMs)))
	}()
}

// dispatchRoomEvent builds the room's event (with its channel, and the
// cause and chain of the job that caused it) and dispatches it to the
// room's own tasks.
func (s *Service) dispatchRoomEvent(ctx context.Context, tid, roomID string, of roomEventOf, data any, at time.Time) {
	job, found := s.causeJob(ctx, roomID, of.causeJob)
	if of.ended != nil {
		if !found {
			return // a job event names its job; one that cannot be read emits nothing
		}
		data = jobEndedData(*of.ended, job)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	ce := wire.WithLineage(wire.CloudEvent{ID: ids.New("evt_"), Source: wire.SourceRoomPrefix + roomID, Type: of.typ,
		Time: wire.EventTime(at), Subject: of.subject, Data: raw}, of.channel, job.CauseEventID, job.ChainEventID)
	ce, err = wire.ValidEvent(ce)
	if err != nil {
		slog.Warn("bnbloc realtime: room event invalid", "room", roomID, "type", of.typ, "error", err)
		return
	}
	d := eventDelivery{event: ce, reach: store.EventScheduleQuery{Tenants: []string{tid}, RoomID: roomID}, causeRoom: s.causeRoom(ctx, roomID, of.causeJob)}
	if _, _, err := s.dispatchEvent(ctx, d, time.Now()); err != nil {
		slog.Warn("bnbloc realtime: room event dispatch failed", "event", "bnbloc.schedule.room_event_failed", "room", roomID, "type", of.typ, "error", err)
	}
}

// causeJob reads the job that caused a room event (found false for none,
// or one that cannot be read: the event then starts a chain of its own).
func (s *Service) causeJob(ctx context.Context, roomID, jobID string) (store.AgentJob, bool) {
	if jobID == "" || s.Dispatches == nil {
		return store.AgentJob{}, false
	}
	j, err := s.Dispatches.GetAgentRun(ctx, jobID)
	if err != nil || j.RoomID != roomID {
		if err != nil && !errors.Is(err, store.ErrAgentJobNotFound) {
			slog.Warn("bnbloc realtime: a room event's job could not be read", "room", roomID, "job", jobID, "error", err)
		}
		return store.AgentJob{}, false
	}
	return j, true
}

// jobEndedData is a job event's data: the job, its agent and requester and
// how it ended (the update's), and its reply (the stored run's, cut to
// wire.MaxJobEventReplyBytes).
func jobEndedData(u wire.AgentJob, j store.AgentJob) wire.RoomJobEndedData {
	d := wire.RoomJobEndedData{JobID: u.JobID, OwnerID: u.OwnerUserID, Core: u.Core, Provider: u.Provider, Requester: u.RequesterUserID,
		Status: u.Status, FailureReason: u.FailureReason}
	if reply, ok := store.RunDiag[wire.AgentRunReply](j, wire.RunDiagReply); ok {
		d.Reply = roomlog.Truncate(reply.Text, wire.MaxJobEventReplyBytes)
		d.ReplyCut = len(d.Reply) < len(reply.Text)
	}
	return d
}

// causeRoom is the room whose scheduled run caused what job did ("" for
// none): the job's own request, or the request at the root of the dispatch
// chain it sits in, was a scheduled run's post (its client id is a run
// slot's, schedule.IsRunSlotClientMsgID), made in that room. A lookup that
// fails is read as caused by roomID (fail closed: a missed trigger beats a
// loop); a job that is gone caused nothing that can still loop.
func (s *Service) causeRoom(ctx context.Context, roomID, jobID string) string {
	if jobID == "" || s.Dispatches == nil {
		return ""
	}
	for hop := 0; hop <= wire.MaxDispatchDepth && jobID != ""; hop++ {
		j, err := s.Dispatches.GetAgentRun(ctx, jobID)
		if errors.Is(err, store.ErrAgentJobNotFound) {
			return ""
		}
		if err != nil {
			slog.Warn("bnbloc realtime: an event's cause could not be read; its room's tasks do not take it", "room", roomID, "job", jobID, "error", err)
			return roomID
		}
		if schedule.IsRunSlotClientMsgID(j.ClientMsgID) {
			return j.RoomID
		}
		jobID = j.DispatchedByJobID
	}
	return ""
}

// appWindows coalesces room.app.data.changed per app and cause.
type appWindows struct {
	mu   sync.Mutex
	open map[string]*appWindow
}

// appWindow is one open window: the changes since its event and the newest
// change's collection.
type appWindow struct {
	pending    int
	collection string
}

// appChanged folds one app_changed event into its window: the first of a
// window is dispatched at once; the rest are one event at the window's end.
func (s *Service) appChanged(ctx context.Context, ev wire.Event, c wire.AppChanged) {
	tid := tenant.FromContext(ctx)
	if tid == "" {
		return
	}
	key := tid + "\x00" + ev.RoomID + "\x00" + c.AppID + "\x00" + c.SourceJobID
	w := &s.appWindows
	w.mu.Lock()
	if win, ok := w.open[key]; ok {
		win.pending++
		win.collection = c.Collection
		w.mu.Unlock()
		return
	}
	if w.open == nil {
		w.open = map[string]*appWindow{}
	}
	if len(w.open) >= maxAppWindows {
		w.mu.Unlock()
		slog.Warn("bnbloc realtime: room.app.data.changed dropped: too many apps changing at once", "room", ev.RoomID, "app", c.AppID)
		return
	}
	w.open[key] = &appWindow{}
	w.mu.Unlock()
	s.goRoomEvent(ctx, ev, roomEventOf{typ: wire.RoomAppDataChanged, subject: c.AppID, causeJob: c.SourceJobID}, wire.RoomAppDataChangedData{AppID: c.AppID, Collection: c.Collection, Changes: 1})
	s.closeAppWindowLater(ctx, ev, c, key)
}

// closeAppWindowLater ends key's window after appEventWindow: its pending
// changes become one event and a new window opens; none, and it closes.
func (s *Service) closeAppWindowLater(ctx context.Context, ev wire.Event, c wire.AppChanged, key string) {
	window := appEventWindow
	if s.AppEventWindow > 0 {
		window = s.AppEventWindow
	}
	time.AfterFunc(window, func() {
		w := &s.appWindows
		w.mu.Lock()
		win := w.open[key]
		if win == nil || win.pending == 0 {
			delete(w.open, key)
			w.mu.Unlock()
			return
		}
		n, collection := win.pending, win.collection
		win.pending = 0
		w.mu.Unlock()
		at := ev
		at.AtUnixMs = time.Now().UnixMilli()
		s.goRoomEvent(ctx, at, roomEventOf{typ: wire.RoomAppDataChanged, subject: c.AppID, causeJob: c.SourceJobID}, wire.RoomAppDataChangedData{AppID: c.AppID, Collection: collection, Changes: n})
		s.closeAppWindowLater(ctx, ev, c, key)
	})
}
