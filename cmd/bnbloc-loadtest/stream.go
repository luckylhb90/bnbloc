package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Reconnect backoff of the web client's subscribeRoom: 1s doubling up to
// 8s, with 50-100% jitter, reset by a successful open. Variables only so
// tests can shorten them.
var (
	reconnectBase = time.Second
	reconnectMax  = 8 * time.Second
)

// stream is one web tab: a member's member stream (GET /v1/my/events),
// scoped to the member's rooms (their unread) and showing one room and
// channel, held for the whole run and reopened with the web client's backoff
// when it drops.
type stream struct {
	id      int
	room    *room
	member  *member
	api     *api
	tracker *tracker
	stats   *streamStats
	// rooms are the member's rooms, whose unread the stream carries.
	rooms  []string
	unread *activityStats
	// channel is the channel the stream has open (a lanes stream: full
	// events there, channel_activity for the room's others).
	channel string
	// activityFrom are the channels this stream received activity for.
	activityMu   sync.Mutex
	activityFrom map[string]bool
	// idle ends a connection that delivered no bytes (not even the server's
	// 15s keep-alive comment) for this long, as a browser or proxy would.
	idle time.Duration
	// tz is the time zone the member's client reports ("" = none).
	tz string

	live   atomic.Bool
	opened atomic.Bool
	// resumeSeq is the last sequence the stream received: a reconnect asks
	// for what is newer (the web client's since).
	resumeSeq atomic.Int64
}

// streamStats aggregates every stream's counters.
type streamStats struct {
	mu            sync.Mutex
	opened        int
	disconnects   int
	reconnects    int
	outOfOrder    int
	activity      int
	gaps          int
	connectErrors map[string]int
	connect       []time.Duration
}

func newStreamStats() *streamStats {
	return &streamStats{connectErrors: map[string]int{}}
}

