package pulsesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// KAnonymity is the smallest number of people a displayed people count may
// stand for (docs/prd/2026-10-24-executive-pulse.md, section 7). Below it the
// count is withheld and the response says so.
const KAnonymity = 5

// settleWindow is how far back the rollup may still change (late events land in
// their finished minute): figures inside it are marked settling.
const settleWindow = 5 * time.Minute

// SnapshotVersion is the snapshot's contract version.
const SnapshotVersion = 1

// Snapshot is the board's first paint and the base of each refresh.
type Snapshot struct {
	V               int      `json:"v"`
	AtMs            int64    `json:"atMs"`
	SettlingSinceMs int64    `json:"settlingSinceMs"`
	DataAsOfMs      int64    `json:"dataAsOfMs"` // the rollup's last write; 0 = never
	Today           Today    `json:"today"`
	Live            LiveView `json:"live"`
	// Geo is people online per country, only for countries with at least k people.
	Geo  Geo  `json:"geo"`
	Week Week `json:"week"`
}

// CoreReplies is one agent family's replies for the day: the lane of the token
// pump, with its coverage (reported / replies).
type CoreReplies struct {
	Replies  float64 `json:"replies"`
	Reported float64 `json:"reported"`
}

// Today is the UTC day so far, summed from the rollup.
type Today struct {
	Replies          float64                `json:"replies"`
	RepliesReported  float64                `json:"repliesReported"`
	Coverage         *float64               `json:"coverage"` // reported / replies; null with no replies
	RepliesByCore    map[string]CoreReplies `json:"repliesByCore"`
	TokensIn         float64                `json:"tokensIn"`
	TokensOut        float64                `json:"tokensOut"`
	CostUsd          float64                `json:"costUsd"`
	JobsClaimed      float64                `json:"jobsClaimed"`
	JobsFinished     float64                `json:"jobsFinished"`
	Messages         float64                `json:"messages"`
	ArtifactVersions float64                `json:"artifactVersions"`
	AppWrites        float64                `json:"appWrites"`
}

// LiveView is the state right now.
type LiveView struct {
	AgentsWorking int64 `json:"agentsWorking"`
	AgentsWaiting int64 `json:"agentsWaiting"`
	// OldestWaitingSec is how long the oldest queued job has waited; null when none waits.
	OldestWaitingSec *int64 `json:"oldestWaitingSec"`
	PeopleOnline     *int64 `json:"peopleOnline"` // null when suppressed
	PeopleSuppressed bool   `json:"peopleSuppressed"`
}

// Geo is the country view of presence. Countries under k are not listed; the
// count of them is reported so the board can say some were withheld. The board
// labels whether these are real (the organization's switch is on) or none.
type Geo struct {
	Countries         []GeoCountry `json:"countries"`
	CountriesWithheld int          `json:"countriesWithheld"`
}

