package main

import (
	"sync"
	"time"
)

// tracker matches chat_message arrivals on the room streams to the sends
// that produced them, by clientMsgId. A send registers the streams of its
// room that are open at send start (begin) BEFORE the POST goes out, because
// the echo can arrive before the POST answers. Each expected stream's first
// arrival is one delivery sample; any other matching arrival (a stream that
// opened after the send and got the message from the replay buffer, or a
// repeat after a reconnect) counts as extra and is not a latency sample.
type tracker struct {
	mu        sync.Mutex
	msgs      map[string]*sentMsg
	latencies []time.Duration
	extra     int
	// expected/delivered cover committed (successfully sent) messages only.
	expected  int
	delivered int
}

// sentMsg is one send's delivery state. expected/got are released once
// every expected stream has the message; the entry stays as a tombstone so a
// later repeat is counted as extra rather than as an unknown id.
type sentMsg struct {
	start     time.Time
	committed bool
	expected  map[int]bool
	got       map[int]bool
	// early holds latencies of arrivals that beat the POST answer; they
	// become samples only if the send commits.
	early []time.Duration
	done  bool
}

func newTracker() *tracker {
	return &tracker{msgs: map[string]*sentMsg{}}
}

// begin registers a send about to go out at start, expected on streams.
func (t *tracker) begin(id string, start time.Time, streams []int) {
	exp := make(map[int]bool, len(streams))
	for _, s := range streams {
		exp[s] = true
	}
	t.mu.Lock()
	t.msgs[id] = &sentMsg{start: start, expected: exp, got: make(map[int]bool, len(streams))}
	t.mu.Unlock()
}

// commit marks a send as accepted by the server (POST 200).
func (t *tracker) commit(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.msgs[id]
	if m == nil || m.committed {
		return
	}
	m.committed = true
	t.expected += len(m.expected)
	t.delivered += len(m.early)
	t.latencies = append(t.latencies, m.early...)
	m.early = nil
	t.finishIfComplete(m)
}

// abort forgets a send the server refused or that failed in transport.
// Arrivals for it (a send whose answer was lost but that the server did
// commit) then count as extra.
func (t *tracker) abort(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := t.msgs[id]; m != nil && !m.committed {
		m.done = true
		m.expected, m.got, m.early = nil, nil, nil
	}
}

// deliver records the arrival of message id on stream at time at. Unknown
// ids (another client's messages, an earlier run's replay) are ignored.
func (t *tracker) deliver(stream int, id string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.msgs[id]
	if m == nil {
		return
	}
	if m.done || !m.expected[stream] || m.got[stream] {
		t.extra++
		return
	}
	m.got[stream] = true
	lat := at.Sub(m.start)
	if !m.committed {
		m.early = append(m.early, lat)
		return
	}
	t.delivered++
	t.latencies = append(t.latencies, lat)
	t.finishIfComplete(m)
}

func (t *tracker) finishIfComplete(m *sentMsg) {
	if m.committed && len(m.got) == len(m.expected) {
		m.done = true
		m.expected, m.got = nil, nil
	}
}

// trackerResult is the tracker's final tally.
type trackerResult struct {
	expected, delivered, extra int
	latencies                  []time.Duration
}

// result returns the tally; the latency slice is a copy.
func (t *tracker) result() trackerResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return trackerResult{
		expected:  t.expected,
		delivered: t.delivered,
		extra:     t.extra,
		latencies: append([]time.Duration(nil), t.latencies...),
	}
}

// outstanding reports how many committed deliveries are still missing, so
// the drain wait can end early once everything has arrived.
func (t *tracker) outstanding() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expected - t.delivered
}
