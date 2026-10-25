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
// 30s, with 50-100% jitter, reset by a successful open. Variables only so
// tests can shorten them.
var (
	reconnectBase = time.Second
	reconnectMax  = 30 * time.Second
)

// stream is one member's room SSE subscription, held for the whole run and
// reopened with the web client's backoff when it drops.
type stream struct {
	id      int
	room    *room
	member  *member
	api     *api
	tracker *tracker
	stats   *streamStats
	// idle ends a connection that delivered no bytes (not even the server's
	// 15s keep-alive comment) for this long, as a browser or proxy would.
	idle time.Duration
	// tz is the time zone the member's client reports ("" = none).
	tz string

	live   atomic.Bool
	opened atomic.Bool
}

// streamStats aggregates every stream's counters.
type streamStats struct {
	mu            sync.Mutex
	opened        int
	disconnects   int
	reconnects    int
	outOfOrder    int
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.api.eventsURL(st.member, st.room.id, st.room.channel, st.tz), nil)
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
		return false, &statusError{method: http.MethodGet, path: "/v1/rooms/{id}/events", status: resp.StatusCode, body: string(body)}
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
	rd := sse.NewReader(resp.Body)
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
		ev, ok := decodeEvent(raw.Name, raw.Data)
		if !ok {
			continue
		}
		if ev.Sequence > 0 {
			if ev.Sequence < lastSeq {
				st.stats.add(func(s *streamStats) { s.outOfOrder++ })
			}
			lastSeq = max(lastSeq, ev.Sequence)
		}
		if ev.clientMsgID != "" {
			st.tracker.deliver(st.id, ev.clientMsgID, at)
		}
	}
}

// roomEvent is the part of a realtime event (internal/wire.Event: kind,
// sequence, payload) the load test reads; clientMsgID is set for
// chat_message only.
type roomEvent struct {
	Sequence    int64 `json:"sequence"`
	clientMsgID string
}

// decodeEvent decodes one SSE data payload. It reports false for data that is
// not a room event.
func decodeEvent(kind string, data []byte) (roomEvent, bool) {
	var raw struct {
		Sequence int64  `json:"sequence"`
		Kind     string `json:"kind"`
		Payload  struct {
			ClientMsgID string `json:"clientMsgId"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return roomEvent{}, false
	}
	if raw.Kind == "" {
		raw.Kind = kind
	}
	ev := roomEvent{Sequence: raw.Sequence}
	if raw.Kind == wire.KindChatMessage {
		ev.clientMsgID = raw.Payload.ClientMsgID
	}
	return ev, true
}