// GeoCountry is one country with at least k people online.
type GeoCountry struct {
	Code   string  `json:"code"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	People int64   `json:"people"`
}

// Week is the rolling seven days.
type Week struct {
	ActivePeople           *int64 `json:"activePeople"` // null when suppressed
	ActivePeopleSuppressed bool   `json:"activePeopleSuppressed"`
}

// Server answers the snapshot.
type Server struct {
	State *Live
	Rows  store.PulseMinutes
	Now   func() time.Time
}

// todayMetrics are the rollup metrics the day's totals read. jobs_finished is
// one metric across every core and status; its total sums them all.
var todayMetrics = []string{
	MetricReplies, MetricRepliesReported, MetricTokensIn, MetricTokensOut, MetricCostUsd,
	MetricJobsClaimed, MetricJobsFinished, MetricMessages, MetricArtifactVersions, MetricAppWrites,
}

// streamEvery is how often the stream pushes a snapshot (a variable so tests
// can run it fast; the board refreshes every two seconds).
var streamEvery = 2 * time.Second

// Handler serves the board for the caller's tenant at the same public paths the
// gateway forwards (/v1/tenant/pulse/...): the snapshot (one shot) and the stream
// (a snapshot every two seconds). The gateway's read_org_pulse gate is in front
// of them, and the internal identity middleware sits in front of this handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tenant/pulse/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /v1/tenant/pulse/stream", s.handleStream)
	return mux
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	tnt, err := tenant.Require(r.Context())
	if err != nil {
		httpx.Error(w, "missing tenant", http.StatusBadRequest)
		return
	}
	snap, err := s.buildSnapshot(r.Context(), tnt, s.Now())
	if err != nil {
		s.fail(w, "snapshot read failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snap)
}

// handleStream writes a "snapshot" event now and every streamEvery until the
// client goes away. A failed read ends the stream with an "error" event: the
// board then shows "disconnected", never stale numbers as live.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	tnt, err := tenant.Require(r.Context())
	if err != nil {
		httpx.Error(w, "missing tenant", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		obs.ServerError(w, "streaming unsupported", errors.New("response writer cannot flush"))
		httpx.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	ticker := time.NewTicker(streamEvery)
	defer ticker.Stop()
	for {
		snap, err := s.buildSnapshot(r.Context(), tnt, s.Now())
		if err != nil {
			if r.Context().Err() == nil {
				obs.ServerError(w, "snapshot stream read failed", err)
				fmt.Fprint(w, "event: error\ndata: {}\n\n")
				flusher.Flush()
			}
			return
		}
		data, err := json.Marshal(snap)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// buildSnapshot reads one snapshot for tnt at now: the day's totals from the
// rollup (lag-tolerant, so from the replica), and the live state from Redis.
func (s *Server) buildSnapshot(ctx context.Context, tnt string, now time.Time) (Snapshot, error) {
	dayStart := now.UTC().Truncate(24 * time.Hour)
	ro := store.WithReplicaReads(ctx)
	totals, err := s.Rows.PulseTotals(ro, tnt, dayStart.UnixMilli(), now.UnixMilli()+1, todayMetrics)
	if err != nil {
		return Snapshot{}, err
	}
	byCore, err := s.Rows.PulseBreakdown(ro, tnt, dayStart.UnixMilli(), now.UnixMilli()+1,
		[]string{MetricReplies, MetricRepliesReported})
	if err != nil {
		return Snapshot{}, err
	}
	asOf, err := s.Rows.PulseFreshness(ro, tnt)
	if err != nil {
		return Snapshot{}, err
	}
	online, err := s.State.PeopleOnline(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	working, err := s.State.AgentsWorking(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	waiting, err := s.State.AgentsWaiting(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	oldest, hasOldest, err := s.State.OldestWaiting(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	countries, err := s.State.Countries(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	week, err := s.State.ActivePeopleWeek(ctx, tnt, now)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		V:               SnapshotVersion,
		AtMs:            now.UnixMilli(),
		SettlingSinceMs: now.Add(-settleWindow).UnixMilli(),
		DataAsOfMs:      asOf,
		Today: Today{
			Replies:          totals[MetricReplies],
			RepliesReported:  totals[MetricRepliesReported],
			RepliesByCore:    coreLanes(byCore),
			TokensIn:         totals[MetricTokensIn],
			TokensOut:        totals[MetricTokensOut],
			CostUsd:          totals[MetricCostUsd],
			JobsClaimed:      totals[MetricJobsClaimed],
			JobsFinished:     totals[MetricJobsFinished],
			Messages:         totals[MetricMessages],
			ArtifactVersions: totals[MetricArtifactVersions],
			AppWrites:        totals[MetricAppWrites],
		},
		Live: LiveView{AgentsWorking: working, AgentsWaiting: waiting},
	}
	for _, c := range countries {
		if c.People < KAnonymity {
			snap.Geo.CountriesWithheld++
			continue
		}
		snap.Geo.Countries = append(snap.Geo.Countries, GeoCountry{Code: c.Code, Lat: c.Lat, Lon: c.Lon, People: c.People})
	}
	// An empty list is an empty array on the wire, never null.
	if snap.Geo.Countries == nil {
		snap.Geo.Countries = []GeoCountry{}
	}
	if snap.Today.Replies > 0 {
		c := snap.Today.RepliesReported / snap.Today.Replies
		snap.Today.Coverage = &c
	}
	if hasOldest {
		sec := int64(oldest / time.Second)
		snap.Live.OldestWaitingSec = &sec
	}
	if online < KAnonymity {
		snap.Live.PeopleSuppressed = true
	} else {
		snap.Live.PeopleOnline = &online
	}
	if week < KAnonymity {
		snap.Week.ActivePeopleSuppressed = true
	} else {
		snap.Week.ActivePeople = &week
	}
	return snap, nil
}

// coreLanes turns the replies breakdown (metric -> core -> value) into one lane
// per core. A core with no replies has no lane.
func coreLanes(byCore map[string]map[string]float64) map[string]CoreReplies {
	lanes := map[string]CoreReplies{}
	for core, n := range byCore[MetricReplies] {
		lanes[core] = CoreReplies{Replies: n, Reported: byCore[MetricRepliesReported][core]}
	}
	return lanes
}

// fail logs the cause, then answers 500 with a generic message: the same
// convention as every service.
func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	obs.ServerError(w, what, err)
	httpx.Error(w, what, http.StatusInternalServerError)
}
