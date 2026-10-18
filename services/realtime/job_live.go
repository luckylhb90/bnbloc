package realtimehttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Job live views (wire/job_live.go, docs/prd/2026-10-17-browser-component.md
// §3.2-§3.3). Realtime holds each live job's newest frame, its queued input
// and who controls it, in memory on the room's owner (the routes are
// room-keyed, so every replica forwards them here): a frame is worth
// nothing after the next one, and nothing of it is kept past the turn.
//
//   - POST /internal/v1/rooms/{roomId}/live/{jobId} (the gateway, after the
//     job grant): store the frame, tell the room (job_live_frame, no image),
//     answer whether anyone watches, who controls, and the queued input.
//   - GET  /v1/rooms/{roomId}/jobs/{jobId}/live/frame: the newest image, for
//     any member of the room. ?watch=1 marks the caller as watching the large
//     view for liveWatchFor.
//   - POST /v1/rooms/{roomId}/jobs/{jobId}/live/control: take or hand back
//     control (the job's owner only, until control grants exist).
//   - POST /v1/rooms/{roomId}/jobs/{jobId}/live/input: the controller's
//     input, queued for the tool's next post.

const (
	// liveWatchFor is how long a large-view fetch counts as watching.
	liveWatchFor = 5 * time.Second
	// liveIdleFor drops a job's state when its tool has not posted for
	// this long (the turn ended, the machine went away).
	liveIdleFor = 2 * time.Minute
)

// liveJob is one job's live state.
type liveJob struct {
	tenant     string
	channel    string
	owner      string
	requester  string
	frame      wire.LiveFramePost
	atMs       int64
	posted     time.Time
	watchedTo  time.Time
	controller string
	controlTo  time.Time
	input      []wire.LiveInput
}

// liveStore holds every live job of the rooms this replica owns.
type liveStore struct {
	mu   sync.Mutex
	jobs map[string]*liveJob // room + "\x00" + job
}

func liveKey(roomID, jobID string) string { return roomID + "\x00" + jobID }

// get answers a job's state, dropping it when idle.
func (l *liveStore) getLocked(roomID, jobID string, now time.Time) *liveJob {
	j := l.jobs[liveKey(roomID, jobID)]
	if j != nil && now.Sub(j.posted) > liveIdleFor {
		delete(l.jobs, liveKey(roomID, jobID))
		return nil
	}
	return j
}

// controllerLocked is who holds control now ("" when it lapsed).
func (j *liveJob) controllerLocked(now time.Time) string {
	if j.controller != "" && now.After(j.controlTo) {
		j.controller = ""
	}
	return j.controller
}

// handleInternalLiveFrame stores a job's frame (the gateway has checked the
// job grant) and answers the tool.
func (s *Service) handleInternalLiveFrame(w http.ResponseWriter, r *http.Request) {
	roomID, _, ok := s.roomCaller(w, r)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	var in wire.LiveFrameIngest
	if !httpx.DecodeJSON(w, r, maxInternalPublishBytes+wire.MaxLiveFrameBytes, &in) {
		return
	}
	if jobID == "" || in.Owner == "" {
		httpx.Error(w, "jobId and owner required", http.StatusBadRequest)
		return
	}
	f := in.Frame
	if len(f.Data) > wire.MaxLiveFrameBytes || f.Width < 0 || f.Height < 0 || f.Width > wire.MaxLiveFrameSide || f.Height > wire.MaxLiveFrameSide {
		httpx.Error(w, "frame too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(f.Data) > 0 && f.MimeType != "image/jpeg" && f.MimeType != "image/png" && f.MimeType != "image/webp" {
		httpx.Error(w, "frame must be a jpeg, png or webp image", http.StatusBadRequest)
		return
	}
	f.Title, f.URL = cutRunes(f.Title, wire.MaxLiveTitle), cutRunes(f.URL, wire.MaxLiveTitle)
	now := s.now()
	l := s.live()
	l.mu.Lock()
	j := l.getLocked(roomID, jobID, now)
	if j == nil {
		j = &liveJob{}
		l.jobs[liveKey(roomID, jobID)] = j
	}
	j.tenant, j.channel, j.owner, j.requester, j.posted = tenant.FromContext(r.Context()), in.ChannelID, in.Owner, in.Requester, now
	fresh := len(f.Data) > 0
	if fresh {
		j.frame, j.atMs = f, now.UnixMilli()
	}
	reply := wire.LiveFrameReply{Watching: now.Before(j.watchedTo), Controller: j.controllerLocked(now), Input: j.input}
	j.input = nil
	event := wire.JobLiveFrame{JobID: jobID, Run: j.frame.Run, Seq: j.frame.Seq, Width: j.frame.Width, Height: j.frame.Height,
		Title: j.frame.Title, URL: j.frame.URL, Owner: j.owner, Controller: reply.Controller, AtMs: j.atMs}
	l.mu.Unlock()
	if fresh {
		s.publishLive(r.Context(), roomID, j.channel, event)
	}
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// publishLive tells the room a job's live view changed (no image;
// transient, so nothing is stored).
func (s *Service) publishLive(ctx context.Context, roomID, channelID string, ev wire.JobLiveFrame) {
	_, _ = s.publish(ctx, wire.Event{RoomID: roomID, ChannelID: channelID, Kind: wire.EventJobLiveFrame, Payload: ev, AtUnixMs: s.now().UnixMilli()}, realtime.Durable{})
}

// handleLiveFrame answers a job's newest image to a member of the room.
func (s *Service) handleLiveFrame(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.room(w, r, membership.Human)
	if !ok {
		return
	}
	now := s.now()
	l := s.live()
	l.mu.Lock()
	j := l.getLocked(roomID, r.PathValue("jobId"), now)
	if j == nil || j.tenant != tenant.FromContext(r.Context()) || len(j.frame.Data) == 0 {
		l.mu.Unlock()
		httpx.Error(w, "no live view for this job", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("watch") == "1" {
		j.watchedTo = now.Add(liveWatchFor)
	}
	f := j.frame
	// The frame's facts ride along (wire.JobLiveFrame as JSON): a viewer
	// that opened after the last event (a pop-out, a reload) knows the
	// view at once; the event is never replayed.
	facts, _ := json.Marshal(wire.JobLiveFrame{JobID: r.PathValue("jobId"), Run: f.Run, Seq: f.Seq, Width: f.Width, Height: f.Height,
		Title: f.Title, URL: f.URL, Owner: j.owner, Controller: j.controllerLocked(now), AtMs: j.atMs})
	l.mu.Unlock()
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(wire.LiveFrameHeader, base64.RawURLEncoding.EncodeToString(facts))
	_, _ = w.Write(f.Data)
}

// handleLiveControl takes or hands back control of a job's live view.
func (s *Service) handleLiveControl(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.Human)
	if !ok {
		return
	}
	var in wire.LiveControlPost
	if !httpx.DecodeJSON(w, r, 1<<10, &in) {
		return
	}
	now := s.now()
	l := s.live()
	l.mu.Lock()
	j := l.getLocked(roomID, r.PathValue("jobId"), now)
	if j == nil || j.tenant != tenant.FromContext(r.Context()) {
		l.mu.Unlock()
		httpx.Error(w, "no live view for this job", http.StatusNotFound)
		return
	}
	if !mayControl(j, userID) {
		l.mu.Unlock()
		httpx.Error(w, "only "+j.owner+", whose machine this runs on, can take control", http.StatusForbidden)
		return
	}
	switch cur := j.controllerLocked(now); {
	case in.Take && cur != "" && cur != userID:
		l.mu.Unlock()
		httpx.Error(w, cur+" has control", http.StatusConflict)
		return
	case in.Take:
		j.controller, j.controlTo = userID, now.Add(wire.LiveControlLapseMs*time.Millisecond)
	case cur == userID:
		j.controller = ""
	}
	event := wire.JobLiveFrame{JobID: r.PathValue("jobId"), Run: j.frame.Run, Seq: j.frame.Seq, Width: j.frame.Width, Height: j.frame.Height,
		Title: j.frame.Title, URL: j.frame.URL, Owner: j.owner, Controller: j.controller, AtMs: j.atMs}
	channel := j.channel
	l.mu.Unlock()
	s.publishLive(r.Context(), roomID, channel, event)
	httpx.WriteJSON(w, http.StatusOK, wire.LiveControlReply{Controller: event.Controller})
}

// handleLiveInput queues the controller's input for the tool.
func (s *Service) handleLiveInput(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.Human)
	if !ok {
		return
	}
	var in wire.LiveInputPost
	if !httpx.DecodeJSON(w, r, 64<<10, &in) {
		return
	}
	events := liveInput(in.Events)
	now := s.now()
	l := s.live()
	l.mu.Lock()
	defer l.mu.Unlock()
	j := l.getLocked(roomID, r.PathValue("jobId"), now)
	if j == nil || j.tenant != tenant.FromContext(r.Context()) {
		httpx.Error(w, "no live view for this job", http.StatusNotFound)
		return
	}
	// Bringing the window forward is the owner's, with or without control
	// (it acts on their own screen); everything else needs control.
	if focusOnly(events) {
		if userID != j.owner {
			httpx.Error(w, "only "+j.owner+" can bring the window forward on their machine", http.StatusForbidden)
			return
		}
	} else if j.controllerLocked(now) != userID {
		httpx.Error(w, "take control first", http.StatusConflict)
		return
	}
	j.input = append(j.input, events...)
	if len(j.input) > wire.MaxLiveInput {
		j.input = j.input[len(j.input)-wire.MaxLiveInput:]
	}
	w.WriteHeader(http.StatusNoContent)
}

// mayControl: the job's owner (their machine, their sign-ins). Another
// member needs the owner's control grant (§2.6), which does not exist yet.
func mayControl(j *liveJob, userID string) bool { return userID == j.owner }

func focusOnly(events []wire.LiveInput) bool {
	for _, e := range events {
		if e.Type != wire.LiveInputFocus {
			return false
		}
	}
	return len(events) > 0
}

// liveInput keeps the known kinds, coordinates inside the frame, and short
// text.
func liveInput(in []wire.LiveInput) []wire.LiveInput {
	out := make([]wire.LiveInput, 0, len(in))
	for _, e := range in {
		switch e.Type {
		case wire.LiveInputMouse, wire.LiveInputWheel, wire.LiveInputKey, wire.LiveInputText, wire.LiveInputFocus:
		default:
			continue
		}
		if e.X < 0 || e.X > 1 || e.Y < 0 || e.Y > 1 {
			continue
		}
		e.Text, e.Key, e.Code = cutRunes(e.Text, 1000), cutRunes(e.Key, 32), cutRunes(e.Code, 32)
		out = append(out, e)
		if len(out) == wire.MaxLiveInput {
			break
		}
	}
	return out
}

func cutRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// live is the service's live store, made on first use.
func (s *Service) live() *liveStore {
	s.liveOnce.Do(func() { s.liveJobs = &liveStore{jobs: map[string]*liveJob{}} })
	return s.liveJobs
}