func (s *streamStats) add(f func(s *streamStats)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

// run holds the stream until ctx ends.
func (st *stream) run(ctx context.Context) {
	attempts := 0
	for ctx.Err() == nil {
		opened, err := st.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if opened {
			attempts = 0
			st.stats.add(func(s *streamStats) { s.disconnects++ })
		} else if err != nil {
			key := errKey(err)
			st.stats.add(func(s *streamStats) { s.connectErrors[key]++ })
		}
		delay := min(reconnectMax, reconnectBase<<min(attempts, 5))
		delay = time.Duration(float64(delay) * (0.5 + rand.Float64()*0.5))
		attempts++
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connectOnce opens the stream and reads it until it ends. opened reports
// whether the server answered 200 (so the end is a disconnect, not a failed
// open).
func (st *stream) connectOnce(ctx context.Context) (opened bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.api.memberEventsURL(st.member, st.tz), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := st.api.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, &statusError{method: http.MethodGet, path: "/v1/my/events", status: resp.StatusCode, body: string(body)}
	}
	// The stream opens with its id; the tab then scopes it, as the web
	// client does: its rooms, and the room and channel it shows.
	rd := sse.NewReader(resp.Body)
	first, err := rd.Next()
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	var hello struct {
		Payload wire.StreamOpenedPayload `json:"payload"`
	}
	if first.Name != wire.EventStreamOpened || json.Unmarshal(first.Data, &hello) != nil || hello.Payload.StreamID == "" {
		return false, fmt.Errorf("the stream did not open with %s", wire.EventStreamOpened)
	}
	if err := st.api.scopeStream(ctx, st.member, hello.Payload.StreamID, st.rooms, st.room.id, st.channel, st.resumeSeq.Load()); err != nil {
		return false, err
	}
	reopen := st.opened.Swap(true)
	st.live.Store(true)
	defer st.live.Store(false)
	took := time.Since(start)
	st.stats.add(func(s *streamStats) {
		s.opened++
		if reopen {
			s.reconnects++
		}
		s.connect = append(s.connect, took)
	})

	// Idle watchdog: any line read (data or comment) re-arms it.
	var watchdog *time.Timer
	if st.idle > 0 {
		watchdog = time.AfterFunc(st.idle, cancel)
		defer watchdog.Stop()
	}
	lastSeq := int64(-1)
	if watchdog != nil {
		rd.OnLine = func() { watchdog.Reset(st.idle) }
	}
	for {
		raw, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return true, errors.New("stream closed by the server")
		}
		if err != nil {
			return true, fmt.Errorf("read: %w", err)
		}
		at := time.Now()
		if st.unread.note(st.id, raw.Name, raw.Data) {
			continue
		}
		ev, ok := decodeEvent(raw.Name, raw.Data)
		if !ok || (ev.roomID != "" && ev.roomID != st.room.id) {
			continue
		}
		if ev.Sequence > 0 {
			if ev.Sequence < lastSeq {
				st.stats.add(func(s *streamStats) { s.outOfOrder++ })
			}
			lastSeq = max(lastSeq, ev.Sequence)
			if ev.Sequence > st.resumeSeq.Load() {
				st.resumeSeq.Store(ev.Sequence)
			}
		}
		if ev.gap {
			// The stream lacks events: read the difference from history,
			// as the web client does, and count what it recovers.
			st.stats.add(func(s *streamStats) { s.gaps++ })
			go st.fillGap()
		}
		if ev.clientMsgID != "" {
			st.tracker.deliver(st.id, ev.clientMsgID, at)
		}
		if ev.activityFrom != "" {
			st.activityMu.Lock()
			if st.activityFrom == nil {
				st.activityFrom = map[string]bool{}
			}
			st.activityFrom[ev.activityFrom] = true
			st.activityMu.Unlock()
			st.stats.add(func(s *streamStats) { s.activity++ })
		}
	}
}

// roomEvent is the part of a realtime event (internal/wire.Event: kind,
// sequence, payload) the load test reads; clientMsgID is set for
// chat_message only.
type roomEvent struct {
	Sequence    int64 `json:"sequence"`
	roomID      string
	clientMsgID string
	// activityFrom is the channel of a channel_activity event.
	activityFrom string
	// gap: a stream_gap event.
	gap bool
}

// decodeEvent decodes one SSE data payload. It reports false for data that is
// not a room event.
func decodeEvent(kind string, data []byte) (roomEvent, bool) {
	var raw struct {
		Sequence  int64  `json:"sequence"`
		RoomID    string `json:"roomId"`
		Kind      string `json:"kind"`
		ChannelID string `json:"channelId"`
		Payload   struct {
			ClientMsgID string `json:"clientMsgId"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return roomEvent{}, false
	}
	if raw.Kind == "" {
		raw.Kind = kind
	}
	ev := roomEvent{Sequence: raw.Sequence, roomID: raw.RoomID}
	switch raw.Kind {
	case wire.KindChatMessage:
		ev.clientMsgID = raw.Payload.ClientMsgID
	case wire.EventChannelActivity:
		ev.activityFrom = raw.ChannelID
	case wire.EventStreamGap:
		ev.gap = true
	}
	return ev, true
}

// activityReport says whether the lanes streams' activity reached them: a
// stream should have heard of every other channel of its room that had
// messages (each channel had, with a member posting in it).
func activityReport(streams []*stream, ss *streamStats) ActivityReport {
	r := ActivityReport{Events: ss.activity}
	for _, st := range streams {
		others := 0
		for _, ch := range st.room.channels {
			if ch != st.channel && st.room.hasPoster(ch) {
				others++
			}
		}
		if others == 0 {
			continue
		}
		r.Streams++
		st.activityMu.Lock()
		heard := 0
		for ch := range st.activityFrom {
			if ch != st.channel {
				heard++
			}
		}
		st.activityMu.Unlock()
		if heard >= others {
			r.Complete++
		}
	}
	return r
}

// fillGap reads the channel's latest history after a stream_gap and counts
// each message found there as delivered to this stream (a recovered one,
// not a lost one).
func (st *stream) fillGap() {
	for i, wait := range gapReads {
		if i > 0 {
			time.Sleep(wait)
		}
		ids, err := st.api.historyClientIDs(context.Background(), st.member, st.room.id, st.channel)
		if err != nil {
			continue
		}
		at := time.Now()
		for _, id := range ids {
			st.tracker.deliver(st.id, id, at)
		}
	}
}

// gapReads are when the web client reads a gap's difference: at once, and
// again once a room's owners agree (web/src/lib/useRoomStream.ts
// GAP_SETTLE_MS).
var gapReads = []time.Duration{0, 3 * time.Second}
