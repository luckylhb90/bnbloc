package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// memberActivity is one member's activity stream (GET /v1/my/activity), as
// the web shell holds one per tab: the unread state of the member's rooms,
// then what changes. The run counts the new messages each one is told of.
type memberActivity struct {
	member *member
	rooms  []*room
	api    *api
	stats  *activityStats
	idle   time.Duration
	live   atomic.Bool
}

// activityLastFlush outlasts the server's delta interval (one second), so
// the report counts the last delta.
const activityLastFlush = 1500 * time.Millisecond

// activityStats aggregates every activity stream.
type activityStats struct {
	mu          sync.Mutex
	opened      int
	disconnects int
	states      int
	deltas      int
	// seen are the (member, room, seq) a delta named.
	seen map[string]bool
}

func newActivityStats() *activityStats { return &activityStats{seen: map[string]bool{}} }

// activitiesFor gives each member one stream across the rooms they are in.
func activitiesFor(rooms []*room, a *api, stats *activityStats, idle time.Duration) []*memberActivity {
	byMember := map[*member]*memberActivity{}
	var out []*memberActivity
	for _, r := range rooms {
		for _, m := range r.members {
			ma := byMember[m]
			if ma == nil {
				ma = &memberActivity{member: m, api: a, stats: stats, idle: idle}
				byMember[m] = ma
				out = append(out, ma)
			}
			ma.rooms = append(ma.rooms, r)
		}
	}
	return out
}

// run holds the stream until ctx ends, reopening it with the web client's
// backoff.
func (ma *memberActivity) run(ctx context.Context) {
	attempts := 0
	for ctx.Err() == nil {
		if opened, _ := ma.connectOnce(ctx); opened {
			attempts = 0
			ma.stats.add(func(s *activityStats) { s.disconnects++ })
		}
		if ctx.Err() != nil {
			return
		}
		delay := min(reconnectMax, reconnectBase<<min(attempts, 5))
		attempts++
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(float64(delay) * (0.5 + rand.Float64()*0.5))):
		}
	}
}

func (s *activityStats) add(f func(s *activityStats)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

func (ma *memberActivity) connectOnce(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ids := make([]string, len(ma.rooms))
	for i, r := range ma.rooms {
		ids[i] = r.id
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ma.api.base+"/v1/my/activity?rooms="+url.QueryEscape(strings.Join(ids, ","))+"&access_token="+url.QueryEscape(ma.member.token), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := ma.api.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, &statusError{method: http.MethodGet, path: "/v1/my/activity", status: resp.StatusCode}
	}
	ma.live.Store(true)
	defer ma.live.Store(false)
	ma.stats.add(func(s *activityStats) { s.opened++ })
	var watchdog *time.Timer
	if ma.idle > 0 {
		watchdog = time.AfterFunc(ma.idle, cancel)
		defer watchdog.Stop()
	}
	rd := sse.NewReader(resp.Body)
	if watchdog != nil {
		rd.OnLine = func() { watchdog.Reset(ma.idle) }
	}
	for {
		raw, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return true, errors.New("stream closed by the server")
		}
		if err != nil {
			return true, fmt.Errorf("read: %w", err)
		}
		switch raw.Name {
		case wire.UnreadStateKind:
			ma.stats.add(func(s *activityStats) { s.states++ })
		case wire.UnreadDeltaKind:
			var ev struct {
				Payload wire.UnreadDeltaPayload `json:"payload"`
			}
			if json.Unmarshal(raw.Data, &ev) != nil {
				continue
			}
			ma.stats.add(func(s *activityStats) {
				s.deltas++
				for room, d := range ev.Payload.Rooms {
					for _, seq := range d.Seqs {
						s.seen[fmt.Sprintf("%s\x00%s\x00%d", ma.member.subject, room, seq)] = true
					}
				}
			})
		}
	}
}

// ActivityStreamReport covers the members' activity streams (--member-activity).
type ActivityStreamReport struct {
	Streams     int `json:"streams"`
	OpenAtEnd   int `json:"openAtEnd"`
	Opened      int `json:"opened"`
	Disconnects int `json:"disconnects"`
	States      int `json:"states"`
	Deltas      int `json:"deltas"`
	// Told counts the new messages the streams were told of, one per
	// member and message; Expected is every stored send times its room's
	// members (each member is told of every message of their rooms,
	// theirs included). A stream that reopened is sent the state instead,
	// so Told falls short of Expected only by what reopening covered.
	Told     int `json:"told"`
	Expected int `json:"expected"`
}

func memberActivityReport(streams []*memberActivity, stats *activityStats, rooms []*room) ActivityStreamReport {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	rep := ActivityStreamReport{Streams: len(streams), Opened: stats.opened, Disconnects: stats.disconnects,
		States: stats.states, Deltas: stats.deltas, Told: len(stats.seen)}
	for _, s := range streams {
		if s.live.Load() {
			rep.OpenAtEnd++
		}
	}
	for _, r := range rooms {
		rep.Expected += int(r.stored.Load()) * len(r.members)
	}
	return rep
}
