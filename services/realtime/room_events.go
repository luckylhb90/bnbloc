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
		s.goRoomEvent(ctx, ev, wire.RoomFileAdded, subject, p.SourceJobID, wire.RoomFileAddedData{
			ArtifactID: p.ArtifactID, Kind: p.Kind, Title: p.Title, FileName: p.FileName, MimeType: p.MimeType,
			Version: p.CurrentVersion, By: p.UpdatedBy,
		})
	case wire.EventMemberJoined:
		if member := joinedMember(ev); member != "" {
			s.goRoomEvent(ctx, ev, wire.RoomMemberJoined, member, "", wire.RoomMemberJoinedData{UserID: member})
		}
	case wire.EventAppChanged:
		if c, ok := wire.DecodePayload[wire.AppChanged](ev.Payload); ok && c.AppID != "" {
			s.appChanged(ctx, ev, c)
		}
	}
}

// goRoomEvent dispatches one room event in the background, under the
// publish's tenant.
func (s *Service) goRoomEvent(ctx context.Context, ev wire.Event, typ, subject, causeJob string, data any) {
	tid := tenant.FromContext(ctx)
	if tid == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.WithoutCancel(ctx), tid), roomEventTimeout)
		defer cancel()
		s.dispatchRoomEvent(ctx, tid, ev.RoomID, typ, subject, causeJob, data, time.UnixMilli(serverTimeIfUnset(ev.AtUnixMs)))
	}()
}

// dispatchRoomEvent builds the room's event and dispatches it to the
// room's own tasks.
func (s *Service) dispatchRoomEvent(ctx context.Context, tid, roomID, typ, subject, causeJob string, data any, at time.Time) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	ce, err := wire.ValidEvent(wire.CloudEvent{ID: ids.New("evt_"), Source: wire.SourceRoomPrefix + roomID, Type: typ,
		Time: wire.EventTime(at), Subject: subject, Data: raw})
	if err != nil {
		slog.Warn("bnbloc realtime: room event invalid", "room", roomID, "type", typ, "error", err)
		return
	}
	d := eventDelivery{event: ce, reach: store.EventScheduleQuery{Tenants: []string{tid}, RoomID: roomID}, causeRoom: s.causeRoom(ctx, roomID, causeJob)}
	if _, _, err := s.dispatchEvent(ctx, d, time.Now()); err != nil {
		slog.Warn("bnbloc realtime: room event dispatch failed", "event", "bnbloc.schedule.room_event_failed", "room", roomID, "type", typ, "error", err)
	}
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
	s.goRoomEvent(ctx, ev, wire.RoomAppDataChanged, c.AppID, c.SourceJobID, wire.RoomAppDataChangedData{AppID: c.AppID, Collection: c.Collection, Changes: 1})
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
		s.goRoomEvent(ctx, at, wire.RoomAppDataChanged, c.AppID, c.SourceJobID, wire.RoomAppDataChangedData{AppID: c.AppID, Collection: collection, Changes: n})
		s.closeAppWindowLater(ctx, ev, c, key)
	})
}
