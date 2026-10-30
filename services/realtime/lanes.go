package realtimehttp

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Delivery lanes (docs/prd/2026-10-28-channels-and-threads.md §2.4): a
// person's lanes stream (?lanes=1&open=<channel>,…) receives full events
// only for the channels it has open, room-wide and room-lane events always
// (wire.StreamLane, the one rule), and for every other channel it may read
// a coalesced channel_activity, at most one a second per channel. Opening
// another channel re-scopes the stream (POST .../events/scope) without
// reopening it, so navigation replays nothing.

// activityEvery is the coalescing window of a channel's activity.
const activityEvery = time.Second

// laneStream is one lanes stream: its open channels (changed by a scope
// request, on another goroutine) and its pending activity (the stream's
// goroutine only).
type laneStream struct {
	id, roomID, userID string

	mu   sync.Mutex
	open map[string]bool

	pending map[string]*wire.ChannelActivityPayload
	sentAt  map[string]time.Time
	// counts are the events per lane since the last report (CountLaneEvents).
	counts map[string]int
}

// laneStreams are the lanes streams this replica serves (a room's streams
// live on its owner, where its scope requests are routed too).
type laneStreams struct {
	mu   sync.Mutex
	byID map[string]*laneStream
}

func (l *laneStreams) add(ls *laneStream) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = map[string]*laneStream{}
	}
	l.byID[ls.id] = ls
}

func (l *laneStreams) remove(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byID, id)
}

func (l *laneStreams) get(id string) *laneStream {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byID[id]
}

// openSet parses the open channels (a comma list or a request's list):
// trimmed, de-duplicated, at most wire.MaxStreamOpen.
func openSet(list []string) (map[string]bool, bool) {
	out := map[string]bool{}
	for _, c := range list {
		if c = strings.TrimSpace(c); c != "" {
			if len(c) > 128 {
				return nil, false
			}
			out[c] = true
		}
	}
	return out, len(out) <= wire.MaxStreamOpen
}

func newLaneStream(roomID, userID string, open map[string]bool) *laneStream {
	return &laneStream{id: ids.New("st_"), roomID: roomID, userID: userID, open: open,
		pending: map[string]*wire.ChannelActivityPayload{}, sentAt: map[string]time.Time{}, counts: map[string]int{}}
}

func (ls *laneStream) isOpen(channelID string) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.open[channelID]
}

func (ls *laneStream) openList() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]string, 0, len(ls.open))
	for c := range ls.open {
		out = append(out, c)
	}
	return out
}

func (ls *laneStream) setOpen(open map[string]bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.open = open
}

// opened is the stream's first event: its id and open channels.
func (ls *laneStream) opened(now time.Time) wire.Event {
	return wire.Event{Kind: wire.EventStreamOpened, RoomID: ls.roomID, AtUnixMs: now.UnixMilli(),
		Payload: wire.StreamOpenedPayload{StreamID: ls.id, Open: ls.openList()}}
}

// note adds ev to its channel's activity. It returns the channel's activity
// event when it is due now (none sent within the window), else keeps it
// pending for flush.
func (ls *laneStream) note(ev wire.Event, now time.Time) (wire.Event, bool) {
	a := ls.pending[ev.ChannelID]
	if a == nil {
		a = &wire.ChannelActivityPayload{ChannelID: ev.ChannelID}
		ls.pending[ev.ChannelID] = a
	}
	a.Events++
	if ev.Sequence > a.LastSeq {
		a.LastSeq = ev.Sequence
	}
	if ev.Sequence > 0 && wire.PersistedFeedKind(ev.Kind) {
		a.Messages++
	}
	if ev.Kind == wire.KindChatMessage && mentionsSubscriber(ev, ls.userID) {
		a.Mention = true
	}
	if now.Sub(ls.sentAt[ev.ChannelID]) < activityEvery {
		return wire.Event{}, false
	}
	return ls.take(ev.ChannelID, now), true
}

// flush returns the pending activity whose window has passed.
func (ls *laneStream) flush(now time.Time) []wire.Event {
	var out []wire.Event
	for ch := range ls.pending {
		if now.Sub(ls.sentAt[ch]) >= activityEvery {
			out = append(out, ls.take(ch, now))
		}
	}
	return out
}

func (ls *laneStream) take(channelID string, now time.Time) wire.Event {
	ls.counts["activity_sent"]++
	a := ls.pending[channelID]
	delete(ls.pending, channelID)
	ls.sentAt[channelID] = now
	return wire.Event{Kind: wire.EventChannelActivity, RoomID: ls.roomID, ChannelID: channelID, AtUnixMs: now.UnixMilli(), Payload: *a}
}

// lane decides ev's lane on this stream and counts it.
func (ls *laneStream) lane(ev wire.Event) string {
	l := wire.StreamLane(ev.Kind, ev.ChannelID, ls.isOpen)
	ls.counts[l]++
	return l
}

// report hands the counts since the last report to the metrics.
func (ls *laneStream) report(m *obs.Metrics) {
	realtime.CountLaneEvents(m, ls.counts)
	clear(ls.counts)
}

// forgetOpen drops the pending activity of channels now open: their full
// events flow, and the client reads their history on opening.
func (ls *laneStream) forgetOpen() {
	for ch := range ls.pending {
		if ls.isOpen(ch) {
			delete(ls.pending, ch)
		}
	}
}

// mentionsSubscriber reports whether a person's message mentions userID by
// id, @channel or @here (the tokens notify reads, MentionCandidates).
func mentionsSubscriber(ev wire.Event, userID string) bool {
	author, text, ok := wire.ChatAuthorText(ev.Payload)
	if !ok || !strings.Contains(text, "@") || author == userID {
		return false
	}
	for _, c := range notify.MentionCandidates(text) {
		if c == userID || c == notify.ReachChannel || c == notify.ReachHere {
			return true
		}
	}
	return false
}

// handleStreamScope re-scopes the caller's lanes stream: the channels it has
// open from now on. The stream is the caller's own in this room; read access
// is asked of every event it delivers, so naming a channel grants nothing.
func (s *Service) handleStreamScope(w http.ResponseWriter, r *http.Request) {
	roomID, userID, ok := s.roomCaller(w, r)
	if !ok {
		return
	}
	var body wire.StreamScopeRequest
	if !httpx.DecodeJSON(w, r, 4<<10, &body) {
		return
	}
	open, ok := openSet(body.Open)
	if !ok {
		httpx.Error(w, "open: at most 4 channel ids", http.StatusBadRequest)
		return
	}
	ls := s.lanes.get(body.StreamID)
	if ls == nil || ls.roomID != roomID || ls.userID != userID {
		// Gone (closed, or served by another replica after an ownership
		// move): the client's stream reconnects with its open channels.
		httpx.Error(w, "no such stream", http.StatusNotFound)
		return
	}
	ls.setOpen(open)
	w.WriteHeader(http.StatusNoContent)
}
